package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func jsonServer(body string, status int, hits *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// A 502 from the first node fails over to the second.
func TestFailoverOn5xx(t *testing.T) {
	var h1, h2 int32
	s1 := jsonServer(`{"error":"bad gateway"}`, http.StatusBadGateway, &h1)
	defer s1.Close()
	s2 := jsonServer(`[]`, http.StatusOK, &h2)
	defer s2.Close()

	d := New(s1.URL + "," + s2.URL)
	if _, err := d.ListServices(context.Background()); err != nil {
		t.Fatalf("expected failover success, got %v", err)
	}
	if atomic.LoadInt32(&h1) != 1 || atomic.LoadInt32(&h2) != 1 {
		t.Fatalf("expected each hit once, got s1=%d s2=%d", h1, h2)
	}
}

// When every endpoint returns 5xx, the last error is surfaced.
func TestAll5xxReturnsError(t *testing.T) {
	s1 := jsonServer(`{"error":"down"}`, http.StatusServiceUnavailable, nil)
	defer s1.Close()
	s2 := jsonServer(`{"error":"down"}`, http.StatusServiceUnavailable, nil)
	defer s2.Close()

	d := New(s1.URL + "," + s2.URL)
	if _, err := d.ListServices(context.Background()); err == nil {
		t.Fatal("expected an error when all endpoints are 5xx")
	}
}

// A custom retry predicate that ignores 5xx must not fail over.
func TestRetryOverrideNo5xx(t *testing.T) {
	var h2 int32
	s1 := jsonServer(`{"error":"bad gateway"}`, http.StatusBadGateway, nil)
	defer s1.Close()
	s2 := jsonServer(`[]`, http.StatusOK, &h2)
	defer s2.Close()

	d := New(s1.URL+","+s2.URL, WithRetry(func(_ string, _ int, err error) bool { return err != nil }))
	if _, err := d.ListServices(context.Background()); err == nil {
		t.Fatal("expected the 502 to surface (no 5xx retry)")
	}
	if atomic.LoadInt32(&h2) != 0 {
		t.Fatalf("second endpoint should not be tried, got %d hits", h2)
	}
}

// Sequential (default) always tries the first endpoint first; a healthy first
// node means the second is never reached.
func TestSequentialOrder(t *testing.T) {
	var h2 int32
	s1 := jsonServer(`[]`, http.StatusOK, nil)
	defer s1.Close()
	s2 := jsonServer(`[]`, http.StatusOK, &h2)
	defer s2.Close()

	d := New(s1.URL + "," + s2.URL)
	for range 12 {
		if _, err := d.ListServices(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&h2) != 0 {
		t.Fatalf("sequential order should never reach the second node, got %d hits", h2)
	}
}

// Random order spreads requests across both healthy nodes.
func TestRandomOrder(t *testing.T) {
	var h1, h2 int32
	s1 := jsonServer(`[]`, http.StatusOK, &h1)
	defer s1.Close()
	s2 := jsonServer(`[]`, http.StatusOK, &h2)
	defer s2.Close()

	d := New(s1.URL+","+s2.URL, WithEndpointOrder(OrderRandom))
	for range 50 {
		if _, err := d.ListServices(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&h1) == 0 || atomic.LoadInt32(&h2) == 0 {
		t.Fatalf("random order should hit both nodes, got s1=%d s2=%d", h1, h2)
	}
}
