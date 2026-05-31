package gormcache

import (
	"bytes"
	"testing"

	"github.com/glebarez/sqlite" // pure-Go sqlite (no CGO)
	"gorm.io/gorm"
)

func TestGormCacheRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(db)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected miss for unknown key")
	}

	c.Set("k1", []byte("hello"))
	got, ok := c.Get("k1")
	if !ok || !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("get after set: ok=%v got=%q", ok, got)
	}

	// upsert overwrites
	c.Set("k1", []byte("world"))
	got, ok = c.Get("k1")
	if !ok || !bytes.Equal(got, []byte("world")) {
		t.Fatalf("get after upsert: ok=%v got=%q", ok, got)
	}
}
