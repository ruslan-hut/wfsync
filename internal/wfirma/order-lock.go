package wfirma

import "sync"

// orderLocks serializes faktura creation per order reference (id_external).
//
// The duplicate guard is a check-then-create sequence over two network calls
// (invoices/find → invoices/add), and wFirma enforces no uniqueness on id_external.
// Without a lock, two triggers observing the same capture — the capture API goroutine,
// the Stripe webhook, and the reconciler can all fire within seconds — each see "no
// faktura yet" and both create one. Holding the lock across find→add closes that window.
//
// The guarantee is process-local: it protects a single running instance, which is the
// deployment model here (one systemd unit). Locks are reference-counted and dropped once
// idle so the map does not grow with every order handled.
type orderLocks struct {
	mu    sync.Mutex
	locks map[string]*orderLock
}

type orderLock struct {
	mu   sync.Mutex
	refs int
}

func newOrderLocks() *orderLocks {
	return &orderLocks{locks: make(map[string]*orderLock)}
}

// lock blocks until the caller holds the lock for key and returns the release function.
// An empty key is a no-op (nothing to serialize on), as is a nil receiver (a Client built
// as a struct literal rather than via NewClient), so callers can invoke it unconditionally.
func (o *orderLocks) lock(key string) func() {
	if o == nil || key == "" {
		return func() {}
	}

	o.mu.Lock()
	l := o.locks[key]
	if l == nil {
		l = &orderLock{}
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
