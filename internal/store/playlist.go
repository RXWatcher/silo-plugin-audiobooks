package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Playlist mirrors the playlist row shape. Description + CoverItem
// are optional; visible to others when is_public.
type Playlist struct {
	ID          string
	UserID      string
	ProfileID   string
	Name        string
	Description string
	CoverItem   string
	IsPublic    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PlaylistItem is one ordered entry in a playlist. EpisodeID is
// the empty string for book entries; non-empty for podcast-episode
// entries. Position monotonically increases within a playlist; the
// store reorders on remove to keep the index dense.
type PlaylistItem struct {
	PlaylistID    string
	LibraryItemID string
	EpisodeID     string
	Position      int
	AddedAt       time.Time
}

func (s *Store) CreatePlaylist(ctx context.Context, p Playlist) error {
	if p.ID == "" || p.UserID == "" || p.Name == "" {
		return errors.New("id, user_id, name required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO playlist (id, user_id, profile_id, name, description, cover_item, is_public)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), $7)
	`, p.ID, p.UserID, p.ProfileID, p.Name, p.Description, p.CoverItem, p.IsPublic)
	if err != nil {
		return fmt.Errorf("create playlist: %w", err)
	}
	return nil
}

func (s *Store) UpdatePlaylist(ctx context.Context, p Playlist, ownerID string, profileID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE playlist SET
			name        = $1,
			description = NULLIF($2,''),
			cover_item  = NULLIF($3,''),
			is_public   = $4,
			updated_at  = now()
		WHERE id = $5 AND user_id = $6 AND profile_id = $7
	`, p.Name, p.Description, p.CoverItem, p.IsPublic, p.ID, ownerID, profileID)
	if err != nil {
		return fmt.Errorf("update playlist: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeletePlaylist(ctx context.Context, id, ownerID string, profileID string) error {
	if id == "" || ownerID == "" {
		return errors.New("id, owner required")
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM playlist WHERE id = $1 AND user_id = $2 AND profile_id = $3
	`, id, ownerID, profileID)
	if err != nil {
		return fmt.Errorf("delete playlist: %w", err)
	}
	return nil
}

func (s *Store) GetPlaylist(ctx context.Context, id string) (Playlist, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, COALESCE(description,''), COALESCE(cover_item,''),
		       is_public, created_at, updated_at
		FROM playlist WHERE id = $1
	`, id)
	var p Playlist
	if err := row.Scan(&p.ID, &p.UserID, &p.ProfileID, &p.Name, &p.Description, &p.CoverItem,
		&p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Playlist{}, ErrNotFound
		}
		return Playlist{}, fmt.Errorf("get playlist: %w", err)
	}
	return p, nil
}

// ListUserPlaylists returns the user's own playlists for the given profile.
// Public playlists from other users are NOT included here — they have
// their own dedicated route (matches the manual-collections
// surface).
func (s *Store) ListUserPlaylists(ctx context.Context, userID string, profileID string) ([]Playlist, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, COALESCE(description,''), COALESCE(cover_item,''),
		       is_public, created_at, updated_at
		FROM playlist WHERE user_id = $1 AND profile_id = $2
		ORDER BY LOWER(name)
	`, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list playlists: %w", err)
	}
	defer rows.Close()
	var out []Playlist
	for rows.Next() {
		var p Playlist
		if err := rows.Scan(&p.ID, &p.UserID, &p.ProfileID, &p.Name, &p.Description, &p.CoverItem,
			&p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan playlist: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddPlaylistItem appends one item at the next position. owner
// pins the playlist ownership so a caller can't add to someone
// else's playlist.
func (s *Store) AddPlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID, ownerID string, profileID string) error {
	if playlistID == "" || libraryItemID == "" {
		return errors.New("playlist_id, library_item_id required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	var ownerCheck, profileCheck string
	if err := tx.QueryRow(ctx,
		`SELECT user_id, profile_id FROM playlist WHERE id = $1`, playlistID).Scan(&ownerCheck, &profileCheck); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("owner check: %w", err)
	}
	if ownerCheck != ownerID || profileCheck != profileID {
		return errors.New("not owned by caller")
	}
	var nextPos int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(position), -1) + 1 FROM playlist_item WHERE playlist_id = $1`,
		playlistID).Scan(&nextPos); err != nil {
		return fmt.Errorf("next pos: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO playlist_item (playlist_id, library_item_id, episode_id, position)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (playlist_id, library_item_id, episode_id) DO NOTHING
	`, playlistID, libraryItemID, episodeID, nextPos); err != nil {
		return fmt.Errorf("insert item: %w", err)
	}
	return tx.Commit(ctx)
}

// RemovePlaylistItem deletes one (playlist, item, episode) row.
// Idempotent; doesn't re-pack positions (the read-side ORDER BY
// position keeps the order stable, and gaps are harmless).
func (s *Store) RemovePlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID, ownerID string, profileID string) error {
	if playlistID == "" || libraryItemID == "" {
		return errors.New("playlist_id, library_item_id required")
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM playlist_item
		WHERE playlist_id = $1 AND library_item_id = $2 AND episode_id = $3
		  AND EXISTS (SELECT 1 FROM playlist WHERE id = $1 AND user_id = $4 AND profile_id = $5)
	`, playlistID, libraryItemID, episodeID, ownerID, profileID)
	if err != nil {
		return fmt.Errorf("delete playlist_item: %w", err)
	}
	return nil
}

func (s *Store) ListPlaylistItems(ctx context.Context, playlistID, viewerID string, profileID string) ([]PlaylistItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pi.playlist_id, pi.library_item_id, pi.episode_id, pi.position, pi.added_at
		FROM playlist_item pi
		JOIN playlist p ON p.id = pi.playlist_id
		WHERE pi.playlist_id = $1 AND ((p.user_id = $2 AND p.profile_id = $3) OR p.is_public = TRUE)
		ORDER BY pi.position
	`, playlistID, viewerID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list playlist_items: %w", err)
	}
	defer rows.Close()
	var out []PlaylistItem
	for rows.Next() {
		var pi PlaylistItem
		if err := rows.Scan(&pi.PlaylistID, &pi.LibraryItemID, &pi.EpisodeID,
			&pi.Position, &pi.AddedAt); err != nil {
			return nil, fmt.Errorf("scan playlist_item: %w", err)
		}
		out = append(out, pi)
	}
	return out, rows.Err()
}
