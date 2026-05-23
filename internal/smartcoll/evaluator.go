package smartcoll

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
)

// Candidate is the input shape to Evaluate — one backend item plus the
// optional per-user state needed by personalized rules. The audiobook
// handler builds these by fetching backend summaries and merging
// progress + bookmark counts for the requesting user.
//
// Progress / BookmarkCount default to zero values; rules that read
// them will treat that as "no progress yet" — which matches the
// audiobook UX (a book with no row in `progress` is "not started").
type Candidate struct {
	Item            backend.AudiobookSummary
	IsFinished      bool
	ProgressPct     float32
	CurrentSeconds  int
	LastPlayedAt    time.Time // zero when never played
	BookmarkCount   int
	PlayCount       int
}

// EvaluateOptions controls non-rule aspects of evaluation. UserSeed is
// used by the `random` sort to keep ordering stable for a given
// (user, collection) pair across paginated requests.
type EvaluateOptions struct {
	// AllowPersonalized makes rules referencing per-user state
	// (finished, in_progress, last_played, abandoned, bookmark_count)
	// evaluable. When false, those rules are dropped — useful when the
	// evaluator runs in a system context with no user identity.
	AllowPersonalized bool
	// UserSeed seeds the `random` sort. Pass the user_id ULID so the
	// shuffle is per-user-stable; pass a fresh string for a fresh
	// shuffle every call.
	UserSeed string
	// Now is the reference time for relative comparisons like
	// `in_last`. Defaults to time.Now() when zero.
	Now time.Time
	// AbandonedAfter is how long since LastPlayedAt a partially-read
	// book must be to count as "abandoned". 60 days mirrors a common
	// reader-app default.
	AbandonedAfter time.Duration
}

// Evaluate filters the candidate list by qd's rule tree and sorts the
// survivors by qd.Sort, returning at most qd.Limit results (or all
// when Limit is nil). qd is normalised before evaluation so callers
// don't need to pre-normalize. Pure function — no side effects, no
// I/O, safe to share across goroutines.
func Evaluate(ctx context.Context, qd QueryDefinition, candidates []Candidate, opts EvaluateOptions) []Candidate {
	_ = ctx // reserved for future SQL-pushdown variant
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.AbandonedAfter == 0 {
		opts.AbandonedAfter = 60 * 24 * time.Hour
	}
	qd = qd.Normalize()
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if matchDefinition(qd, c, opts) {
			out = append(out, c)
		}
	}
	sortCandidates(out, qd.Sort, opts)
	if qd.Limit != nil && *qd.Limit > 0 && *qd.Limit < len(out) {
		out = out[:*qd.Limit]
	}
	return out
}

// matchDefinition walks the top-level groups with qd.Match as the
// combinator. Empty groups list matches everything (the "no rules" UX
// — a new collection with just a name).
func matchDefinition(qd QueryDefinition, c Candidate, opts EvaluateOptions) bool {
	if len(qd.Groups) == 0 {
		return true
	}
	if qd.Match == "any" {
		for _, g := range qd.Groups {
			if matchGroup(g, c, opts) {
				return true
			}
		}
		return false
	}
	// all
	for _, g := range qd.Groups {
		if !matchGroup(g, c, opts) {
			return false
		}
	}
	return true
}

func matchGroup(g QueryGroup, c Candidate, opts EvaluateOptions) bool {
	if len(g.Rules) == 0 {
		return true
	}
	if g.Match == "any" {
		for _, r := range g.Rules {
			if matchRule(r, c, opts) {
				return true
			}
		}
		return false
	}
	// all
	for _, r := range g.Rules {
		if !matchRule(r, c, opts) {
			return false
		}
	}
	return true
}

func matchRule(r QueryRule, c Candidate, opts EvaluateOptions) bool {
	def, ok := queryFieldDefs[r.Field]
	if !ok {
		return false
	}
	if def.personalized && !opts.AllowPersonalized {
		return false
	}
	switch r.Field {
	case "title":
		return cmpString(c.Item.Title, r)
	case "author":
		return cmpStringArray(c.Item.Authors, authorNamesFromRefs(c.Item.AuthorRefs), r)
	case "narrator":
		return cmpStringArray(c.Item.Narrators, nil, r)
	case "series":
		return cmpStringArray(nil, seriesNamesFromRefs(c.Item.SeriesRefs), r)
	case "genre":
		// Genres aren't on the summary; covered by detail. Treat as
		// no-data: rules can still match `is_not` (anything-not-equal-to-X
		// is true when there's no data), but `is`/`contains` against
		// empty returns false.
		return cmpStringArray(nil, nil, r)
	case "year":
		return cmpInt(c.Item.Year, r)
	case "rating":
		return cmpFloat(c.Item.Rating, r)
	case "language":
		// Language isn't on the summary; treat as no-data.
		return cmpString("", r)
	case "publisher":
		return cmpString("", r) // ditto
	case "added_at":
		return cmpTime(msToTime(c.Item.AddedAtMs), r, opts.Now)
	case "duration_seconds":
		return cmpInt(c.Item.DurationSeconds, r)
	case "finished":
		return cmpBool(c.IsFinished, r)
	case "in_progress":
		return cmpBool(!c.IsFinished && c.ProgressPct > 0, r)
	case "last_played":
		return cmpTime(c.LastPlayedAt, r, opts.Now)
	case "abandoned":
		abandoned := !c.IsFinished && c.ProgressPct > 0 &&
			!c.LastPlayedAt.IsZero() &&
			opts.Now.Sub(c.LastPlayedAt) >= opts.AbandonedAfter
		return cmpBool(abandoned, r)
	case "bookmark_count":
		return cmpInt(c.BookmarkCount, r)
	}
	return false
}

