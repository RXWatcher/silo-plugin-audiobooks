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
	"os"
	goruntime "runtime"
	"sync/atomic"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	publicmanifest "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/manifest"
	sdkruntime "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/abs"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/consumer"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/event"
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
		poolPtr  atomic.Pointer[pgxpool.Pool]
		storePtr atomic.Pointer[store.Store]
		cachePtr atomic.Pointer[streaming.Cache]
	)

	// Host base URL used to build self-referential public stream URLs in
	// the ABS handler. The continuum host doesn't expose its public URL via
	// the plugin SDK; we read CONTINUUM_HOST_URL from the env as a stopgap.
	hostBase := os.Getenv("CONTINUUM_HOST_URL")
	if hostBase == "" {
		hostBase = "http://localhost:8080"
	}
	bkClient := backend.NewClient(backend.NewHostClient(hostBase))

	rt := pluginrt.New(manifest, func(cfg pluginrt.Config) error {
		ctx := context.Background()

		p, err := pgxpool.New(ctx, cfg.DatabaseURL)
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
		_, _ = rand.Read(secret)
		bcfg, err := st.EnsureBackendConfig(ctx, secret)
		if err != nil {
			p.Close()
			return fmt.Errorf("ensure backend_config: %w", err)
		}

		// Wire streaming layer.
		var cache *streaming.Cache
		if bcfg.CacheDir != "" {
			maxBytes := int64(bcfg.CacheMaxSizeGB) * 1024 * 1024 * 1024
			cache = streaming.NewCache(bcfg.CacheDir, maxBytes, st)
		}
		streamRouter := streaming.NewRouter(st, bkClient, cache)

		ev := event.New(sdkruntime.Host(), logger)

		// ABS handler.
		absHandler := abs.NewHandler(abs.Deps{
			Store:   st,
			Backend: bkClient,
			Logger:  hclogAdapter{logger},
			TargetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
				cfg, err := st.GetBackendConfig(ctx)
				if err != nil {
					return "", store.BackendConfig{}, err
				}
				return cfg.TargetBackendPluginID, cfg, nil
			},
			HostBaseFn: func() string { return hostBase },
			InstallID:  func() string { return "continuum.audiobooks" },
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

		storePtr.Store(st)
		if cache != nil {
			cachePtr.Store(cache)
		}
		if old := poolPtr.Swap(p); old != nil {
			old.Close()
		}
		logger.Info("configured", "target_backend", bcfg.TargetBackendPluginID, "streaming_mode", bcfg.StreamingMode)
		return nil
	})

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
			Cache:   cachePtr.Load(),
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
