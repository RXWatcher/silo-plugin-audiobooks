package abs

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookref"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// RSS feed publishing — listeners can expose a library item / series
// / collection as a podcast-style RSS feed, then subscribe to that
// feed from any podcast app. The feed is gated by an unguessable
// random slug rather than a Bearer token (podcast apps don't speak
// custom auth); rotate by closing + reopening.
//
// Routes follow the upstream ABS naming:
//   GET    /api/feeds                          — list user's feeds
//   POST   /api/feeds/item/{itemId}/open       — open feed for item
//   POST   /api/feeds/series/{seriesId}/open
//   POST   /api/feeds/collection/{collId}/open
//   POST   /api/feeds/{id}/close
//   GET    /feed/{slug}.xml                    — public RSS document

func (h *Handler) mountRSSFeedRoutes(prefix string, r chi.Router) {
	r.Get(prefix+"/feeds", h.handleListRSSFeeds)
	r.Post(prefix+"/feeds/item/{itemId}/open", h.handleOpenItemFeed)
	r.Post(prefix+"/feeds/series/{seriesId}/open", h.handleOpenSeriesFeed)
	r.Post(prefix+"/feeds/collection/{collId}/open", h.handleOpenCollectionFeed)
	r.Post(prefix+"/feeds/{id}/close", h.handleCloseFeed)
}

// MountPublicFeed registers the unauthenticated /feed/{slug}.xml
// route. Mount this OUTSIDE the bearer-auth group — podcast clients
// reach this URL with no auth at all.
func (h *Handler) MountPublicFeed(r chi.Router) {
	r.Get("/feed/{slug}.xml", h.handlePublicFeed)
	r.Get("/feed/{slug}", h.handlePublicFeed)
}

