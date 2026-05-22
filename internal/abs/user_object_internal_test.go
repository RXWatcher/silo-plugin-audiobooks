package abs

import (
	"context"
	"testing"
)

func TestAbsUserObjectShape(t *testing.T) {
	h := &Handler{}
	obj := h.absUserObject(context.Background(), "u1", "", "lib1")
	for _, k := range []string{"mediaProgress", "permissions", "bookmarks", "username"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("user object missing %q", k)
		}
	}
	if obj["username"] != "u1" {
		t.Errorf("username = %v, want u1 (falls back to id when displayName empty)", obj["username"])
	}
}
