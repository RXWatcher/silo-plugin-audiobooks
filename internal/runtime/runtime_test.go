package runtime

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func cfgReq(t *testing.T, kv map[string]any) *pluginv1.ConfigureRequest {
	t.Helper()
	entries := make([]*pluginv1.ConfigEntry, 0, len(kv))
	for k, v := range kv {
		s, err := structpb.NewStruct(map[string]any{"value": v})
		if err != nil {
			t.Fatalf("structpb: %v", err)
		}
		entries = append(entries, &pluginv1.ConfigEntry{Key: k, Value: s})
	}
	return &pluginv1.ConfigureRequest{Config: entries}
}

// The DSN embeds the DB password and must never appear when a Config is logged
// or formatted.
func TestConfigRedaction(t *testing.T) {
	cfg := Config{
		DatabaseURL:          "postgres://user:sup3rsecret@db:5432/silo",
		StandaloneHTTPListen: "127.0.0.1:9999",
	}

	if s := cfg.String(); strings.Contains(s, "sup3rsecret") {
		t.Fatalf("String() leaked a secret: %s", s)
	}

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cfg", "config", cfg)
	out := buf.String()
	if strings.Contains(out, "sup3rsecret") {
		t.Fatalf("slog leaked a secret: %s", out)
	}
	if !strings.Contains(out, "127.0.0.1:9999") {
		t.Fatalf("redaction hid non-secret fields: %s", out)
	}

	// An empty secret stays empty (no spurious marker).
	if (Config{}).LogValue().String() == "" {
		t.Fatal("LogValue should still render group for an empty config")
	}
}
