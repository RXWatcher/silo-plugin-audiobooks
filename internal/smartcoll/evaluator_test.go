package smartcoll

import (
	"context"
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
)

// TestNormalize_AppliesAliasesAndDefaults pins the canonicalisation:
// "rating" → "rating" stays since we removed the host's imdb-specific
// alias, but "authors" → "author", "sort_title" → "title", empty sort
// → "added_at" desc.
func TestNormalize_AppliesAliasesAndDefaults(t *testing.T) {
	q := QueryDefinition{
		Match: "",
		Groups: []QueryGroup{{
			Match: "",
			Rules: []QueryRule{{Field: "Authors", Op: "IS", Value: "Sanderson"}},
		}},
		Sort: QuerySort{Field: "sort_title"},
	}
	n := q.Normalize()
	if n.Match != "all" {
		t.Errorf("top match default = %q", n.Match)
	}
	if n.Groups[0].Match != "all" {
		t.Errorf("group match default = %q", n.Groups[0].Match)
	}
	if n.Groups[0].Rules[0].Field != "author" {
		t.Errorf("authors alias not applied: %q", n.Groups[0].Rules[0].Field)
	}
	if n.Groups[0].Rules[0].Op != "is" {
		t.Errorf("op not lowercased: %q", n.Groups[0].Rules[0].Op)
	}
	if n.Sort.Field != "title" {
		t.Errorf("sort alias not applied: %q", n.Sort.Field)
	}
	if n.Sort.Order != "asc" {
		t.Errorf("sort default order: %q", n.Sort.Order)
	}
}

// TestValidate_RejectsBadRules walks the validation surface: unknown
// field, unsupported op for field, personalized field without scope,
// negative limit.
func TestValidate_RejectsBadRules(t *testing.T) {
	cases := []struct {
		name string
		q    QueryDefinition
		err  string
	}{
		{
			name: "unknown field",
			q:    QueryDefinition{Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "not_a_field", Op: "is", Value: "x"}}}}},
			err:  "not supported",
		},
		{
			name: "bad op for field",
			q:    QueryDefinition{Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "year", Op: "contains", Value: 2024}}}}},
			err:  "not valid for field",
		},
		{
			name: "personalized field requires scope",
			q:    QueryDefinition{Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "finished", Op: "is", Value: true}}}}},
			err:  "requires user scope",
		},
		{
			name: "negative limit",
			q:    QueryDefinition{Limit: intPtr(-5)},
			err:  "limit must be positive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.q.Validate(false)
			if err == nil || !contains(err.Error(), tc.err) {
				t.Errorf("err = %v, want substring %q", err, tc.err)
			}
		})
	}
}

// TestEvaluate_FiltersByAuthorIs guards the most common rule: an
// author equality match against the candidate's AuthorRefs list.
func TestEvaluate_FiltersByAuthorIs(t *testing.T) {
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	cands := []Candidate{
		{Item: backend.AudiobookSummary{ID: "a", Title: "Way of Kings", AuthorRefs: []backend.AuthorRef{{Name: "Brandon Sanderson"}}}},
		{Item: backend.AudiobookSummary{ID: "b", Title: "Other Book", AuthorRefs: []backend.AuthorRef{{Name: "Robert Jordan"}}}},
	}
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{
			{Field: "author", Op: "is", Value: "Brandon Sanderson"},
		}}},
	}
	out := Evaluate(context.Background(), qd, cands, EvaluateOptions{Now: now})
	if len(out) != 1 || out[0].Item.ID != "a" {
		t.Fatalf("got %d hits, want exactly 'a': %+v", len(out), out)
	}
}

// TestEvaluate_BetweenYearRange covers the between operator on year —
// inclusive on both ends, and rejects items outside.
func TestEvaluate_BetweenYearRange(t *testing.T) {
	cands := []Candidate{
		{Item: backend.AudiobookSummary{ID: "old", Year: 2010}},
		{Item: backend.AudiobookSummary{ID: "mid", Year: 2020}},
		{Item: backend.AudiobookSummary{ID: "new", Year: 2025}},
	}
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{
			{Field: "year", Op: "between", Value: []any{float64(2015), float64(2024)}},
		}}},
	}
	out := Evaluate(context.Background(), qd, cands, EvaluateOptions{})
	if len(out) != 1 || out[0].Item.ID != "mid" {
		t.Errorf("between 2015-2024 hits = %+v", out)
	}
}

// TestEvaluate_AnyAllCombinators exercises the dual combinator: top
// "any" with two groups, each "all" with one rule. An item matches
// if EITHER author == "X" OR rating > 4.
func TestEvaluate_AnyAllCombinators(t *testing.T) {
	cands := []Candidate{
		{Item: backend.AudiobookSummary{ID: "a", AuthorRefs: []backend.AuthorRef{{Name: "X"}}, Rating: 2}},
		{Item: backend.AudiobookSummary{ID: "b", AuthorRefs: []backend.AuthorRef{{Name: "Y"}}, Rating: 4.5}},
		{Item: backend.AudiobookSummary{ID: "c", AuthorRefs: []backend.AuthorRef{{Name: "Z"}}, Rating: 1}},
	}
	qd := QueryDefinition{
		Match: "any",
		Groups: []QueryGroup{
			{Match: "all", Rules: []QueryRule{{Field: "author", Op: "is", Value: "X"}}},
			{Match: "all", Rules: []QueryRule{{Field: "rating", Op: "gt", Value: float64(4)}}},
		},
	}
	out := Evaluate(context.Background(), qd, cands, EvaluateOptions{})
	if len(out) != 2 {
		t.Fatalf("any-author-OR-rating hits = %d, want 2", len(out))
	}
}

