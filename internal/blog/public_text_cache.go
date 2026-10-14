package blog

import (
	"container/list"
	"strings"
	"sync"
)

// Bound both retained text and entry metadata. Content keys make article edits,
// translations and restored data immediately independent of previous results.
var publicTextCache = newPublicPlainTextCache(512, 16<<20)

type publicPlainTextCache struct {
	mu         sync.RWMutex
	entries    map[string]string
	order      list.List
	bytes      int
	maxEntries int
	maxBytes   int
}

func newPublicPlainTextCache(maxEntries, maxBytes int) *publicPlainTextCache {
	return &publicPlainTextCache{entries: make(map[string]string), maxEntries: maxEntries, maxBytes: maxBytes}
}

func (c *publicPlainTextCache) text(input string) string {
	if input == "" {
		return ""
	}
	c.mu.RLock()
	text, found := c.entries[input]
	c.mu.RUnlock()
	if found {
		return text
	}
	// Parsing stays outside the lock so unrelated warm readers never queue
	// behind a long article. A simultaneous miss is harmless duplicated work.
	text = parsePublicPlainText(input)
	size := len(input) + len(text)
	if c.maxEntries <= 0 || size > c.maxBytes {
		return text
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, found := c.entries[input]; found {
		return cached
	}
	for len(c.entries) >= c.maxEntries || c.bytes+size > c.maxBytes {
		oldest := c.order.Front()
		key := oldest.Value.(string)
		c.bytes -= len(key) + len(c.entries[key])
		delete(c.entries, key)
		c.order.Remove(oldest)
	}
	// A substring may otherwise keep a much larger source allocation alive.
	key := strings.Clone(input)
	text = strings.Clone(text)
	c.entries[key] = text
	c.order.PushBack(key)
	c.bytes += size
	return text
}
