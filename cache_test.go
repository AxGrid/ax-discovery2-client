package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheRevalidateAndFallback(t *testing.T) {
	const etag = `"abc123"`
	const body = `[{"id":"i1","service":"api","address":"10.0.0.1"}]`
	var hits int32
	var down atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if down.Load() {
			w.WriteHeader(http.StatusBadGateway) // discovery unhealthy
			return
		}
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	// Tiny TTL so every call revalidates (exercises the conditional path).
	d := New(srv.URL, WithCacheDir(dir), WithCacheTTL(time.Millisecond))

	// 1) cold fetch → 200, populates cache.
	insts, err := d.Discover(context.Background(), "api")
	if err != nil || len(insts) != 1 {
		t.Fatalf("cold fetch: err=%v n=%d", err, len(insts))
	}

	// 2) after TTL → conditional GET → 304 → served from cache.
	time.Sleep(2 * time.Millisecond)
	if insts, err = d.Discover(context.Background(), "api"); err != nil || len(insts) != 1 {
		t.Fatalf("revalidate: err=%v n=%d", err, len(insts))
	}

	// 3) discovery goes unhealthy (502 on all endpoints) → served stale, no error.
	down.Store(true)
	time.Sleep(2 * time.Millisecond)
	if insts, err = d.Discover(context.Background(), "api"); err != nil || len(insts) != 1 {
		t.Fatalf("stale fallback: err=%v n=%d", err, len(insts))
	}

	// 4) a brand-new client (simulating a restart) with the same cache dir and
	//    discovery still down → boots off the persisted cache.
	d2 := New(srv.URL, WithCacheDir(dir), WithCacheTTL(time.Millisecond))
	time.Sleep(2 * time.Millisecond)
	if insts, err = d2.Discover(context.Background(), "api"); err != nil || len(insts) != 1 {
		t.Fatalf("restart-from-disk: err=%v n=%d", err, len(insts))
	}

	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("expected the server to have been queried")
	}
}

// Without a cache, discovery being down is a hard error (regression guard).
func TestNoCacheNoFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := New(srv.URL).Discover(context.Background(), "api"); err == nil {
		t.Fatal("expected an error with no cache and discovery down")
	}
}
