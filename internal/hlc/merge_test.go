package hlc

import (
	"testing"
)

// TestMerge_FieldLevelLWW pins the central contract: two states
// edited concurrently on different fields produce a merge that
// keeps both edits. Row-level LWW would lose one.
func TestMerge_FieldLevelLWW(t *testing.T) {
	clock := New("node-a")
	t1 := clock.Now()
	t2 := clock.Now() // later than t1

	local := FieldState{
		FieldHLCs: map[string]string{"color": t1.String()},
		Fields:    map[string]any{"color": "blue"},
	}
	remote := FieldState{
		FieldHLCs: map[string]string{"note": t2.String()},
		Fields:    map[string]any{"note": "reviewed"},
	}
	merged, err := Merge(local, remote)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Fields["color"] != "blue" {
		t.Errorf("merge lost local field color: %+v", merged)
	}
	if merged.Fields["note"] != "reviewed" {
		t.Errorf("merge lost remote field note: %+v", merged)
	}
}

// TestMerge_LatestWriterWinsPerField guards the LWW semantic when
// both sides edited the same field.
func TestMerge_LatestWriterWinsPerField(t *testing.T) {
	clock := New("node-a")
	earlier := clock.Now()
	later := clock.Now()

	local := FieldState{
		FieldHLCs: map[string]string{"color": earlier.String()},
		Fields:    map[string]any{"color": "blue"},
	}
	remote := FieldState{
		FieldHLCs: map[string]string{"color": later.String()},
		Fields:    map[string]any{"color": "red"},
	}
	merged, err := Merge(local, remote)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Fields["color"] != "red" {
		t.Errorf("LWW lost — remote (later) should win: %+v", merged)
	}
}

// TestMerge_HandlesEmptyRemote tolerates one-sided merges.
func TestMerge_HandlesEmptyRemote(t *testing.T) {
	clock := New("node-a")
	t1 := clock.Now()
	local := FieldState{
		FieldHLCs: map[string]string{"color": t1.String()},
		Fields:    map[string]any{"color": "green"},
	}
	merged, err := Merge(local, FieldState{})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Fields["color"] != "green" {
		t.Errorf("empty remote merge dropped local field: %+v", merged)
	}
}

// TestMerge_RejectsMalformedHLC surfaces parse errors so a bad
// peer's payload doesn't silently lose data.
func TestMerge_RejectsMalformedHLC(t *testing.T) {
	bad := FieldState{
		FieldHLCs: map[string]string{"color": "not-an-hlc"},
		Fields:    map[string]any{"color": "x"},
	}
	good := FieldState{
		FieldHLCs: map[string]string{"color": New("n").Now().String()},
		Fields:    map[string]any{"color": "y"},
	}
	if _, err := Merge(bad, good); err == nil {
		t.Errorf("Merge should reject malformed hlc")
	}
}
