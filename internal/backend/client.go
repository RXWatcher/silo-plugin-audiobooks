package backend

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// Client is a typed wrapper over HostClient that knows how to call the
// audiobook_backend.v1 contract endpoints. installID and bearer come from the
// caller per request.
type Client struct {
	host *HostClient
}

// NewClient wires up a typed client over a HostClient.
func NewClient(host *HostClient) *Client { return &Client{host: host} }

// ListParams mirrors the query shape for /catalog endpoints.
type ListParams struct {
	Cursor    string
	Limit     int
	Sort      string
	Order     string
	Query     string
	LibraryID int64
}

func (p ListParams) toQuery() string {
	q := url.Values{}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Sort != "" {
		q.Set("sort", p.Sort)
	}
	if p.Order != "" {
		q.Set("order", p.Order)
	}
	if p.Query != "" {
		q.Set("q", p.Query)
	}
	if p.LibraryID > 0 {
		q.Set("library_id", strconv.FormatInt(p.LibraryID, 10))
	}
	enc := q.Encode()
	if enc == "" {
		return ""
	}
	return "?" + enc
}

// ListLibraries calls GET /api/v1/catalog/libraries on backends that expose
// sub-library metadata. Older backends may 404; callers treat that as empty.
func (c *Client) ListLibraries(ctx context.Context, bearer, installID string) ([]LibraryInfo, error) {
	var out struct {
		Items []LibraryInfo `json:"items"`
	}
	if err := c.host.GetJSON(ctx, bearer, installID, "/api/v1/catalog/libraries", &out); err != nil {
		return nil, fmt.Errorf("decode libraries: %w", err)
	}
	return out.Items, nil
}

// ListCatalog calls GET /api/v1/catalog on the backend.
func (c *Client) ListCatalog(ctx context.Context, bearer, installID string, p ListParams) (PageEnvelope[AudiobookSummary], error) {
	path := "/api/v1/catalog" + p.toQuery()
	if p.Query != "" {
		path = "/api/v1/catalog/search" + p.toQuery()
	}
	var out PageEnvelope[AudiobookSummary]
	if err := c.host.GetJSON(ctx, bearer, installID, path, &out); err != nil {
		return PageEnvelope[AudiobookSummary]{}, fmt.Errorf("decode catalog: %w", err)
	}
	return out, nil
}

// GetDetail calls GET /api/v1/catalog/{id}.
func (c *Client) GetDetail(ctx context.Context, bearer, installID, id string) (AudiobookDetail, error) {
	var out AudiobookDetail
	if err := c.host.GetJSON(ctx, bearer, installID, "/api/v1/catalog/"+url.PathEscape(id), &out); err != nil {
		return AudiobookDetail{}, fmt.Errorf("decode detail: %w", err)
	}
	return out, nil
}

// BrowseAuthors calls GET /api/v1/browse/authors.
func (c *Client) BrowseAuthors(ctx context.Context, bearer, installID string, p ListParams) (PageEnvelope[AuthorSummary], error) {
	var out PageEnvelope[AuthorSummary]
	if err := c.host.GetJSON(ctx, bearer, installID, "/api/v1/browse/authors"+p.toQuery(), &out); err != nil {
		return PageEnvelope[AuthorSummary]{}, err
	}
	return out, nil
}

// BrowseSeries calls GET /api/v1/browse/series.
func (c *Client) BrowseSeries(ctx context.Context, bearer, installID string, p ListParams) (PageEnvelope[SeriesSummary], error) {
	var out PageEnvelope[SeriesSummary]
	if err := c.host.GetJSON(ctx, bearer, installID, "/api/v1/browse/series"+p.toQuery(), &out); err != nil {
		return PageEnvelope[SeriesSummary]{}, err
	}
	return out, nil
}

// BrowseNarrators calls GET /api/v1/browse/narrators.
func (c *Client) BrowseNarrators(ctx context.Context, bearer, installID string, p ListParams) (PageEnvelope[NarratorSummary], error) {
	var out PageEnvelope[NarratorSummary]
	if err := c.host.GetJSON(ctx, bearer, installID, "/api/v1/browse/narrators"+p.toQuery(), &out); err != nil {
		return PageEnvelope[NarratorSummary]{}, err
	}
	return out, nil
}

// CoverURL returns the URL clients hit for a book cover. The portal can
// either redirect the SPA to this URL or proxy bytes.
func (c *Client) CoverURL(installID, bookID, size string) string {
	if size == "" {
		size = "large"
	}
	return c.host.PluginURL(installID, fmt.Sprintf("/api/v1/cover/%s/%s", bookID, size))
}

// StreamURL returns the public URL for a stream redirect.
func (c *Client) StreamURL(installID, bookID string, fileIdx int) string {
	return c.host.PluginURL(installID, fmt.Sprintf("/api/v1/stream/%s/%d", bookID, fileIdx))
}

// GetRequestSnapshot calls GET /api/v1/requests/{external_id}.
func (c *Client) GetRequestSnapshot(ctx context.Context, bearer, installID, externalID string) (RequestSnapshot, error) {
	var out RequestSnapshot
	if err := c.host.GetJSON(ctx, bearer, installID, "/api/v1/requests/"+url.PathEscape(externalID), &out); err != nil {
		return RequestSnapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	return out, nil
}

// HostClient exposes the underlying host client for streaming pass-through.
func (c *Client) HostClient() *HostClient { return c.host }
