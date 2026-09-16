package v2rayxhttp

import (
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"time"
)

// XMUX counts logical streams separately from HTTP requests. One packet-up
// stream can consume many requests; stream-up consumes two concurrent requests.
type pooledTransport struct {
	http.RoundTripper
	active           int
	inFlight         int
	uses             int32
	requests         int32
	expires          time.Time
	retired          bool
	closed           bool
	closeIdlePending bool
}

type clientPool struct {
	keepIdle    bool
	mu          sync.Mutex
	config      poolConfig
	concurrency int32
	connections int32
	factory     func() http.RoundTripper
	entries     []*pooledTransport
}

type poolLease struct {
	pool   *clientPool
	entry  *pooledTransport
	closed bool
}

func newClientPool(config poolConfig, factory func() http.RoundTripper) *clientPool {
	return &clientPool{
		keepIdle:    true,
		config:      config,
		concurrency: config.concurrency.sample(),
		connections: config.connections.sample(),
		factory:     factory,
	}
}

func (p *clientPool) acquire() *poolLease {
	p.mu.Lock()
	defer p.mu.Unlock()
	return &poolLease{pool: p, entry: p.selectLocked()}
}

func (p *clientPool) selectLocked() *pooledTransport {
	now := time.Now()
	live := p.entries[:0]

	for _, e := range p.entries {
		if e.requests == 0 || e.uses == 0 || (!e.expires.IsZero() && !now.Before(e.expires)) {
			e.retired = true
		}
		if e.retired {
			p.maybeCloseLocked(e)
		} else {
			live = append(live, e)
		}
	}

	clear(p.entries[len(live):])
	p.entries = live

	var e *pooledTransport

	if len(live) > 0 {
		if p.connections > 0 {
			if len(live) >= int(p.connections) {
				e = live[rand.IntN(len(live))]
			}
		} else {
			for _, candidate := range live {
				if p.concurrency == 0 || candidate.active < int(p.concurrency) {
					e = candidate
					break
				}
			}
		}
	}

	if e == nil {
		e = &pooledTransport{RoundTripper: p.factory(), uses: -1, requests: -1}
		if n := p.config.reuse.sample(); n > 0 {
			e.uses = n
		}
		if n := p.config.requests.sample(); n > 0 {
			e.requests = n
		}
		if n := p.config.lifetime.sample(); n > 0 {
			e.expires = now.Add(time.Duration(n) * time.Second)
		}
		p.entries = append(p.entries, e)
	}

	e.active++

	if e.uses > 0 {
		e.uses--
	}
	return e
}

func (l *poolLease) RoundTrip(req *http.Request) (*http.Response, error) {
	p := l.pool

	p.mu.Lock()
	if l.closed {
		p.mu.Unlock()
		return nil, net.ErrClosed
	}
	e := l.entry
	// Reuse limits retire a connection for new streams, but existing streams only
	// rotate on request/time limits. Never interrupt a live download to rotate.
	if e.requests == 0 || (!e.expires.IsZero() && !time.Now().Before(e.expires)) || e.closed {
		e.retired = true
		e.active--
		p.maybeCloseLocked(e)
		e = p.selectLocked()
		l.entry = e
	}
	if e.requests > 0 {
		e.requests--
	}
	e.inFlight++
	p.mu.Unlock()

	response, err := e.RoundTrip(req)
	done := func() {
		p.mu.Lock()
		e.inFlight--
		if err != nil {
			e.retired = true
		}
		p.maybeCloseLocked(e)
		p.mu.Unlock()
	}
	if err != nil {
		done()
		return nil, err
	}
	response.Body = &pooledBody{ReadCloser: response.Body, done: done}
	return response, nil
}

func (l *poolLease) Close() {
	p := l.pool
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	l.entry.active--
	p.maybeCloseLocked(l.entry)
}

func (p *clientPool) maybeCloseLocked(e *pooledTransport) {
	// HTTP/3's transport can consider a connection idle as soon as response
	// headers arrive. Our counters include the entire body and logical stream.
	if e.closed || e.active != 0 || e.inFlight != 0 {
		return
	}
	if e.retired {
		e.closed = true
		closeTransport(e.RoundTripper)
	} else if e.closeIdlePending || !p.keepIdle {
		e.closeIdlePending = false
		closeIdleTransport(e.RoundTripper)
	}
}

type pooledBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (b *pooledBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.done)
	return err
}

func closeTransport(t http.RoundTripper) {
	if c, ok := t.(io.Closer); ok {
		c.Close()
	}
	if c, ok := t.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

func (p *clientPool) closeIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		e.closeIdlePending = true
		p.maybeCloseLocked(e)
	}
}

func (p *clientPool) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		e.retired = true
		e.closed = true
		closeTransport(e.RoundTripper)
	}
	p.entries = nil
}

func closeIdleTransport(t http.RoundTripper) {
	if idle, ok := t.(interface{ CloseIdleConnections() }); ok {
		idle.CloseIdleConnections()
	}
}

func (p *clientPool) setKeepIdle(keep bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keepIdle = keep
	if !keep {
		for _, entry := range p.entries {
			p.maybeCloseLocked(entry)
		}
	}
}
