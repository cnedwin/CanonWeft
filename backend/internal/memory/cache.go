package memory

import (
	"container/list"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrCacheCapacity = errors.New("memory cache entry exceeds configured capacity")

// Cache holds disposable compiled packs. A hit never replaces source and
// permission validation, and a miss must be recoverable from durable state.
type Cache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Put(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

type cacheEntry struct {
	key     string
	value   []byte
	expires time.Time
}

// LocalCache bounds both payload bytes and entry count. Bytes includes keys and
// values; list/map overhead is bounded separately by maxEntries.
type LocalCache struct {
	mu                          sync.Mutex
	entries                     map[string]*list.Element
	lru                         *list.List
	maxEntries, maxBytes, bytes int
	now                         func() time.Time
}

func NewLocalCache(maxEntries, maxBytes int) *LocalCache {
	return &LocalCache{entries: make(map[string]*list.Element), lru: list.New(), maxEntries: maxEntries, maxBytes: maxBytes, now: time.Now}
}

func validCacheKey(key string) bool {
	if !strings.HasPrefix(key, "memory:") || len(key) != len("memory:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(key, "memory:"))
	return err == nil
}

func (c *LocalCache) remove(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	c.bytes -= len(entry.key) + len(entry.value)
	delete(c.entries, entry.key)
	c.lru.Remove(element)
}

func (c *LocalCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !validCacheKey(key) {
		return nil, false, ErrInvalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[key]
	if !ok {
		return nil, false, nil
	}
	entry := element.Value.(*cacheEntry)
	if !c.now().Before(entry.expires) {
		c.remove(element)
		return nil, false, nil
	}
	c.lru.MoveToFront(element)
	return append([]byte(nil), entry.value...), true, nil
}

func (c *LocalCache) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validCacheKey(key) || ttl <= 0 {
		return ErrInvalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	size := len(key) + len(value)
	if c.maxEntries <= 0 || size > c.maxBytes {
		return ErrCacheCapacity
	}
	now := c.now()
	// Reclaim expired entries before evicting any still useful entry.
	for element := c.lru.Back(); element != nil; {
		previous := element.Prev()
		if !now.Before(element.Value.(*cacheEntry).expires) {
			c.remove(element)
		}
		element = previous
	}
	if old, ok := c.entries[key]; ok {
		c.remove(old)
	}
	for len(c.entries) >= c.maxEntries || c.bytes+size > c.maxBytes {
		c.remove(c.lru.Back())
	}
	entry := &cacheEntry{key: key, value: append([]byte(nil), value...), expires: now.Add(ttl)}
	c.entries[key] = c.lru.PushFront(entry)
	c.bytes += size
	return nil
}

func (c *LocalCache) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validCacheKey(key) {
		return ErrInvalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.remove(element)
	}
	return nil
}

func (c *LocalCache) Bytes() int { c.mu.Lock(); defer c.mu.Unlock(); return c.bytes }

func (c *LocalCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*list.Element)
	c.lru.Init()
	c.bytes = 0
}
