// Package store provides generic in-memory storage with TTL support.
package store

import (
	"sync"
	"time"
)

// Entry wraps a value with expiration metadata
type Entry[T any] struct {
	Value     T
	ExpiresAt time.Time
}

// IsExpired returns true if the entry has expired
func (e *Entry[T]) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

// TTL returns the remaining time until expiration
func (e *Entry[T]) TTL() time.Duration {
	remaining := time.Until(e.ExpiresAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// TTLStore is a generic in-memory store with TTL support and automatic cleanup.
type TTLStore[K comparable, V any] struct {
	mu       sync.RWMutex
	items    map[K]*Entry[V]
	stopCh   chan struct{}
	interval time.Duration
	onEvict  func(key K, value V) // Optional callback called when items are evicted
}

// NewTTLStore creates a new TTL store with the specified cleanup interval.
// The cleanup goroutine runs every `cleanupInterval` to remove expired entries.
func NewTTLStore[K comparable, V any](cleanupInterval time.Duration) *TTLStore[K, V] {
	s := &TTLStore[K, V]{
		items:    make(map[K]*Entry[V]),
		stopCh:   make(chan struct{}),
		interval: cleanupInterval,
	}
	go s.cleanupLoop()
	return s
}

// NewTTLStoreWithEvict creates a new TTL store with an eviction callback.
// The callback is called when items are removed during cleanup (not on manual Delete).
func NewTTLStoreWithEvict[K comparable, V any](cleanupInterval time.Duration, onEvict func(key K, value V)) *TTLStore[K, V] {
	s := &TTLStore[K, V]{
		items:    make(map[K]*Entry[V]),
		stopCh:   make(chan struct{}),
		interval: cleanupInterval,
		onEvict:  onEvict,
	}
	go s.cleanupLoop()
	return s
}

// SetOnEvict sets the callback function called when items are evicted during cleanup.
// This can be called after construction to add or change the eviction callback.
func (s *TTLStore[K, V]) SetOnEvict(fn func(key K, value V)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onEvict = fn
}

// Set stores a value with the given TTL
func (s *TTLStore[K, V]) Set(key K, value V, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items[key] = &Entry[V]{
		Value:     value,
		ExpiresAt: time.Now().Add(ttl),
	}
}

// SetWithExpiry stores a value with an absolute expiration time
func (s *TTLStore[K, V]) SetWithExpiry(key K, value V, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items[key] = &Entry[V]{
		Value:     value,
		ExpiresAt: expiresAt,
	}
}

// Get retrieves a value by key. Returns the value and true if found and not expired.
func (s *TTLStore[K, V]) Get(key K) (V, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.items[key]
	if !exists || entry.IsExpired() {
		var zero V
		return zero, false
	}
	return entry.Value, true
}

// GetEntry retrieves the full entry with metadata
func (s *TTLStore[K, V]) GetEntry(key K) (*Entry[V], bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.items[key]
	if !exists || entry.IsExpired() {
		return nil, false
	}
	return entry, true
}

// Delete removes a key from the store
func (s *TTLStore[K, V]) Delete(key K) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.items[key]; exists {
		delete(s.items, key)
		return true
	}
	return false
}

// Has returns true if the key exists and is not expired
func (s *TTLStore[K, V]) Has(key K) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.items[key]
	return exists && !entry.IsExpired()
}

// Len returns the number of non-expired items
func (s *TTLStore[K, V]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, entry := range s.items {
		if !entry.IsExpired() {
			count++
		}
	}
	return count
}

// All returns all non-expired entries as a map
func (s *TTLStore[K, V]) All() map[K]V {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[K]V)
	for key, entry := range s.items {
		if !entry.IsExpired() {
			result[key] = entry.Value
		}
	}
	return result
}

// AllEntries returns all non-expired entries with metadata
func (s *TTLStore[K, V]) AllEntries() map[K]*Entry[V] {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[K]*Entry[V])
	for key, entry := range s.items {
		if !entry.IsExpired() {
			result[key] = entry
		}
	}
	return result
}

// ForEach iterates over all non-expired items
func (s *TTLStore[K, V]) ForEach(fn func(key K, value V) bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for key, entry := range s.items {
		if !entry.IsExpired() {
			if !fn(key, entry.Value) {
				break
			}
		}
	}
}

// Refresh updates the TTL for an existing key without changing the value
func (s *TTLStore[K, V]) Refresh(key K, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.items[key]
	if !exists {
		return false
	}
	entry.ExpiresAt = time.Now().Add(ttl)
	return true
}

// Update modifies the value for an existing key and optionally refreshes TTL
func (s *TTLStore[K, V]) Update(key K, fn func(V) V, newTTL *time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.items[key]
	if !exists || entry.IsExpired() {
		return false
	}

	entry.Value = fn(entry.Value)
	if newTTL != nil {
		entry.ExpiresAt = time.Now().Add(*newTTL)
	}
	return true
}

// Clear removes all items from the store
func (s *TTLStore[K, V]) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[K]*Entry[V])
}

// Close stops the cleanup goroutine and clears the store
func (s *TTLStore[K, V]) Close() {
	close(s.stopCh)
	s.Clear()
}

// cleanupLoop periodically removes expired entries
func (s *TTLStore[K, V]) cleanupLoop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.stopCh:
			return
		}
	}
}

// cleanup removes all expired entries and calls the eviction callback if set
func (s *TTLStore[K, V]) cleanup() {
	// Collect expired entries while holding lock
	s.mu.Lock()
	var expired []struct {
		key   K
		value V
	}

	for key, entry := range s.items {
		if entry.IsExpired() {
			expired = append(expired, struct {
				key   K
				value V
			}{key, entry.Value})
			delete(s.items, key)
		}
	}
	onEvict := s.onEvict
	s.mu.Unlock()

	// Call eviction callbacks outside of the critical section to avoid deadlocks
	if onEvict != nil {
		for _, e := range expired {
			onEvict(e.key, e.value)
		}
	}
}
