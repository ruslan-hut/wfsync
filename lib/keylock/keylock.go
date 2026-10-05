// Package keylock provides per-key mutual exclusion.
package keylock

import "sync"

// Map serializes work per key — in this service, per order reference (id_external).
//
// The duplicate guards are check-then-create sequences over several network calls
// (invoices/find → invoices/add), and wFirma enforces no uniqueness on id_external.
// Without a lock, two triggers for the same order — the capture API goroutine, the
// Stripe webhook and the reconciler for fakturas, a client retrying a timed-out request
// for proformas — each see "no document yet" and both create one. Holding the lock
// across find→add closes that window.
//
// The guarantee is process-local: it protects a single running instance, which is the
// deployment model here (one systemd unit). Locks are reference-counted and dropped once
// idle so the map does not grow with every key handled.
type Map struct {
	mu    sync.Mutex
	locks map[string]*entry
}

type entry struct {
	mu   sync.Mutex
	refs int
}

// New returns an empty Map.
func New() *Map {
	return &Map{locks: make(map[string]*entry)}
}

// Lock blocks until the caller holds the lock for key and returns the release function.
// An empty key is a no-op (nothing to serialize on), as is a nil receiver (an owner built
// as a struct literal), so callers can invoke it unconditionally.
func (o *Map) Lock(key string) func() {
	if o == nil || key == "" {
		return func() {}
	}

	o.mu.Lock()
	l := o.locks[key]
	if l == nil {
		l = &entry{}
		o.locks[key] = l
	}
	l.refs++
	o.mu.Unlock()

	l.mu.Lock()

	return func() {
		l.mu.Unlock()
		o.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(o.locks, key)
		}
		o.mu.Unlock()
	}
}
