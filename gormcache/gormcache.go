// Package gormcache provides a GORM-backed cache for discovery2-client.
//
// It lives in its own module so GORM is only a dependency for apps that opt in
// — the core discovery2-client stays dependency-light. It implements the
// discovery.CacheBackend interface structurally (Get/Set on []byte), so it
// doesn't even import the parent.
//
//	db, _ := gorm.Open(sqlite.Open("cache.db"))
//	cache, _ := gormcache.New(db)
//	d := discovery.New(url, discovery.WithCache(cache))
package gormcache

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// record is the cache row. Column names avoid SQL reserved words.
type record struct {
	CacheKey string `gorm:"column:cache_key;primaryKey;size:64"`
	Data     []byte `gorm:"column:data"`
}

func (record) TableName() string { return "discovery_cache" }

// Cache stores discovery2-client cache entries in a GORM-managed table.
type Cache struct{ db *gorm.DB }

// New auto-migrates the cache table and returns a Cache ready to pass to
// discovery.WithCache(...).
func New(db *gorm.DB) (*Cache, error) {
	if err := db.AutoMigrate(&record{}); err != nil {
		return nil, err
	}
	return &Cache{db: db}, nil
}

// Get returns the stored bytes for key (best-effort: any error → miss).
func (c *Cache) Get(key string) ([]byte, bool) {
	var r record
	if err := c.db.Where("cache_key = ?", key).Take(&r).Error; err != nil {
		return nil, false
	}
	return r.Data, true
}

// Set upserts the bytes for key (best-effort: errors are ignored — the cache
// only ever degrades to "no cache").
func (c *Cache) Set(key string, data []byte) {
	_ = c.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "cache_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"data"}),
	}).Create(&record{CacheKey: key, Data: data}).Error
}
