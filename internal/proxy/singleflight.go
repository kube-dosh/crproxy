package proxy

import "sync"

type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done chan struct{}
	err  error
}

func (g *flightGroup) Do(key string, fn func() error) error {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = map[string]*flightCall{}
	}
	if existing := g.calls[key]; existing != nil {
		g.mu.Unlock()
		<-existing.done
		return existing.err
	}
	call := &flightCall{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	call.err = fn()
	g.mu.Lock()
	delete(g.calls, key)
	close(call.done)
	g.mu.Unlock()
	return call.err
}
