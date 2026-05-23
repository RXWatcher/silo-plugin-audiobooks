// Package enrich wraps free metadata providers (OpenLibrary +
// Google Books) used for fleshing out sparse imports. Admin
// "enrich" action calls into here.
//
// Each provider is a narrow client: Search(query) returns a
// candidate list ranked by the provider's own relevance score; the
// caller picks one and the admin UI shows them side by side.
package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Match is the unified shape returned by every provider. Provider-
// specific fields are dropped; if we ever need them, add an
// untyped `extras` map.
type Match struct {
	Provider    string   `json:"provider"`
	ProviderID  string   `json:"provider_id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Narrators   []string `json:"narrators,omitempty"`
	Description string   `json:"description,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	PublishYear int      `json:"publish_year,omitempty"`
	ISBN        string   `json:"isbn,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	CoverURL    string   `json:"cover_url,omitempty"`
	Language    string   `json:"language,omitempty"`
	PageCount   int      `json:"page_count,omitempty"`
}

// HTTPClient is a 30-second-timeout http.Client shared across the
// providers. Reusing one client keeps the connection pool warm
// across batch enrichments.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// SearchOpenLibrary queries openlibrary.org's search.json endpoint.
// `query` is the user's freeform text (title, "title author", or
// ISBN); `limit` caps results at 10. Returns matches sorted by
// OpenLibrary's relevance ordering.
func SearchOpenLibrary(ctx context.Context, query string, limit int) ([]Match, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query required")
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("limit", fmt.Sprintf("%d", limit))
	endpoint := "https://openlibrary.org/search.json?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", "silo-audiobooks/enrich (+https://siloapp.com)")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openlibrary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("openlibrary %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var raw struct {
		Docs []struct {
			Key             string   `json:"key"`
			Title           string   `json:"title"`
			Subtitle        string   `json:"subtitle"`
			AuthorName      []string `json:"author_name"`
			FirstPublish    int      `json:"first_publish_year"`
			ISBN            []string `json:"isbn"`
			Publisher       []string `json:"publisher"`
			Subject         []string `json:"subject"`
			Language        []string `json:"language"`
			CoverEditionKey string   `json:"cover_edition_key"`
			CoverI          int      `json:"cover_i"`
			NumberOfPages   int      `json:"number_of_pages_median"`
		} `json:"docs"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]Match, 0, len(raw.Docs))
	for _, d := range raw.Docs {
		m := Match{
			Provider:    "openlibrary",
			ProviderID:  d.Key,
			Title:       d.Title,
			Subtitle:    d.Subtitle,
			Authors:     d.AuthorName,
			PublishYear: d.FirstPublish,
			Genres:      capStringSlice(d.Subject, 6),
			PageCount:   d.NumberOfPages,
		}
		if len(d.ISBN) > 0 {
			m.ISBN = d.ISBN[0]
		}
		if len(d.Publisher) > 0 {
			m.Publisher = d.Publisher[0]
		}
		if len(d.Language) > 0 {
			m.Language = d.Language[0]
		}
		if d.CoverI > 0 {
			m.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", d.CoverI)
		} else if d.CoverEditionKey != "" {
			m.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/olid/%s-L.jpg", d.CoverEditionKey)
		}
		out = append(out, m)
	}
	return out, nil
}

// SearchGoogleBooks queries the Google Books API v1. No API key
// required for unauthenticated use up to the documented rate
// limits. Use this as a fallback when OpenLibrary returns sparse
// results.
func SearchGoogleBooks(ctx context.Context, query string, limit int) ([]Match, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query required")
	}
	if limit <= 0 || limit > 40 {
		limit = 10
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("maxResults", fmt.Sprintf("%d", limit))
	endpoint := "https://www.googleapis.com/books/v1/volumes?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", "silo-audiobooks/enrich (+https://siloapp.com)")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google books: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("google books %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var raw struct {
		Items []struct {
			ID         string `json:"id"`
			VolumeInfo struct {
				Title         string   `json:"title"`
				Subtitle      string   `json:"subtitle"`
				Authors       []string `json:"authors"`
				Description   string   `json:"description"`
				Publisher     string   `json:"publisher"`
				PublishedDate string   `json:"publishedDate"`
				PageCount     int      `json:"pageCount"`
				Categories    []string `json:"categories"`
				Language      string   `json:"language"`
				ImageLinks    struct {
					Thumbnail string `json:"thumbnail"`
					Large     string `json:"large"`
				} `json:"imageLinks"`
				IndustryIdentifiers []struct {
					Type       string `json:"type"`
					Identifier string `json:"identifier"`
				} `json:"industryIdentifiers"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := make([]Match, 0, len(raw.Items))
	for _, it := range raw.Items {
		v := it.VolumeInfo
		m := Match{
			Provider:    "google_books",
			ProviderID:  it.ID,
			Title:       v.Title,
			Subtitle:    v.Subtitle,
			Authors:     v.Authors,
			Description: v.Description,
			Publisher:   v.Publisher,
			PublishYear: parseYearPrefix(v.PublishedDate),
			Genres:      capStringSlice(v.Categories, 6),
			Language:    v.Language,
			PageCount:   v.PageCount,
		}
		if v.ImageLinks.Large != "" {
			m.CoverURL = upgradeGoogleCover(v.ImageLinks.Large)
		} else if v.ImageLinks.Thumbnail != "" {
			m.CoverURL = upgradeGoogleCover(v.ImageLinks.Thumbnail)
		}
		for _, ii := range v.IndustryIdentifiers {
			if ii.Type == "ISBN_13" || (ii.Type == "ISBN_10" && m.ISBN == "") {
				m.ISBN = ii.Identifier
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// parseYearPrefix grabs the leading 4-digit year from a Google
// Books published-date string ("2014", "2014-03", "2014-03-21").
// Returns 0 on any failure — most callers treat 0 as unknown.
func parseYearPrefix(s string) int {
	if len(s) < 4 {
		return 0
	}
	y := 0
	for i := 0; i < 4; i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0
		}
		y = y*10 + int(c-'0')
	}
	if y < 1000 || y > 3000 {
		return 0
	}
	return y
}

// upgradeGoogleCover swaps the &zoom= parameter (Google serves
// tiny thumbnails by default) for a larger image. Idempotent on
// URLs that already lack the parameter.
func upgradeGoogleCover(u string) string {
	if u == "" {
		return ""
	}
	// Force https (Google still ships http URLs for some volumes)
	// and drop the edge parameter that adds page-curl borders.
	u = strings.Replace(u, "http://", "https://", 1)
	u = strings.Replace(u, "&edge=curl", "", 1)
	return u
}

// capStringSlice trims a slice to the first n entries. Used for
// genre lists that some providers return with 20+ subject tags
// when we really only want the top few.
func capStringSlice(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
