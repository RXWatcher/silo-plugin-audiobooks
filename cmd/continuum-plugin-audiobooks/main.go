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
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	redisadapter "github.com/zishang520/socket.io-go-redis/adapter"
	redistypes "github.com/zishang520/socket.io-go-redis/types"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	publicmanifest "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/manifest"
	sdkruntime "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/abs"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/abssocket"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/consumer"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/event"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/httproutes"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/podcastfeed"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/recommend"
	pluginrt "github.com/RXWatcher/continuum-plugin-audiobooks/internal/runtime"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/scheduler"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/server"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/streaming"
	"github.com/RXWatcher/continuum-plugin-audiobooks/web"
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
		standaloneMu     sync.Mutex   // serialises standalone-listener rebinds across Configure calls
		standaloneAddr   atomic.Value // string; the addr the listener is currently bound to
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

	// Per-IP rate limiter for the standalone-port body-creds login path.
	// Constructed once at process scope so the janitor goroutine doesn't
	// leak on each plugin reconfigure (NewHandler is called per-Configure).
	loginLimiter := abs.NewLoginLimiter()

	// Podcast feed refresher — process-scoped so its HTTP client is
	// reused across scheduler ticks and the admin force-refresh path.
	podcastRefresher := podcastfeed.New(hclogAdapter{logger})
	// Broadcaster is wired inside Configure once absHub exists.

	// Embedding-based recommender. Reads its config from the env;
	// when EMBEDDING_BASE_URL / EMBEDDING_MODEL aren't set, the
	// engine no-ops at every entry point so an unconfigured
	// deployment still works (just with no /similar shelf).
	embedCfg := recommend.LoadConfigFromEnv(os.Getenv)
	var recommender *recommend.Engine // set inside Configure when store is ready

	// Socket.io realtime hub for ABS clients. The JWT secret comes from the
	// active backend_config (read on every auth handshake so admin rotates
	// take effect for new connections without a plugin restart). The store
	// is accessed via the storePtr atomic so it survives reconfigures —
	// while storePtr is nil (pre-Configure), the auth handler refuses
	// connections rather than trusting JWTs against a missing revocation
	// list. Mounted on the standalone listener only; see httproutes.
	// Optional multi-replica adapter for the Socket.io hub. When the
	// operator deploys the plugin as more than one instance behind a
	// sticky-session-aware load balancer, set CONTINUUM_REDIS_URL so
	// events published on replica A reach a client connected to replica
	// B. Empty/unset → single-replica in-memory adapter (default).
	socketOpts := buildABSSocketOptions(logger)
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
		socketOpts,
	)
	httpSrv.SetSocketHandler(absHub.Handler())

	// Wire the broadcaster into the podcast refresher now that the
	// hub exists. The refresher emits episode_download_finished on
	// each completed feed refresh, so connected ABS clients get a
	// shelf-refresh hint without polling.
	podcastRefresher.SetBroadcaster(absHub)

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
		// take effect without a plugin restart. The Router threads the
		// inbound request's context through so a stalled DB lookup is
		// cancellable when the client disconnects.
		streamSecret := func(reqCtx context.Context) string {
			cfg, err := st.GetBackendConfig(reqCtx)
			if err != nil {
				return ""
			}
			return cfg.MediaSigningSecret
		}
		streamRouter := streaming.NewRouter(bkClient, streamSecret)

		ev := event.New(sdkruntime.Host(), logger)

		// Construct the recommender now that the store is wired.
		// Reused across the ABS handler + consumer hook + scheduler.
		recommender = recommend.New(embedCfg, st, bkClient, hclogAdapter{logger})

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
			HostBaseFn:    func() string { return hostBase },
			InstallID:     func() string { return "continuum.audiobooks" },
			CredValidator: sdkruntime.Host(),
			LoginLimiter:  loginLimiter,
			Publisher:     absHub,
			Recommender:   recommender,
		})

		srv := server.New(server.Deps{
			PodcastFeed: podcastRefresher,
			Store:       st,
			Backend:     bkClient,
			Events:      ev,
			Streaming:   streamRouter,
			ABS:         absHandler,
			SPA:         web.SPAHandler(),
			HostBaseFn:  func() string { return hostBase },
			Broadcaster: absHub,
		})
		httpSrv.SetHandler(srv.Handler())

		// Standalone HTTP listener for direct ABS client apps. The bind
		// address lives in backend_config and is managed by the admin SPA.
		// When it changes — including the first Configure — drain the old
		// listener and start a fresh one so an edit takes effect without a
		// manual plugin restart. (The previous sync.Once binding froze the
		// addr at first boot, so a later SPA/DB change silently never
		// applied — see docs/2026-05-21-standalone-abs-login.md.)
		{
			addr := bcfg.StandaloneHTTPListen
			standaloneMu.Lock()
			prev, _ := standaloneAddr.Load().(string)
			if prev != addr {
				if old := standaloneSrvPtr.Swap(nil); old != nil {
					logger.Info("standalone http listener rebinding", "from", prev, "to", addr)
					drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					if err := old.Shutdown(drainCtx); err != nil {
						logger.Warn("standalone http listener drain returned error", "err", err)
					}
					cancel()
				}
				standaloneAddr.Store(addr)
				if addr != "" {
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
				}
			}
			standaloneMu.Unlock()
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
		return &consumer.Deps{Store: st, Broadcast: absHub}
	}, logger)

	// Scheduled tasks.
	sched := scheduler.New(func() *scheduler.Deps {
		st := storePtr.Load()
		if st == nil {
			return nil
		}
		return &scheduler.Deps{
			Store:       st,
			Backend:     bkClient,
			PodcastFeed: podcastRefresher,
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
func (a hclogAdapter) Info(msg string, args ...any)  { a.l.Info(msg, args...) }
func (a hclogAdapter) Debug(msg string, args ...any) { a.l.Debug(msg, args...) }

// buildABSSocketOptions constructs the optional adapter for the Socket.io
// hub. When CONTINUUM_REDIS_URL is set, we wire a Redis adapter so events
// published on one plugin replica reach clients connected to another.
// Empty/unset (the single-replica default) → nil options → built-in
// in-memory adapter.
//
// A malformed URL or an unreachable Redis is logged at warn level and
// falls back to the in-memory adapter rather than failing the boot — the
// plugin remains functional as a single-replica deployment even when
// Redis is misconfigured.
func buildABSSocketOptions(logger hclog.Logger) *abssocket.Options {
	raw := strings.TrimSpace(os.Getenv("CONTINUUM_REDIS_URL"))
	if raw == "" {
		return nil
	}
	opts, err := goredis.ParseURL(raw)
	if err != nil {
		logger.Warn("CONTINUUM_REDIS_URL is set but unparseable; falling back to in-memory Socket.io adapter",
			"err", err.Error())
		return nil
	}
	rdb := goredis.NewClient(opts)
	rc := redistypes.NewRedisClient(context.Background(), rdb)
	rc.On("error", func(args ...any) {
		logger.Warn("Socket.io Redis adapter error", "args", args)
	})
	return &abssocket.Options{
		Adapter: &redisadapter.RedisAdapterBuilder{
			Redis: rc,
			Opts:  &redisadapter.RedisAdapterOptions{},
		},
	}
}

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
