// Package libsync mirrors a backend's libraries into portal presentation
// shelves. Reconcile is pure (the single destructive decision); Sync wraps
// it behind a guard that refuses to run on a failed/empty backend fetch.
package libsync

import (
	"context"
	"errors"
	"fmt"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// SyncStats summarizes a reconcile pass.
type SyncStats struct {
	Created int
	Updated int
	Pruned  int
	Kept    int
}

// Reconcile computes the full desired portal_library set for backendID.
//
// Sync-managed = rows with BackendPluginID==backendID AND BackendLibraryID!=nil.
// Non-managed rows (operator-created nil-BackendLibraryID, or other backend)
// pass through untouched. Managed rows are matched to backend libraries by
// *BackendLibraryID == LibraryInfo.ID: matched -> update Name/MediaType only
// (ID/Enabled/SortOrder preserved); unmatched backend lib -> create; managed
// row with no backend lib -> prune (omitted). Pure & deterministic.
// Assumes at most one managed row per backend library; a duplicate (only
// possible via manual DB edits) is counted as Pruned.
func Reconcile(existing []store.PortalLibrary, backendLibs []backend.LibraryInfo, backendID string) ([]store.PortalLibrary, SyncStats) {
	var out []store.PortalLibrary
	var st SyncStats

	managed := make(map[int64]store.PortalLibrary)
	maxSort := -1
	for _, e := range existing {
		if e.SortOrder > maxSort {
			maxSort = e.SortOrder
		}
		if e.BackendPluginID == backendID && e.BackendLibraryID != nil {
			if _, dup := managed[*e.BackendLibraryID]; dup {
				st.Pruned++
			}
			managed[*e.BackendLibraryID] = e
		} else {
			out = append(out, e)
			st.Kept++
		}
	}

	seen := make(map[int64]bool, len(backendLibs))
	for _, bl := range backendLibs {
		seen[bl.ID] = true
		name := bl.Name
		if name == "" {
			name = fmt.Sprintf("Library %d", bl.ID)
		}
		mt := bl.MediaType
		if mt == "" {
			mt = "audiobook"
		}
		if cur, ok := managed[bl.ID]; ok {
			changed := cur.Name != name || cur.MediaType != mt
			cur.Name = name
			cur.MediaType = mt
			out = append(out, cur)
			if changed {
				st.Updated++
			} else {
				st.Kept++
			}
			continue
		}
		maxSort++
		idCopy := bl.ID
		out = append(out, store.PortalLibrary{
			ID:               0,
			Name:             name,
			MediaType:        mt,
			BackendPluginID:  backendID,
			BackendLibraryID: &idCopy,
			Enabled:          true,
			SortOrder:        maxSort,
		})
		st.Created++
	}

	for blID := range managed {
		if !seen[blID] {
			st.Pruned++
		}
	}
	return out, st
}

// LibStore is the store surface Sync needs (satisfied by *store.Store).
type LibStore interface {
	ListPortalLibraries(ctx context.Context, enabledOnly bool) ([]store.PortalLibrary, error)
	ReplacePortalLibraries(ctx context.Context, libs []store.PortalLibrary) error
}

// BackendLister fetches the backend's libraries (satisfied by *backend.Client).
type BackendLister interface {
	ListLibraries(ctx context.Context, bearer, installID string) ([]backend.LibraryInfo, error)
}

// Sync fetches backendID's libraries and reconciles them into portal
// shelves. THE GUARD: a fetch error OR zero libraries aborts with an error
// and ZERO store writes, so a briefly-unavailable/empty backend can never
// mass-prune operator config.
func Sync(ctx context.Context, st LibStore, lister BackendLister, bearer, backendID string) (SyncStats, error) {
	if backendID == "" {
		return SyncStats{}, errors.New("no backend configured")
	}
	libs, err := lister.ListLibraries(ctx, bearer, backendID)
	if err != nil {
		return SyncStats{}, fmt.Errorf("fetch backend libraries: %w", err)
	}
	if len(libs) == 0 {
		return SyncStats{}, errors.New("refusing to sync: backend returned no libraries")
	}
	existing, err := st.ListPortalLibraries(ctx, false)
	if err != nil {
		return SyncStats{}, fmt.Errorf("list portal libraries: %w", err)
	}
	desired, stats := Reconcile(existing, libs, backendID)
	if err := st.ReplacePortalLibraries(ctx, desired); err != nil {
		return SyncStats{}, fmt.Errorf("replace portal libraries: %w", err)
	}
	return stats, nil
}
