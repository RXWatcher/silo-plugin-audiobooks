package store_test

import (
	"bytes"
	"testing"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

func TestImportLegacyBackendConfigSeedsOnlyDefaultFields(t *testing.T) {
	st, ctx := newStore(t)
	secret := bytes.Repeat([]byte{1}, 32)
	if _, err := st.EnsureBackendConfig(ctx, secret); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	cfg, err := st.ImportLegacyBackendConfig(ctx, store.LegacyBackendConfig{
		StandaloneHTTPListen: "127.0.0.1:7878",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if cfg.StandaloneHTTPListen != "127.0.0.1:7878" {
		t.Fatalf("standalone listener not imported: %q", cfg.StandaloneHTTPListen)
	}

	if err := st.UpdateBackendConfig(ctx, store.BackendConfig{
		TargetBackendPluginID:  cfg.TargetBackendPluginID,
		TargetBackendInstallID: cfg.TargetBackendInstallID,
		TargetRequestPluginID:  cfg.TargetRequestPluginID,
		TargetRequestInstallID: cfg.TargetRequestInstallID,
		AutoApproveRequests:    cfg.AutoApproveRequests,
		ABSJWTSecret:           cfg.ABSJWTSecret,
		ABSAccessTTLHours:      cfg.ABSAccessTTLHours,
		ABSRefreshTTLDays:      cfg.ABSRefreshTTLDays,
		StandaloneHTTPListen:   "127.0.0.1:9999",
	}); err != nil {
		t.Fatalf("admin update: %v", err)
	}

	cfg, err = st.ImportLegacyBackendConfig(ctx, store.LegacyBackendConfig{
		StandaloneHTTPListen: "127.0.0.1:1111",
	})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if cfg.StandaloneHTTPListen != "127.0.0.1:9999" {
		t.Fatalf("legacy import overwrote standalone listener: %q", cfg.StandaloneHTTPListen)
	}
}

func TestEnsureBackendConfigDefaultsStandaloneListener(t *testing.T) {
	st, ctx := newStore(t)
	secret := bytes.Repeat([]byte{2}, 32)

	cfg, err := st.EnsureBackendConfig(ctx, secret)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if cfg.StandaloneHTTPListen != "127.0.0.1:9998" {
		t.Fatalf("unexpected standalone listener default: %q", cfg.StandaloneHTTPListen)
	}
}
