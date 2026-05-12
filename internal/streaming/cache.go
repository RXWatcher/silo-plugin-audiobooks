package streaming

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Cache is a content-addressed file cache backed by the audiobook_file_cache
// DB table. Single-flight downloads per cache key.
type Cache struct {
	dir      string
	maxBytes int64
	st       *store.Store

	mu       sync.Mutex
	inflight map[string]chan struct{}
}

// NewCache wires a cache.
func NewCache(dir string, maxBytes int64, st *store.Store) *Cache {
	if maxBytes <= 0 {
		maxBytes = 50 * 1024 * 1024 * 1024 // 50 GiB default
	}
	return &Cache{dir: dir, maxBytes: maxBytes, st: st, inflight: map[string]chan struct{}{}}
}

// MaxBytes returns the configured cache size cap.
func (c *Cache) MaxBytes() int64 { return c.maxBytes }

// CacheEntry is an open file plus metadata for an HTTP response.
type CacheEntry struct {
	File    *os.File
	Path    string
	MimeType string
	Size     int64
	ModTime  time.Time
}

// Close closes the underlying file.
func (e *CacheEntry) Close() { _ = e.File.Close() }

// Fetcher is invoked on cache miss to obtain the audio bytes.
type Fetcher func(ctx context.Context) (rc io.ReadCloser, mimeType string, contentLength int64, err error)

// Get returns a CacheEntry — either an existing file or a freshly-downloaded
// one. Single-flight per cache_key.
func (c *Cache) Get(ctx context.Context, key string, fetch Fetcher) (*CacheEntry, error) {
	entry, exists, err := c.lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	if exists {
		_ = c.st.TouchFileCache(ctx, key)
		return entry, nil
	}

	// Single-flight: one downloader per key.
	c.mu.Lock()
	if ch, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-ch
		entry, exists, err = c.lookup(ctx, key)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("download finished but cache empty")
		}
		return entry, nil
	}
	ch := make(chan struct{})
	c.inflight[key] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		close(ch)
		delete(c.inflight, key)
		c.mu.Unlock()
	}()

	if err := c.download(ctx, key, fetch); err != nil {
		return nil, err
	}
	entry, exists, err = c.lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("download completed but cache miss")
	}
	return entry, nil
}

func (c *Cache) relPath(key string) string {
	h := sha256.Sum256([]byte(key))
	hexs := hex.EncodeToString(h[:])
	return filepath.Join(hexs[:2], hexs)
}

func (c *Cache) lookup(ctx context.Context, key string) (*CacheEntry, bool, error) {
	rec, err := c.st.GetFileCacheByKey(ctx, key)
	if err != nil {
		return nil, false, nil // treat any error as miss
	}
	if rec.Status != "ready" {
		return nil, false, nil
	}
	full := filepath.Join(c.dir, rec.RelativePath)
	f, err := os.Open(full)
	if err != nil {
		_ = c.st.UpdateFileCacheStatus(ctx, key, "failed", 0, "file missing on disk")
		return nil, false, nil
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	return &CacheEntry{
		File:    f,
		Path:    full,
		MimeType: rec.MimeType,
		Size:     info.Size(),
		ModTime:  info.ModTime(),
	}, true, nil
}

func (c *Cache) download(ctx context.Context, key string, fetch Fetcher) error {
	rc, mime, length, err := fetch(ctx)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer rc.Close()

	relPath := c.relPath(key)
	full := filepath.Join(c.dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("mkdir cache: %w", err)
	}
	tmp := full + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create cache file: %w", err)
	}

	id := ulid.Make().String()
	if err := c.st.InsertFileCacheEntry(ctx, store.FileCacheEntry{
		ID:            id,
		CacheKey:      key,
		BookID:        cacheKeyToBookID(key),
		Filename:      filepath.Base(full),
		MimeType:      mime,
		ContentLength: length,
		Status:        "downloading",
		RelativePath:  relPath,
	}); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("insert cache entry: %w", err)
	}

	n, err := io.Copy(f, rc)
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		_ = c.st.UpdateFileCacheStatus(ctx, key, "failed", 0, err.Error())
		return fmt.Errorf("io.Copy: %w", err)
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = c.st.UpdateFileCacheStatus(ctx, key, "failed", 0, err.Error())
		return fmt.Errorf("rename: %w", err)
	}
	if err := c.st.UpdateFileCacheStatus(ctx, key, "ready", n, ""); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

func cacheKeyToBookID(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i]
		}
	}
	return key
}

// Evict ensures total bytes <= target. Returns the number of entries removed.
func (c *Cache) Evict(ctx context.Context, target int64) (int, error) {
	total, err := c.st.TotalCacheBytes(ctx)
	if err != nil {
		return 0, err
	}
	if total <= target {
		return 0, nil
	}
	candidates, err := c.st.ListFileCacheLRU(ctx, 200)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range candidates {
		full := filepath.Join(c.dir, e.RelativePath)
		_ = os.Remove(full)
		_ = c.st.DeleteFileCacheByKey(ctx, e.CacheKey)
		total -= e.BytesOnDisk
		removed++
		if total <= target {
			break
		}
	}
	return removed, nil
}