func sortCandidates(items []Candidate, s QuerySort, opts EvaluateOptions) {
	descending := s.Order == "desc"
	switch s.Field {
	case "title":
		sortBy(items, descending, func(c Candidate) string { return strings.ToLower(c.Item.Title) })
	case "added_at":
		sortByInt64(items, descending, func(c Candidate) int64 { return c.Item.AddedAtMs })
	case "year":
		sortByInt(items, descending, func(c Candidate) int { return c.Item.Year })
	case "duration_seconds":
		sortByInt(items, descending, func(c Candidate) int { return c.Item.DurationSeconds })
	case "rating":
		sortByFloat(items, descending, func(c Candidate) float64 { return c.Item.Rating })
	case "progress":
		sortByFloat(items, descending, func(c Candidate) float64 { return float64(c.ProgressPct) })
	case "last_played":
		sortByInt64(items, descending, func(c Candidate) int64 { return c.LastPlayedAt.UnixMilli() })
	case "plays":
		sortByInt(items, descending, func(c Candidate) int { return c.PlayCount })
	case "random":
		shuffleSeeded(items, opts.UserSeed)
	default:
		// Unknown field — fall through to added_at desc.
		sortByInt64(items, true, func(c Candidate) int64 { return c.Item.AddedAtMs })
	}
}

// -------- Comparison helpers --------

func cmpString(v string, r QueryRule) bool {
	rv, _ := r.Value.(string)
	switch r.Op {
	case "is":
		return strings.EqualFold(v, rv)
	case "is_not":
		return !strings.EqualFold(v, rv)
	case "contains":
		return strings.Contains(strings.ToLower(v), strings.ToLower(rv))
	}
	return false
}

// cmpStringArray runs the op against the union of plain-string and
// reference-derived names. Empty inputs match `is_not` on any non-empty
// rule value (the "not in" semantic when no data is available).
func cmpStringArray(plain []string, fromRefs []string, r QueryRule) bool {
	rv, _ := r.Value.(string)
	rvLower := strings.ToLower(rv)
	all := make([]string, 0, len(plain)+len(fromRefs))
	all = append(all, plain...)
	all = append(all, fromRefs...)

	switch r.Op {
	case "is":
		for _, s := range all {
			if strings.EqualFold(s, rv) {
				return true
			}
		}
		return false
	case "is_not":
		for _, s := range all {
			if strings.EqualFold(s, rv) {
				return false
			}
		}
		return true
	case "contains":
		for _, s := range all {
			if strings.Contains(strings.ToLower(s), rvLower) {
				return true
			}
		}
		return false
	}
	return false
}

func cmpInt(v int, r QueryRule) bool {
	switch r.Op {
	case "is":
		return v == intValue(r.Value)
	case "is_not":
		return v != intValue(r.Value)
	case "gt":
		return v > intValue(r.Value)
	case "gte":
		return v >= intValue(r.Value)
	case "lt":
		return v < intValue(r.Value)
	case "lte":
		return v <= intValue(r.Value)
	case "between":
		lo, hi := intRangeValue(r.Value)
		return v >= lo && v <= hi
	}
	return false
}

func cmpFloat(v float64, r QueryRule) bool {
	switch r.Op {
	case "gt":
		return v > floatValue(r.Value)
	case "gte":
		return v >= floatValue(r.Value)
	case "lt":
		return v < floatValue(r.Value)
	case "lte":
		return v <= floatValue(r.Value)
	case "between":
		lo, hi := floatRangeValue(r.Value)
		return v >= lo && v <= hi
	}
	return false
}

func cmpBool(v bool, r QueryRule) bool {
	rv, _ := r.Value.(bool)
	if r.Op == "is" {
		return v == rv
	}
	return false
}

