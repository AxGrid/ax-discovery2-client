package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CacheBackend stores opaque cache records keyed by a string. It deals in raw
// bytes so any store works — the filesystem (FileCache), a database, Redis, etc.
// — without the core client depending on it. Implementations are best-effort:
// Get returns ok=false on any miss or error, and Set may silently drop on error
// (the cache only ever degrades to "no cache").
type CacheBackend interface {
	Get(key string) (data []byte, ok bool)
	Set(key string, data []byte)
}

// cacheRecord is the JSON payload stored under each key.
type cacheRecord struct {
	ETag      string    `json:"etag,omitempty"`
	Body      []byte    `json:"body"`
	FetchedAt time.Time `json:"fetchedAt"`
}

func cacheKey(method, path string) string {
	sum := sha256.Sum256([]byte(method + " " + path))
	return hex.EncodeToString(sum[:])
}

// --- filesystem backend ---

// FileCache is a CacheBackend that writes one file per key under a directory.
type FileCache struct{ dir string }

// NewFileCache creates a filesystem-backed cache rooted at dir (created if missing).
func NewFileCache(dir string) *FileCache {
	_ = os.MkdirAll(dir, 0o755)
	return &FileCache{dir: dir}
}

func (f *FileCache) path(key string) string { return filepath.Join(f.dir, key+".json") }

func (f *FileCache) Get(key string) ([]byte, bool) {
	b, err := os.ReadFile(f.path(key))
	if err != nil {
		return nil, false
	}
	return b, true
}

func (f *FileCache) Set(key string, data []byte) {
	tmp := f.path(key) + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, f.path(key)) // atomic replace
	}
}

// --- cached GET path ---

// doGet performs a GET and returns the body + ETag (bare hex, no quotes). With a
// cache backend it serves fresh-enough entries without a round-trip, revalidates
// with If-None-Match (304 = cheap), and falls back to the stale entry when every
// discovery node is unreachable or unhealthy.
func (c *Client) doGet(ctx context.Context, path string) (body []byte, etag string, err error) {
	if c.cache == nil {
		resp, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, "", err
		}
		return readBody(resp)
	}

	key := cacheKey(http.MethodGet, path)
	rec := c.cacheLoad(key)
	ttl := c.cacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if rec != nil && time.Since(rec.FetchedAt) < ttl {
		return rec.Body, rec.ETag, nil // fresh enough — no network
	}

	var extra http.Header
	if rec != nil && rec.ETag != "" {
		extra = http.Header{"If-None-Match": {`"` + rec.ETag + `"`}}
	}
	resp, err := c.doH(ctx, http.MethodGet, path, nil, extra)
	if err != nil {
		if rec != nil {
			return rec.Body, rec.ETag, nil // all endpoints unreachable → serve stale
		}
		return nil, "", err
	}
	defer resp.Body.Close()
	respETag := strings.Trim(resp.Header.Get("ETag"), `"`)

	switch {
	case resp.StatusCode == http.StatusNotModified && rec != nil:
		rec.FetchedAt = time.Now()
		c.cacheStore(key, rec)
		return rec.Body, rec.ETag, nil
	case resp.StatusCode >= 500 && rec != nil:
		return rec.Body, rec.ETag, nil // discovery unhealthy → serve stale
	case resp.StatusCode >= 400:
		return nil, respETag, bodyError(resp)
	default:
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, respETag, err
		}
		c.cacheStore(key, &cacheRecord{ETag: respETag, Body: b, FetchedAt: time.Now()})
		return b, respETag, nil
	}
}

func (c *Client) cacheLoad(key string) *cacheRecord {
	raw, ok := c.cache.Get(key)
	if !ok {
		return nil
	}
	var rec cacheRecord
	if json.Unmarshal(raw, &rec) != nil {
		return nil
	}
	return &rec
}

func (c *Client) cacheStore(key string, rec *cacheRecord) {
	if raw, err := json.Marshal(rec); err == nil {
		c.cache.Set(key, raw)
	}
}

// readBody reads a (non-conditional) response into bytes + ETag, turning a 4xx/5xx
// into an error like decodeJSON does.
func readBody(resp *http.Response) ([]byte, string, error) {
	defer resp.Body.Close()
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if resp.StatusCode >= 400 {
		return nil, etag, bodyError(resp)
	}
	b, err := io.ReadAll(resp.Body)
	return b, etag, err
}

func bodyError(resp *http.Response) error {
	var e struct{ Error string }
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &e)
	if e.Error == "" {
		e.Error = resp.Status
	}
	return fmt.Errorf("discovery: %s", e.Error)
}
