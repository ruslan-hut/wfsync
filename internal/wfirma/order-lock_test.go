package wfirma

import (
	"sync"
	"testing"
)

// TestOrderLocksSerializesSameKey is the property the duplicate guard depends on: two
// goroutines holding the lock for one order never overlap, so a find→add sequence cannot
// be interleaved by a second trigger for the same order.
func TestOrderLocksSerializesSameKey(t *testing.T) {
	locks := newOrderLocks()

	var mu sync.Mutex
	inside, maxInside := 0, 0

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := locks.lock("order-1")
			defer unlock()

			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()

			mu.Lock()
			inside--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if maxInside != 1 {
		t.Errorf("concurrent holders of the same key = %d, want 1", maxInside)
	}
	if n := len(locks.locks); n != 0 {
		t.Errorf("locks retained after release = %d, want 0", n)
	}
}

// TestOrderLocksIndependentKeys guards against serializing unrelated orders: two different
// keys must be held simultaneously, otherwise invoice creation becomes globally single-file.
func TestOrderLocksIndependentKeys(t *testing.T) {
	locks := newOrderLocks()

	first := locks.lock("order-1")
	done := make(chan struct{})
	go func() {
		locks.lock("order-2")()
		close(done)
	}()

	<-done // would block forever if different keys shared a lock
	first()
}

// TestOrderLocksNoKey covers the callers that pass an empty external ref and the zero-value
// client: both must be no-ops rather than a panic or a global lock.
func TestOrderLocksNoKey(t *testing.T) {
	locks := newOrderLocks()
	locks.lock("")()
	locks.lock("")()

	var nilLocks *orderLocks
	nilLocks.lock("order-1")()
}
