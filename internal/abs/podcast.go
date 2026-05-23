package abs

import (
	"strconv"
	"strings"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// PodcastItem is the ABS-shaped representation of a podcast (mediaType=
// "podcast"). The shape mirrors LibraryItem but media.metadata + media
// look different — podcasts carry episodes[] instead of audioFiles[],
// and per-episode contentUrl is built at request time by the play handler.
type PodcastItem struct {
	ID        string              `json:"id"`
	LibraryID string              `json:"libraryId"`
	FolderID  string              `json:"folderId"`
	MediaType string              `json:"mediaType"`
	Media     PodcastItemMedia    `json:"media"`
	AddedAt   int64               `json:"addedAt"`
	UpdatedAt int64               `json:"updatedAt"`
	NumEpisodes int               `json:"numEpisodes"`
}

// PodcastItemMedia holds the podcast-level metadata and episode list.
type PodcastItemMedia struct {
	Metadata  PodcastMetadata    `json:"metadata"`
	CoverPath string             `json:"coverPath"`
	Episodes  []PodcastEpisodeABS `json:"episodes"`
}

// PodcastMetadata mirrors the ABS podcast metadata block. The id-bearing
// title field is required; everything else is best-effort and is emitted
// only when the source actually carries it.
type PodcastMetadata struct {
	Title          string `json:"title"`
	Author         string `json:"author,omitempty"`
	Description    string `json:"description,omitempty"`
	Language       string `json:"language,omitempty"`
	Explicit       bool   `json:"explicit"`
	ITunesCategory string `json:"itunesCategory,omitempty"`
	FeedURL        string `json:"feedUrl,omitempty"`
}

// PodcastEpisodeABS is one playable episode inside PodcastItemMedia. The
// id is the portal-issued episode id (already "pe_..." prefixed); the
// audioFile + chapters fields stay nil because podcast episodes are
// single-file with no internal markers in 99% of feeds.
//
// ContentURL is filled in by the play handler at session-start time. For
// item-detail responses we leave it blank — clients call /play to get a
// signed, session-scoped URL.
type PodcastEpisodeABS struct {
	ID            string  `json:"id"`
	LibraryItemID string  `json:"libraryItemId"`
	Index         int     `json:"index"`
	Season        string  `json:"season,omitempty"`
	Episode       string  `json:"episode,omitempty"`
	Title         string  `json:"title"`
	Description   string  `json:"description,omitempty"`
	PublishedAt   int64   `json:"publishedAt,omitempty"`
	Duration      float64 `json:"duration"`
	AudioBytes    int64   `json:"audioBytes,omitempty"`
	MimeType      string  `json:"mimeType,omitempty"`
	CoverPath     string  `json:"coverPath,omitempty"`
	// ContentURL is set on POST /play responses (not on item-detail).
	ContentURL string `json:"contentUrl,omitempty"`
}

// ToPodcastItem translates a store.Podcast + its episodes into the ABS
// shape. The encoded podcast id is the bookref-prefixed string clients
// send back on subsequent /items/{id}/play calls; episode ids carry the
// "pe_" prefix so the handler can distinguish them from audiobook ids
// when dispatching /me/progress.
func ToPodcastItem(p store.Podcast, episodes []store.PodcastEpisode, encodedID string) PodcastItem {
	abs := PodcastItem{
		ID:        encodedID,
		LibraryID: libraryIDString(p.LibraryID),
		FolderID:  VirtualFolderID,
		MediaType: "podcast",
		Media: PodcastItemMedia{
			Metadata: PodcastMetadata{
				Title:          p.Title,
				Author:         p.Author,
				Description:    p.Description,
				Language:       p.Language,
				Explicit:       p.Explicit,
				ITunesCategory: p.ITunesCategory,
				FeedURL:        p.FeedURL,
			},
			CoverPath: p.CoverURL,
		},
		AddedAt:     p.CreatedAt.UnixMilli(),
		UpdatedAt:   p.UpdatedAt.UnixMilli(),
		NumEpisodes: len(episodes),
	}
	abs.Media.Episodes = make([]PodcastEpisodeABS, 0, len(episodes))
	for i, e := range episodes {
		ep := PodcastEpisodeABS{
			ID:            EncodePodcastEpisodeID(e.ID),
			LibraryItemID: encodedID,
			Index:         i,
			Title:         e.Title,
			Description:   e.Description,
			Duration:      float64(e.DurationSeconds),
			AudioBytes:    e.AudioBytes,
			MimeType:      e.AudioMimeType,
			CoverPath:     e.CoverURL,
		}
		if e.PublishedAt != nil {
			ep.PublishedAt = e.PublishedAt.UnixMilli()
		}
		if e.EpisodeIndex != nil {
			ep.Episode = strconv.Itoa(*e.EpisodeIndex)
		}
		if e.SeasonIndex != nil {
			ep.Season = strconv.Itoa(*e.SeasonIndex)
		}
		abs.Media.Episodes = append(abs.Media.Episodes, ep)
	}
	return abs
}

// ToPodcastSummary is the catalog-list shape: same as item-detail but
// with no episodes[]. ABS clients display NumEpisodes + cover in shelf
// rows and fetch the full detail on tap-in.
func ToPodcastSummary(p store.Podcast, numEpisodes int, encodedID string) PodcastItem {
	item := ToPodcastItem(p, nil, encodedID)
	item.NumEpisodes = numEpisodes
	return item
}

// EncodePodcastEpisodeID and DecodePodcastEpisodeID handle the "pe_"
// prefix on episode ids. The prefix lets the ABS /me/progress handler
// dispatch to podcast vs audiobook progress without an extra DB lookup.
// Stored episode ids are bare; the prefix exists only on the wire.

const podcastEpisodePrefix = "pe_"

func EncodePodcastEpisodeID(rawID string) string {
	if rawID == "" {
		return ""
	}
	return podcastEpisodePrefix + rawID
}

// DecodePodcastEpisodeID strips the prefix and reports whether the input
// looked like an episode id. A bare id with no prefix returns (raw, false)
// so callers can fall through to the audiobook progress path.
func DecodePodcastEpisodeID(in string) (string, bool) {
	if !strings.HasPrefix(in, podcastEpisodePrefix) {
		return in, false
	}
	return strings.TrimPrefix(in, podcastEpisodePrefix), true
}
