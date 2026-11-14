package typedef

import "sync"

// Cache keeps parsed Types by (id, version). A registered version never
// changes, so an entry never goes stale and needs no invalidation. A *Type
// is not modified after Parse or FromJSON, so callers may share it.
//
// The zero value is ready to use. It is safe for concurrent use: sync.Map
// is Go's counterpart to a ConcurrentHashMap.
type Cache struct {
	m sync.Map // cacheKey -> *Type
}

type cacheKey struct {
	id      string
	version int
}

// Load returns the cached Type, or calls read and caches what it returns.
// Errors are not cached, so a Type registered later is found then.
// Two callers that miss at the same moment may both call read; both get an
// equal Type, and the second simply replaces the first.
func (c *Cache) Load(id string, version int, read func() (*Type, error)) (*Type, error) {
	key := cacheKey{id, version}
	// Load returns `any`; .(*Type) asserts it back to the concrete type.
	if t, ok := c.m.Load(key); ok {
		return t.(*Type), nil
	}
	t, err := read()
	if err != nil {
		return nil, err
	}
	c.m.Store(key, t)
	return t, nil
}