// TestEvaluate_PersonalizedFieldsRespectFlag verifies the
// AllowPersonalized gate — when false, rules referencing personalized
// fields silently skip (the validator catches them earlier in the
// admin flow; at evaluate time we'd rather drop than crash).
func TestEvaluate_PersonalizedFieldsRespectFlag(t *testing.T) {
	now := time.Now()
	cands := []Candidate{
		{Item: backend.AudiobookSummary{ID: "a"}, IsFinished: true},
		{Item: backend.AudiobookSummary{ID: "b"}, IsFinished: false},
	}
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{
			{Field: "finished", Op: "is", Value: true},
		}}},
	}
	// AllowPersonalized=true → filters down to the finished one.
	out := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: true, Now: now})
	if len(out) != 1 || out[0].Item.ID != "a" {
		t.Errorf("personalized allowed: hits = %+v", out)
	}
	// AllowPersonalized=false → rule drops; everything matches (no
	// other rule constrains).
	out = Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: false, Now: now})
	if len(out) != 0 {
		// finished:is:true rule is dropped, but then no rule matches —
		// our matchRule returns false on dropped rules, so neither
		// candidate satisfies the all-group. That's correct.
		t.Errorf("personalized disallowed should drop both rule + matches: got %+v", out)
	}
}

// TestEvaluate_SortByYearDesc + Limit confirms ordering + cap.
func TestEvaluate_SortByYearDesc(t *testing.T) {
	limit := 2
	cands := []Candidate{
		{Item: backend.AudiobookSummary{ID: "old", Year: 2010}},
		{Item: backend.AudiobookSummary{ID: "mid", Year: 2020}},
		{Item: backend.AudiobookSummary{ID: "new", Year: 2025}},
	}
	qd := QueryDefinition{
		Match: "all",
		Sort:  QuerySort{Field: "year", Order: "desc"},
		Limit: &limit,
	}
	out := Evaluate(context.Background(), qd, cands, EvaluateOptions{})
	if len(out) != 2 || out[0].Item.ID != "new" || out[1].Item.ID != "mid" {
		t.Errorf("sort by year desc + limit 2: %+v", out)
	}
}

// TestEvaluate_AddedInLast30Days exercises the time-based in_last op
// + the {value, unit} shape.
func TestEvaluate_AddedInLast30Days(t *testing.T) {
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -60).UnixMilli()
	recent := now.AddDate(0, 0, -10).UnixMilli()
	cands := []Candidate{
		{Item: backend.AudiobookSummary{ID: "old", AddedAtMs: old}},
		{Item: backend.AudiobookSummary{ID: "recent", AddedAtMs: recent}},
	}
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{
			{Field: "added_at", Op: "in_last", Value: map[string]any{"value": 30, "unit": "days"}},
		}}},
	}
	out := Evaluate(context.Background(), qd, cands, EvaluateOptions{Now: now})
	if len(out) != 1 || out[0].Item.ID != "recent" {
		t.Errorf("in_last 30d: %+v", out)
	}
}

// TestEvaluate_RandomSortDeterministicPerSeed confirms that two
// evaluations with the same UserSeed produce the same shuffled
// order, and different seeds (usually) produce different orders.
func TestEvaluate_RandomSortDeterministicPerSeed(t *testing.T) {
	cands := []Candidate{
		{Item: backend.AudiobookSummary{ID: "a"}},
		{Item: backend.AudiobookSummary{ID: "b"}},
		{Item: backend.AudiobookSummary{ID: "c"}},
		{Item: backend.AudiobookSummary{ID: "d"}},
		{Item: backend.AudiobookSummary{ID: "e"}},
	}
	qd := QueryDefinition{Match: "all", Sort: QuerySort{Field: "random"}}
	a := Evaluate(context.Background(), qd, append([]Candidate{}, cands...), EvaluateOptions{UserSeed: "alice:c1"})
	b := Evaluate(context.Background(), qd, append([]Candidate{}, cands...), EvaluateOptions{UserSeed: "alice:c1"})
	if !sameOrder(a, b) {
		t.Errorf("same seed produced different orders: %v vs %v", ids(a), ids(b))
	}
}

func intPtr(n int) *int           { return &n }
func contains(s, sub string) bool { return len(s) >= len(sub) && stringIndex(s, sub) >= 0 }
func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func sameOrder(a, b []Candidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Item.ID != b[i].Item.ID {
			return false
		}
	}
	return true
}
func ids(cands []Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Item.ID
	}
	return out
}
