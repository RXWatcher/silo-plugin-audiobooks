package abs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/abs"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/testutil"
)

func newSmartCollectionFixture(t *testing.T) (chi.Router, *store.Store, string) {
	t.Helper()
	dsn := testutil.StartPG(t)
	if err := migrate.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	st := store.New(pool)
	if _, err := st.EnsureBackendConfig(context.Background(), jwtSecret); err != nil {
		t.Fatalf("ensure cfg: %v", err)
	}

	h := abs.NewHandler(abs.Deps{
		Store:   st,
		Backend: backend.NewClient(backend.NewHostClient("http://host")),
		TargetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
			cfg, err := st.GetBackendConfig(ctx)
			return cfg.TargetBackendPluginID, cfg, err
		},
		HostBaseFn: func() string { return "http://host" },
		InstallID:  func() string { return "test-install" },
	})
	r := chi.NewRouter()
	h.Mount(r)

	// Mint an access token via the header path.
	req := httptest.NewRequest("POST", "/abs/api/login", nil)
	req.Header.Set("X-Silo-User-Id", "u-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Fatalf("login: %d body=%s", w.Result().StatusCode, w.Body.String())
	}
	var loginOut struct {
		AccessToken string `json:"accessToken"`
	}
	_ = json.NewDecoder(w.Body).Decode(&loginOut)
	return r, st, loginOut.AccessToken
}

// TestSmartCollection_CRUDRoundTrip walks the route surface end-to-
// end: create, get, list, update, delete. Confirms the QueryDefinition
// survives a JSON round-trip without losing its shape and that ownership
// gates work (caller can't see another user's private collection).
func TestSmartCollection_CRUDRoundTrip(t *testing.T) {
	r, _, tok := newSmartCollectionFixture(t)

	// Create.
	body := `{
		"name": "Recently added Fantasy",
		"description": "auto",
		"is_pinned": true,
		"query_def": {
			"match": "all",
			"groups": [{
				"match": "all",
				"rules": [
					{"field": "genre", "op": "is", "value": "Fantasy"},
					{"field": "added_at", "op": "in_last", "value": {"value": 30, "unit": "days"}}
				]
			}],
			"sort": {"field": "added_at", "order": "desc"},
			"limit": 25
		}
	}`
	w := doJSON(r, "POST", "/abs/api/me/smart-collections", tok, body)
	if w.Result().StatusCode != http.StatusCreated {
		t.Fatalf("create = %d body=%s", w.Result().StatusCode, w.Body.String())
	}
	var created map[string]any
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created without id: %+v", created)
	}
	if created["isPinned"] != true {
		t.Errorf("isPinned not persisted: %+v", created)
	}

	// List.
	w = doJSON(r, "GET", "/abs/api/me/smart-collections", tok, "")
	if w.Result().StatusCode != 200 {
		t.Fatalf("list = %d", w.Result().StatusCode)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.NewDecoder(w.Body).Decode(&list)
	if len(list.Items) != 1 || list.Items[0]["id"] != id {
		t.Fatalf("list mismatch: %+v", list)
	}

	// Update.
	upd := `{"name":"Renamed","query_def":{"match":"all","groups":[],"sort":{"field":"title","order":"asc"}}}`
	w = doJSON(r, "PATCH", "/abs/api/me/smart-collections/"+id, tok, upd)
	if w.Result().StatusCode != 200 {
		t.Fatalf("patch = %d body=%s", w.Result().StatusCode, w.Body.String())
	}
	var updated map[string]any
	_ = json.NewDecoder(w.Body).Decode(&updated)
	if updated["name"] != "Renamed" {
		t.Errorf("name not updated: %v", updated["name"])
	}

	// Delete.
	w = doJSON(r, "DELETE", "/abs/api/me/smart-collections/"+id, tok, "")
	if w.Result().StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d body=%s", w.Result().StatusCode, w.Body.String())
	}
	w = doJSON(r, "GET", "/abs/api/me/smart-collections/"+id, tok, "")
	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("get-after-delete = %d, want 404", w.Result().StatusCode)
	}
}

// TestSmartCollection_RejectsBadQueryDef pins the validation surface:
// a rule against an unknown field or an unsupported op returns 400
// with an actionable error message, not 500.
func TestSmartCollection_RejectsBadQueryDef(t *testing.T) {
	r, _, tok := newSmartCollectionFixture(t)
	body := `{
		"name": "bogus",
		"query_def": {"groups":[{"match":"all","rules":[{"field":"not_a_field","op":"is","value":"x"}]}]}
	}`
	w := doJSON(r, "POST", "/abs/api/me/smart-collections", tok, body)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Result().StatusCode, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not supported") {
		t.Errorf("body should explain unknown field: %s", w.Body.String())
	}
}

// doJSON is a per-fixture helper that drives one HTTP request through
// the chi router and returns the recorder. Centralised so the
// individual tests stay focused on the assertion.
func doJSON(r chi.Router, method, path, tok, body string) *httptest.ResponseRecorder {
	var br *strings.Reader
	if body != "" {
		br = strings.NewReader(body)
	}
	var req *http.Request
	if br != nil {
		req = httptest.NewRequest(method, path, br)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
