package store_test

import (
	"testing"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

func TestNormalizeStandaloneLoginModeCollapsed(t *testing.T) {
	cases := map[string]string{
		"opt_in":       store.StandaloneLoginModeEnabled,
		"all_accounts": store.StandaloneLoginModeEnabled,
		"enabled":      store.StandaloneLoginModeEnabled,
		"disabled":     store.StandaloneLoginModeDisabled,
		"":             store.StandaloneLoginModeDisabled,
	}
	for in, want := range cases {
		if got := store.NormalizeStandaloneLoginMode(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