func (h *Handler) handleListRSSFeeds(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	rows, err := h.store.ListRSSFeedsForUser(r.Context(), a.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	full := make([]map[string]any, 0, len(rows))
	mini := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		full = append(full, h.rssFeedToMap(f, r))
		mini = append(mini, map[string]any{
			"id":   f.ID,
			"slug": f.Slug,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": full, "minified": mini})
}

func (h *Handler) handleOpenItemFeed(w http.ResponseWriter, r *http.Request) {
	h.openFeed(w, r, "item", chi.URLParam(r, "itemId"))
}
func (h *Handler) handleOpenSeriesFeed(w http.ResponseWriter, r *http.Request) {
	h.openFeed(w, r, "series", chi.URLParam(r, "seriesId"))
}
func (h *Handler) handleOpenCollectionFeed(w http.ResponseWriter, r *http.Request) {
	h.openFeed(w, r, "collection", chi.URLParam(r, "collId"))
}

// openFeed is idempotent — re-opening the same (entity_type,
// entity_id) returns the existing slug rather than minting a new
// one. The body carries an optional {title, description} override
// for the feed-level metadata; the entity's own title is the
// default.
func (h *Handler) openFeed(w http.ResponseWriter, r *http.Request, entityType, entityID string) {
	a, _ := absAuthFrom(r)
	if entityID == "" {
		http.Error(w, "entity id required", http.StatusBadRequest)
		return
	}
	if existing, err := h.store.GetRSSFeedForEntity(r.Context(), entityType, entityID, a.UserID); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"feed":     h.rssFeedToMap(existing, r),
			"reopened": true,
		})
		return
	}
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		CoverPath   string `json:"coverPath"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body optional

	title := body.Title
	coverPath := body.CoverPath
	if title == "" {
		// Try to populate the title from the entity. For items we
		// can call GetDetail; for series + collection it'd require
		// the parent metadata. Best-effort — fall back to a
		// generic title.
		title = h.lookupFeedTitle(r, entityType, entityID)
	}
	if title == "" {
		title = "Continuum feed"
	}

	slug, err := mintFeedSlug()
	if err != nil {
		http.Error(w, "slug mint: "+err.Error(), http.StatusInternalServerError)
		return
	}
	feed := store.RSSFeed{
		ID:          ulid.Make().String(),
		UserID:      a.UserID,
		Slug:        slug,
		EntityType:  entityType,
		EntityID:    entityID,
		Title:       title,
		Description: body.Description,
		CoverPath:   coverPath,
	}
	if err := h.store.UpsertRSSFeed(r.Context(), feed); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "rss_feed_open", map[string]any{"id": feed.ID, "slug": feed.Slug})
	writeJSON(w, http.StatusCreated, map[string]any{"feed": h.rssFeedToMap(feed, r)})
}

func (h *Handler) handleCloseFeed(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	id := chi.URLParam(r, "id")
	if err := h.store.DeleteRSSFeed(r.Context(), id, a.UserID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "rss_feed_closed", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

// handlePublicFeed renders the RSS XML for a slug. No auth — the
// slug is the capability. Only handles 'item' entity for now; series
// + collection fall back to a minimal channel with the entity
// metadata (proper episode-per-book rendering is a follow-up).
func (h *Handler) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	feed, err := h.store.GetRSSFeedBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	base := r.URL.Scheme
	if base == "" {
		base = "http"
		if r.TLS != nil {
			base = "https"
		}
	}
	feedURL := base + "://" + r.Host + "/feed/" + feed.Slug + ".xml"
	channel := rssChannel{
		Title:       feed.Title,
		Description: feed.Description,
		Link:        feedURL,
		Language:    "en-us",
		Image:       rssImage{URL: absoluteCover(base, r.Host, feed.CoverPath), Title: feed.Title, Link: feedURL},
		ITunes: itunesChannel{
			Author:   "Continuum",
			Image:    itunesImage{Href: absoluteCover(base, r.Host, feed.CoverPath)},
			Category: itunesCategory{Text: "Audiobooks"},
			Explicit: "no",
		},
	}

	if feed.EntityType == "item" {
		episodes := h.itemFeedEpisodes(r, feed.EntityID, base, r.Host)
		channel.Items = episodes
	}
	// series + collection feeds emit just the channel header for
	// now — a podcast client subscribing sees the title + cover but
	// no episodes until a follow-up commits the per-book episode
	// rendering.

	doc := rssDocument{
		XMLNSITunes: "http://www.itunes.com/dtds/podcast-1.0.dtd",
		Version:     "2.0",
		Channel:     channel,
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(doc)
}

// itemFeedEpisodes renders one episode per audiobook track. The
// enclosure URL points at the existing /abs/public/session-style
// track route — we mint a session-less media token so the URL is
// stable for podcast app subscribers (the standard /public/session
// path expects a session id we'd have to invent per subscriber).
func (h *Handler) itemFeedEpisodes(r *http.Request, encoded, scheme, host string) []rssItem {
	libID, backendBookID, ok := bookref.Decode(encoded)
	if !ok || libID == 0 {
		return nil
	}
	lib, err := h.store.GetPortalLibrary(r.Context(), libID)
	if err != nil || lib.BackendPluginID == "" {
		return nil
	}
	detail, err := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID)
	if err != nil {
		return nil
	}
	out := make([]rssItem, 0, len(detail.Files))
	for i, f := range detail.Files {
		// Synthesize a per-track public URL. Real ABS gates these
		// with the feed slug + index; we adopt the same scheme so
		// the URL stays unguessable even when the slug is.
		trackURL := scheme + "://" + host + "/feed/" + chi.URLParam(r, "slug") +
			"/track/" + strconv.Itoa(i) + "." + strings.ToLower(strings.TrimPrefix(f.Format, "."))
		title := detail.Title
		if len(detail.Files) > 1 {
			title = detail.Title + " — Track " + strconv.Itoa(i+1)
		}
		out = append(out, rssItem{
			Title:       title,
			Description: detail.Description,
			PubDate:     time.Now().UTC().Format(time.RFC1123Z),
			GUID:        rssGUID{Value: encoded + "#" + strconv.Itoa(i), IsPermalink: "false"},
			Enclosure: rssEnclosure{
				URL:    trackURL,
				Length: strconv.FormatInt(f.SizeBytes, 10),
				Type:   f.MimeType,
			},
			ITunes: itunesItem{
				Author:   authorRefsCombined(detail.AuthorRefs),
				Duration: strconv.Itoa(int(f.DurationSeconds)),
				Image:    itunesImage{Href: absoluteCover(scheme, host, detail.CoverPath)},
			},
		})
	}
	return out
}

func (h *Handler) lookupFeedTitle(r *http.Request, entityType, entityID string) string {
	if entityType != "item" {
		return ""
	}
	libID, backendBookID, ok := bookref.Decode(entityID)
	if !ok || libID == 0 {
		return ""
	}
	lib, err := h.store.GetPortalLibrary(r.Context(), libID)
	if err != nil || lib.BackendPluginID == "" {
		return ""
	}
	d, err := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID)
	if err != nil {
		return ""
	}
	return d.Title
}

func (h *Handler) rssFeedToMap(f store.RSSFeed, r *http.Request) map[string]any {
	base := "http"
	if r != nil && r.TLS != nil {
		base = "https"
	}
	host := ""
	if r != nil {
		host = r.Host
	}
	feedURL := base + "://" + host + "/feed/" + f.Slug + ".xml"
	return map[string]any{
		"id":          f.ID,
		"userId":      f.UserID,
		"slug":        f.Slug,
		"entityType":  f.EntityType,
		"entityId":    f.EntityID,
		"title":       f.Title,
		"description": f.Description,
		"coverPath":   f.CoverPath,
		"feedUrl":     feedURL,
		"createdAt":   f.CreatedAt.UnixMilli(),
	}
}

// mintFeedSlug returns a 22-char URL-safe random token (16 bytes
// of entropy). Unguessable by design — the slug is the only
// capability gating the feed.
func mintFeedSlug() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func absoluteCover(scheme, host, coverPath string) string {
	if coverPath == "" {
		return ""
	}
	if len(coverPath) > 4 && (coverPath[:5] == "http:" || coverPath[:6] == "https:") {
		return coverPath
	}
	if coverPath[0] != '/' {
		coverPath = "/" + coverPath
	}
	return scheme + "://" + host + coverPath
}

// authorRefsCombined joins author names with ", " for itunes:author.
// Empty input → empty string so the field omits cleanly.
func authorRefsCombined(refs []backend.AuthorRef) string {
	if len(refs) == 0 {
		return ""
	}
	names := make([]string, 0, len(refs))
	for _, a := range refs {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	return strings.Join(names, ", ")
}

// ---------- RSS document model ----------

type rssDocument struct {
	XMLName     xml.Name   `xml:"rss"`
	XMLNSITunes string     `xml:"xmlns:itunes,attr"`
	Version     string     `xml:"version,attr"`
	Channel     rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string        `xml:"title"`
	Description string        `xml:"description"`
	Link        string        `xml:"link"`
	Language    string        `xml:"language"`
	Image       rssImage      `xml:"image"`
	ITunes      itunesChannel `xml:"itunes:channel,omitempty"`
	Items       []rssItem     `xml:"item"`
}

type rssImage struct {
	URL   string `xml:"url"`
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

type itunesChannel struct {
	Author   string         `xml:"itunes:author,omitempty"`
	Image    itunesImage    `xml:"itunes:image,omitempty"`
	Category itunesCategory `xml:"itunes:category,omitempty"`
	Explicit string         `xml:"itunes:explicit,omitempty"`
}

type itunesImage struct {
	Href string `xml:"href,attr,omitempty"`
}

type itunesCategory struct {
	Text string `xml:"text,attr,omitempty"`
}

type rssItem struct {
	Title       string       `xml:"title"`
	Description string       `xml:"description"`
	PubDate     string       `xml:"pubDate"`
	GUID        rssGUID      `xml:"guid"`
	Enclosure   rssEnclosure `xml:"enclosure"`
	ITunes      itunesItem   `xml:"itunes:item,omitempty"`
}

type rssGUID struct {
	Value       string `xml:",chardata"`
	IsPermalink string `xml:"isPermaLink,attr,omitempty"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type itunesItem struct {
	Author   string      `xml:"itunes:author,omitempty"`
	Duration string      `xml:"itunes:duration,omitempty"`
	Image    itunesImage `xml:"itunes:image,omitempty"`
}
