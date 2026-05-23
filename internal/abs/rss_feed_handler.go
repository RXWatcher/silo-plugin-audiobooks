package abs

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/bookref"
	mediatoken "github.com/RXWatcher/silo-plugin-audiobooks/internal/mediatoken"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
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
	r.Get("/feed/{slug}/track/{ref}/{idx}", h.handlePublicFeedTrack)
}

// handlePublicFeedTrack serves the audio enclosure for an RSS feed
// episode. The feed slug is the capability (podcast apps don't speak
// custom auth); the {ref} path segment is the encoded bookref of the
// specific book. It mirrors handlePublicTrack's byte-proxy.
func (h *Handler) handlePublicFeedTrack(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	ref := chi.URLParam(r, "ref")
	idxRaw := chi.URLParam(r, "idx")
	// The enclosure URL carries a file extension (e.g. "0.mp3"); strip it.
	if dot := strings.IndexByte(idxRaw, '.'); dot >= 0 {
		idxRaw = idxRaw[:dot]
	}
	idx, err := strconv.Atoi(idxRaw)
	if err != nil || idx < 0 {
		http.Error(w, "idx must be a non-negative int", http.StatusBadRequest)
		return
	}

	feed, err := h.store.GetRSSFeedBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Verify the requested book belongs to the feed so a leaked slug
	// cannot be used to walk the owner's whole library:
	//   - item feed: ref must be the feed's single book.
	//   - collection feed: ref must be a member of the collection.
	//   - series feed: membership is a catalog scan too expensive to run
	//     per byte-range request; the unguessable slug is the gate.
	switch feed.EntityType {
	case "item":
		if ref != feed.EntityID {
			http.Error(w, "track not part of this feed", http.StatusNotFound)
			return
		}
	case "collection":
		// TODO(profiles): RSS feeds are not profile-scoped
		items, err := h.store.ListCollectionItems(r.Context(), feed.EntityID, feed.UserID, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		inFeed := false
		for _, it := range items {
			if it.BookID == ref {
				inFeed = true
				break
			}
		}
		if !inFeed {
			http.Error(w, "track not part of this feed", http.StatusNotFound)
			return
		}
	}

	lib, backendBookID, _, err := h.portalLibraryForBookRef(r.Context(), ref)
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}

	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	if cfg.MediaSigningSecret == "" {
		http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
		return
	}
	mediaTok, err := mediatoken.Mint(cfg.MediaSigningSecret, feed.UserID, backendBookID, idx)
	if err != nil {
		http.Error(w, "mint media token", http.StatusInternalServerError)
		return
	}
	backendPath := "/api/v1/stream/" + neturl.PathEscape(backendBookID) + "/" + strconv.Itoa(idx) +
		"?token=" + neturl.QueryEscape(mediaTok)

	hdrs := map[string]string{}
	for _, name := range []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(name); v != "" {
			hdrs[name] = v
		}
	}

	resp, err := h.backend.HostClient().GetStream(r.Context(), "", lib.BackendPluginID, backendPath, hdrs)
	if err != nil {
		h.logger.Warn("abs feed track proxy: upstream error",
			"slug", slug, "book_id", backendBookID, "file_idx", idx, "err", err.Error())
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, name := range []string{
		"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"ETag", "Last-Modified", "Cache-Control",
	} {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		h.logger.Debug("abs feed track proxy: copy ended",
			"slug", slug, "file_idx", idx, "err", err.Error())
	}
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
		title = "Silo feed"
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
			Author:   "Silo",
			Image:    itunesImage{Href: absoluteCover(base, r.Host, feed.CoverPath)},
			Category: itunesCategory{Text: "Audiobooks"},
			Explicit: "no",
		},
	}

	switch feed.EntityType {
	case "item":
		channel.Items = h.itemFeedEpisodes(r, feed.EntityID, base, r.Host)
	case "collection":
		channel.Items = h.collectionFeedEpisodes(r, feed.UserID, feed.EntityID, base, r.Host, feed.Slug)
	case "series":
		channel.Items = h.seriesFeedEpisodes(r, feed.EntityID, base, r.Host, feed.Slug)
	}

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
			"/track/" + neturl.PathEscape(encoded) + "/" + strconv.Itoa(i) + "." + strings.ToLower(strings.TrimPrefix(f.Format, "."))
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