// cmpTime supports gt/gte/lt/lte/between against RFC3339 strings (or
// Unix-ms ints) and in_last against {value: N, unit: "days"|"hours"|...}.
func cmpTime(v time.Time, r QueryRule, now time.Time) bool {
	if r.Op == "in_last" {
		d := parseDuration(r.Value, now)
		if d == 0 {
			return false
		}
		cutoff := now.Add(-d)
		return !v.IsZero() && v.After(cutoff)
	}
	rv := parseTime(r.Value)
	switch r.Op {
	case "gt":
		return !v.IsZero() && v.After(rv)
	case "gte":
		return !v.IsZero() && !v.Before(rv)
	case "lt":
		return !v.IsZero() && v.Before(rv)
	case "lte":
		return !v.IsZero() && !v.After(rv)
	case "between":
		lo, hi := parseTimeRange(r.Value)
		return !v.IsZero() && !v.Before(lo) && !v.After(hi)
	}
	return false
}

// -------- Value-parsing helpers --------

func intValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func intRangeValue(v any) (int, int) {
	switch r := v.(type) {
	case []any:
		if len(r) == 2 {
			return intValue(r[0]), intValue(r[1])
		}
	case []int:
		if len(r) == 2 {
			return r[0], r[1]
		}
	case []float64:
		if len(r) == 2 {
			return int(r[0]), int(r[1])
		}
	}
	return 0, 0
}

func floatValue(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func floatRangeValue(v any) (float64, float64) {
	switch r := v.(type) {
	case []any:
		if len(r) == 2 {
			return floatValue(r[0]), floatValue(r[1])
		}
	case []float64:
		if len(r) == 2 {
			return r[0], r[1]
		}
	}
	return 0, 0
}

// parseTime accepts RFC3339 strings, Unix-ms ints, and Unix-second
// floats. Zero time on any failure.
func parseTime(v any) time.Time {
	switch x := v.(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, x); err == nil {
			return t
		}
	case int64:
		return time.UnixMilli(x)
	case float64:
		return time.UnixMilli(int64(x))
	case int:
		return time.UnixMilli(int64(x))
	}
	return time.Time{}
}

// parseTimeRange unpacks a 2-tuple of times.
func parseTimeRange(v any) (time.Time, time.Time) {
	if r, ok := v.([]any); ok && len(r) == 2 {
		return parseTime(r[0]), parseTime(r[1])
	}
	return time.Time{}, time.Time{}
}

// parseDuration accepts the in_last rule shape:
//
//	{"value": 30, "unit": "days"}
//
// or a bare int (interpreted as days) or a string ("30d" / "12h" /
// "2w"). Returns 0 when no match.
func parseDuration(v any, _ time.Time) time.Duration {
	switch x := v.(type) {
	case map[string]any:
		n := intValue(x["value"])
		unit, _ := x["unit"].(string)
		return durationFromUnit(n, strings.ToLower(unit))
	case int, int64, float64, json.Number:
		return durationFromUnit(intValue(v), "days")
	case string:
		return parseDurationString(x)
	}
	return 0
}

func durationFromUnit(n int, unit string) time.Duration {
	if n <= 0 {
		return 0
	}
	switch unit {
	case "hour", "hours", "h":
		return time.Duration(n) * time.Hour
	case "day", "days", "d", "":
		return time.Duration(n) * 24 * time.Hour
	case "week", "weeks", "w":
		return time.Duration(n) * 7 * 24 * time.Hour
	case "month", "months":
		return time.Duration(n) * 30 * 24 * time.Hour
	case "year", "years", "y":
		return time.Duration(n) * 365 * 24 * time.Hour
	}
	return 0
}

func parseDurationString(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	last := s[len(s)-1:]
	num := s[:len(s)-1]
	n := 0
	for _, r := range num {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return durationFromUnit(n, last)
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func authorNamesFromRefs(refs []backend.AuthorRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out
}

func seriesNamesFromRefs(refs []backend.SeriesRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out
}

// -------- Sort helpers --------

func sortBy(items []Candidate, descending bool, key func(Candidate) string) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := key(items[i]), key(items[j])
		if descending {
			return a > b
		}
		return a < b
	})
}

func sortByInt(items []Candidate, descending bool, key func(Candidate) int) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := key(items[i]), key(items[j])
		if descending {
			return a > b
		}
		return a < b
	})
}

func sortByInt64(items []Candidate, descending bool, key func(Candidate) int64) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := key(items[i]), key(items[j])
		if descending {
			return a > b
		}
		return a < b
	})
}

func sortByFloat(items []Candidate, descending bool, key func(Candidate) float64) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := key(items[i]), key(items[j])
		if descending {
			return a > b
		}
		return a < b
	})
}

// shuffleSeeded deterministically shuffles items using a hash of seed.
// Same seed → same order across multiple calls; useful for stable
// pagination over the random-sorted view.
func shuffleSeeded(items []Candidate, seed string) {
	if seed == "" {
		seed = "random"
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	r := rand.New(rand.NewSource(int64(h.Sum64()))) //nolint:gosec // ordering hint, not crypto
	r.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
}
