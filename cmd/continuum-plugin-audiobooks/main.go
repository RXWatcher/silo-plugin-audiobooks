// Command continuum-plugin-audiobooks is the audiobooks portal plugin
// entrypoint. See README.md and the design spec at
// docs/superpowers/specs/2026-05-11-audiobooks-portal-and-bookwarehouse-backend-design.md.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	publicmanifest "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/manifest"
	sdkruntime "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/abs"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/abssocket"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/consumer"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/event"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/hostlogin"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/httproutes"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/migrate"
	pluginrt "github.com/ContinuumApp/continuum-plugin-audiobooks/internal/runtime"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/scheduler"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/server"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/streaming"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/web"
)

//go:embed manifest.json
var manifestRaw []byte

func main() {
	logger := hclog.New(&hclog.LoggerOptions{Name: "continuum-plugin-audiobooks"})

	manifest, err := loadManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load manifest: %v\n", err)
		os.Exit(1)
	}

	httpSrv := httproutes.NewServer()

	var (
		poolPtr          atomic.Pointer[pgxpool.Pool]
		storePtr         atomic.Pointer[store.Store]
		standaloneOnce   sync.Once
		standaloneAddr   atomic.Value // string; tracks the bound addr so reconfigures can warn on change
		standaloneSrvPtr atomic.Pointer[http.Server]
	)

	// Host base URL used to build self-referential public stream URLs in
	// the ABS handler. Prefer the public host URL when available, but fall
	// back to the host API base for local/dev setups.
	hostBase := os.Getenv("CONTINUUM_HOST_URL")
	if hostBase == "" {
		hostBase = os.Getenv("CONTINUUM_HOST_BASE_URL")
	}
	if hostBase == "" {
		hostBase = "http://localhost:8080"
	}
	hostToken := os.Getenv("CONTINUUM_PLUGIN_TOKEN")
	bkClient := backend.NewClient(backend.NewHostClient(hostBase).WithServiceToken(hostToken).WithRuntimeHost(sdkruntime.Host()))

	// Host-login client used by the ABS standalone-port body-creds path. It
	// posts to {hostBase}/api/v1/auth/login with provider="local". When
	// hostBase is unreachable the handler returns 502, so the client is safe
	// to construct eagerly.
	hostLoginClient := hostlogin.New(hostBase)

	// Per-IP rate limiter for the standalone-port body-creds login path.
	// Constructed once at process scope so the janitor goroutine doesn't
	// leak on each plugin reconfigure (NewHandler is called per-Configure).
	loginLimiter := abs.NewLoginLimiter()

	// Socket.io realtime hub for ABS clients. The JWT secret comes from the
	// active backend_config (read on every auth handshake so admin rotates
	// take effect for new connections without a plugin restart). The store
	// is accessed via the storePtr atomic so it survives reconfigures —
	// while storePtr is nil (pre-Configure), the auth handler refuses
	// connections rather than trusting JWTs against a missing revocation
	// list. Mounted on the standalone listener only; see httproutes.
	absHub := abssocket.New(
		func() []byte {
			st := storePtr.Load()
			if st == nil {
				return nil
			}
			cfg, err := st.GetBackendConfig(context.Background())
			if err != nil {
				return nil
			}
			return cfg.ABSJWTSecret
		},
		func() *store.Store { return storePtr.Load() },
		hclogAdapter{logger},
	)
	httpSrv.SetSocketHandler(absHub.Handler())

	rt := pluginrt.New(manifest, func(cfg pluginrt.Config) error {
		ctx := context.Background()

		// Explicit MaxConns cap. The pgx default scales with GOMAXPROCS and
		// can be as low as 4; the portal + scheduler + ABS streaming mix
		// can starve under that. 16 is generous without saturating a
		// shared Postgres. Operators override via DSN (?pool_max_conns=N).
		pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
		if err != nil {
			return fmt.Errorf("parse db: %w", err)
		}
		if pcfg.MaxConns < 16 {
			pcfg.MaxConns = 16
		}
		p, err := pgxpool.NewWithConfig(ctx, pcfg)
		if err != nil {
			return fmt.Errorf("pgxpool: %w", err)
		}
		if err := migrate.Run(ctx, cfg.DatabaseURL); err != nil {
			p.Close()
			return fmt.Errorf("migrate: %w", err)
		}

		st := store.New(p)

		// Initialize backend_config singleton with a random JWT secret on
		// first Configure.
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			p.Close()
			return fmt.Errorf("generate abs jwt secret: %w", err)
		}
		bcfg, err := st.EnsureBackendConfig(ctx, secret)
		if err != nil {
			p.Close()
			return fmt.Errorf("ensure backend_config: %w", err)
		}
		if imported, err := st.ImportLegacyBackendConfig(ctx, store.LegacyBackendConfig{
			StandaloneHTTPListen: cfg.StandaloneHTTPListen,
		}); err != nil {
			p.Close()
			return fmt.Errorf("import legacy backend_config: %w", err)
		} else {
			bcfg = imported
		}

		// Wire streaming layer — a thin 302 to the backend's stream URL with
		// a freshly-minted signed media token. The SecretProvider reads from
		// the store on each call so admin updates to media_signing_secret
		// take effect without a plugin restart.
		streamSecret := func() string {
			cfg, err := st.GetBackendConfig(ctx)
			if err != nil {
				return ""
			}
			return cfg.MediaSigningSecret
		}
		streamRouter := streaming.NewRouter(bkClient, streamSecret)

		ev := event.New(sdkruntime.Host(), logger)

		// ABS handler.
		absHandler := abs.NewHandler(abs.Deps{
			Store:     st,
			Backend:   bkClient,
			Streaming: streamRouter,
			Logger:    hclogAdapter{logger},
			TargetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
				cfg, err := st.GetBackendConfig(ctx)
				if err != nil {
					return "", store.BackendConfig{}, err
				}
				return cfg.BackendInstallID(), cfg, nil
			},
			HostBaseFn:   func() string { return hostBase },
			InstallID:    func() string { return "continuum.audiobooks" },
			HostLogin:    hostLoginClient,
			LoginLimiter: loginLimiter,
			Publisher:    absHub,
		})

		srv := server.New(server.Deps{
			Store:      st,
			Backend:    bkClient,
			Events:     ev,
			Streaming:  streamRouter,
			ABS:        absHandler,
			SPA:        web.SPAHandler(),
			HostBaseFn: func() string { return hostBase },
		})
		httpSrv.SetHandler(srv.Handler())

		// Optional standalone HTTP listener for direct client apps. The value
		// lives in backend_config and is managed by the admin SPA. Bound once
		// at first Configure; subsequent changes require a plugin restart.
		if addr := bcfg.StandaloneHTTPListen; addr != "" {
			started := false
			standaloneOnce.Do(func() {
				started = true
				standaloneAddr.Store(addr)
				sl := &http.Server{
					Addr:              addr,
					Handler:           httpSrv,
					ReadHeaderTimeout: 10 * time.Second,
					ReadTimeout:       60 * time.Second,
					WriteTimeout:      120 * time.Second,
					IdleTimeout:       120 * time.Second,
				}
				standaloneSrvPtr.Store(sl)
				go func() {
					logger.Info("standalone http listener starting", "addr", addr)
					if err := sl.ListenAndServe(); err != nil && err != http.ErrServerClosed {
						logger.Error("standalone http listener failed", "addr", addr, "err", err)
					}
				}()
			})
			if !started {
				if prev, _ := standaloneAddr.Load().(string); prev != addr {
					logger.Warn("standalone_http_listen changed; restart the plugin to apply",
						"current", prev, "requested", addr)
				}
			}
		}

		storePtr.Store(st)
		if old := poolPtr.Swap(p); old != nil {
			old.Close()
		}
		logger.Info("configured", "target_backend", bcfg.TargetBackendPluginID)
		return nil
	})

	// Graceful shutdown for the standalone HTTP listener (if it bound a port
	// during Configure). On SIGTERM/SIGINT we call Shutdown(ctx) with a 10s
	// drain window so in-flight client-app requests (ABS streams especially)
	// finish instead of being killed mid-byte by process exit. signal.Notify
	// fanning to multiple subscribers is documented and safe; the SDK
	// runtime's own signal handler keeps running independently.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		if sl := standaloneSrvPtr.Load(); sl != nil {
			logger.Info("draining standalone http listener", "addr", sl.Addr)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := sl.Shutdown(ctx); err != nil {
				logger.Warn("standalone http drain returned error", "err", err)
			}
		}
	}()

	// Status watcher (event consumer).
	cons := consumer.New(func() *consumer.Deps {
		st := storePtr.Load()
		if st == nil {
			return nil
		}
		return &consumer.Deps{Store: st}
	}, logger)

	// Scheduled tasks.
	sched := scheduler.New(func() *scheduler.Deps {
		st := storePtr.Load()
		if st == nil {
			return nil
		}
		return &scheduler.Deps{
			Store:   st,
			Backend: bkClient,
		}
	}, logger)

	sdkruntime.Serve(sdkruntime.ServeConfig{
		Logger: logger,
		Servers: sdkruntime.CapabilityServers{
			Runtime:       rt,
			HttpRoutes:    httpSrv,
			EventConsumer: cons,
			ScheduledTask: sched,
		},
	})
}

// hclogAdapter narrows hclog.Logger into the abs.Logger interface.
type hclogAdapter struct{ l hclog.Logger }

func (a hclogAdapter) Warn(msg string, args ...any)  { a.l.Warn(msg, args...) }
func (a hclogAdapter) Debug(msg string, args ...any) { a.l.Debug(msg, args...) }

func loadManifest() (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.Load(manifestRaw)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}
	executablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	binaryData, err := os.ReadFile(executablePath)
	if err != nil {
		return nil, fmt.Errorf("read executable %q: %w", executablePath, err)
	}
	checksum := sha256.Sum256(binaryData)
	manifest.Checksum = hex.EncodeToString(checksum[:])
	if len(manifest.GetSupportedPlatforms()) == 0 {
		manifest.SupportedPlatforms = []*pluginv1.SupportedPlatform{
			{Os: goruntime.GOOS, Arch: goruntime.GOARCH},
		}
	}
	return manifest, nil
}