// collectionFeedEpisodes renders one episode per book in the
// owner's manual collection. Each book becomes one episode using
// its FIRST track as the enclosure — a podcast app subscribing to
// a collection feed gets a queue of audiobook samples, which is
// the standard "follow my reading list" UX.
func (h *Handler) collectionFeedEpisodes(r *http.Request, ownerID, collectionID, scheme, host, slug string) []rssItem {
	// TODO(profiles): RSS feeds are not profile-scoped
	items, err := h.store.ListCollectionItems(r.Context(), collectionID, ownerID, "")
	if err != nil {
		return nil
	}
	out := make([]rssItem, 0, len(items))
	for _, it := range items {
		ep := h.singleBookEpisode(r, it.BookID, scheme, host, slug)
		if ep != nil {
			out = append(out, *ep)
		}
	}
	return out
}

// seriesFeedEpisodes renders one episode per book in the series.
// The backend's BrowseSeries endpoint surfaces books-in-series;
// we walk it + emit each as a single-track episode. Books that
// 404 are silently skipped.
func (h *Handler) seriesFeedEpisodes(r *http.Request, seriesID, scheme, host, slug string) []rssItem {
	// Series feeds are typically owner-scoped to one library; we
	// scan the owner's libraries to find one whose backend knows
	// this series. Best-effort — most deployments have a single
	// audiobook library.
	libs := h.portalLibraries(r.Context(), true)
	for _, lib := range libs {
		if lib.BackendPluginID == "" {
			continue
		}
		out, err := h.backend.ListCatalog(r.Context(), "", lib.BackendPluginID, backend.ListParams{
			Limit:     200,
			LibraryID: backendLibraryID(lib),
		})
		if err != nil {
			continue
		}
		// Filter for books in this series. The summary's SeriesRefs
		// is the v1.1 shape; flat Series is the legacy shape.
		matches := make([]string, 0, 8)
		for _, s := range out.Items {
			if seriesMatches(s, seriesID) {
				matches = append(matches, bookref.Encode(lib.ID, s.ID))
			}
		}
		if len(matches) == 0 {
			continue
		}
		feedItems := make([]rssItem, 0, len(matches))
		for _, encoded := range matches {
			ep := h.singleBookEpisode(r, encoded, scheme, host, slug)
			if ep != nil {
				feedItems = append(feedItems, *ep)
			}
		}
		return feedItems
	}
	return nil
}

// seriesMatches returns true when the summary lists this series.
// The v1.1 backend contract exposes only SeriesRefs on the
// summary; legacy callers carrying a flat series string need to
// pass it as the series id and we'll match on ref.Name.
func seriesMatches(s backend.AudiobookSummary, seriesID string) bool {
	for _, ref := range s.SeriesRefs {
		if ref.ID == seriesID || ref.Name == seriesID {
			return true
		}
	}
	return false
}

// singleBookEpisode emits one rssItem for an entire audiobook.
// First track of the book is the enclosure; title + author come
// from the backend GetDetail. Returns nil when the book can't be
// resolved.
func (h *Handler) singleBookEpisode(r *http.Request, encoded, scheme, host, slug string) *rssItem {
	libID, backendBookID, ok := bookref.Decode(encoded)
	if !ok || libID == 0 {
		return nil
	}
	lib, err := h.store.GetPortalLibrary(r.Context(), libID)
	if err != nil || lib.BackendPluginID == "" {
		return nil
	}
	detail, err := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID)
	if err != nil || len(detail.Files) == 0 {
		return nil
	}
	f := detail.Files[0]
	trackURL := scheme + "://" + host + "/feed/" + slug +
		"/track/" + neturl.PathEscape(encoded) + "/" + strconv.Itoa(0) + "." + strings.ToLower(strings.TrimPrefix(f.Format, "."))
	return &rssItem{
		Title:       detail.Title,
		Description: detail.Description,
		PubDate:     time.Now().UTC().Format(time.RFC1123Z),
		GUID:        rssGUID{Value: encoded, IsPermalink: "false"},
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
	}
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
