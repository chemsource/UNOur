package sqlite

import (
	"context"
	"sync"
)

type keyLock struct {
	gate chan struct{}
	refs int
}
type keyLocks struct {
	mu   sync.Mutex
	keys map[string]*keyLock
}

// acquire bounds lock memory by current requests, including waiters. No database
// connection or global mutex is held while waiting for another room operation.
func (l *keyLocks) acquire(ctx context.Context, id string, try bool) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	l.mu.Lock()
	if l.keys == nil {
		l.keys = make(map[string]*keyLock)
	}
	k := l.keys[id]
	if k == nil {
		k = &keyLock{gate: make(chan struct{}, 1)}
		k.gate <- struct{}{}
		l.keys[id] = k
	}
	k.refs++
	l.mu.Unlock()
	drop := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		k.refs--
		if k.refs == 0 {
			delete(l.keys, id)
		}
	}
	if try {
		select {
		case <-k.gate:
		default:
			drop()
			return nil, false, nil
		}
	} else {
		select {
		case <-ctx.Done():
			drop()
			return nil, false, ctx.Err()
		case <-k.gate:
		}
	}
	return func() { k.gate <- struct{}{}; drop() }, true, nil
}
