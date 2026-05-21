package proxy

import (
	"sync"
)

type ConnTracker struct {
	mu    sync.Mutex
	conns map[string]bool
}

var ActiveConns = &ConnTracker{conns: make(map[string]bool)}

func (t *ConnTracker) Add(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.conns[id] = true
}

func (t *ConnTracker) Remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.conns, id)
}

func (t *ConnTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.conns)
}

func (t *ConnTracker) GracefulShutdown() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.conns {
		delete(t.conns, id)
	}
}
