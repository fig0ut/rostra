package v2rayxhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

var errPacketConflict = errors.New("xhttp: conflicting upload packet")

const (
	serverSessionLimit = 1024
	serverBufferBudget = 64 * 1024 * 1024
	serverJoinTimeout  = 30 * time.Second
)

// A session owns a pipe and ordered upload queue. The HTTP download handler is
// the sole response writer; protocol handlers see normal net.Conn deadlines.
type serverSession struct {
	server           *Server
	id               string
	ctx              context.Context
	cancel           context.CancelFunc
	app, wire        *duplexPipe
	mu               sync.Mutex
	closed, download bool
	upload           string
	next             uint64
	packets          map[uint64][]byte
	wake             chan struct{}
	space            chan struct{}
	joinTimer        *time.Timer
}

func (s *Server) session(id string) *serverSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if id != "" {
		if existing := s.sessions[id]; existing != nil {
			return existing
		}
	}

	if len(s.active) >= serverSessionLimit {
		return nil
	}

	ctx, cancel := context.WithCancel(s.ctx)
	app, wire := newDuplexPipe()
	session := &serverSession{server: s, id: id, ctx: ctx, cancel: cancel, app: app, wire: wire, packets: make(map[uint64][]byte), wake: make(chan struct{}, 1), space: make(chan struct{})}
	s.active[session] = struct{}{}

	if id != "" {
		s.sessions[id] = session
	}

	session.joinTimer = time.AfterFunc(serverJoinTimeout, func() {
		session.mu.Lock()
		incomplete := !session.download || session.upload == ""
		session.mu.Unlock()
		if incomplete {
			session.close()
		}
	})
	return session
}

func (s *serverSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true

	count := len(s.packets)
	clear(s.packets)

	s.joinTimer.Stop()
	s.mu.Unlock()

	if count > 0 {
		s.server.budget.Release(int64(count * s.server.config.postSize))
	}

	s.cancel()
	s.app.Close()
	s.wire.Close()

	s.server.mu.Lock()
	delete(s.server.active, s)
	if s.server.sessions[s.id] == s {
		delete(s.server.sessions, s.id)
	}
	s.server.mu.Unlock()
}

func (s *serverSession) claimDownload() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.download {
		return false
	}
	s.download = true
	return true
}

func (s *serverSession) claimStream() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.upload != "" {
		return false
	}
	s.upload = modeStream
	close(s.space)
	s.space = make(chan struct{})
	return true
}

// The caller holds a buffer-budget reservation. Ownership transfers only on
// success; the queue releases it after consumption or session cancellation.
func (s *serverSession) enqueue(ctx context.Context, sequence uint64, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.closed || s.ctx.Err() != nil {
			return net.ErrClosed
		}
		if s.upload == modeStream || sequence < s.next {
			return errPacketConflict
		}
		if _, exists := s.packets[sequence]; exists {
			return errPacketConflict
		}
		if sequence-s.next < uint64(s.server.config.bufferedPosts) && len(s.packets) < s.server.config.bufferedPosts {
			break
		}

		// Wait for the sequence window to advance instead of terminating a
		// healthy upload when its downstream temporarily stops reading. Wake
		// every waiter: only some sequence numbers may now fit the window.
		space := s.space
		s.mu.Unlock()
		select {
		case <-space:
		case <-ctx.Done():
		case <-s.ctx.Done():
		}
		s.mu.Lock()
	}

	if s.upload == "" {
		s.upload = modePacket
		go s.consumePackets()
	}

	s.packets[sequence] = data

	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *serverSession) consumePackets() {
	defer s.close()
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}

		packet, exists := s.packets[s.next]
		gap := !exists && len(s.packets) > 0

		if exists {
			delete(s.packets, s.next)
			s.next++
			close(s.space)
			s.space = make(chan struct{})
		}
		s.mu.Unlock()

		if exists {
			_, err := s.wire.Write(packet)
			s.server.budget.Release(int64(s.server.config.postSize))
			if err != nil {
				return
			}
			continue
		}

		// An idle stream may wait indefinitely, but missing sequence numbers must
		// not pin a reassembly queue forever. Further out-of-order arrivals cannot
		// extend this deadline.
		var timer *time.Timer
		var timeout <-chan time.Time

		if gap {
			timer = time.NewTimer(serverJoinTimeout)
			timeout = timer.C
		}

		for {
			select {
			case <-s.ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-timeout:
				return
			case <-s.wake:
			}

			s.mu.Lock()
			_, ready := s.packets[s.next]
			pending := len(s.packets) > 0
			s.mu.Unlock()

			if ready {
				if timer != nil {
					timer.Stop()
				}
				break
			}
			if pending && timer == nil {
				timer = time.NewTimer(serverJoinTimeout)
				timeout = timer.C
			}
		}
	}
}

type inboundHTTPConn struct {
	net.Conn
	session       *serverSession
	source, local net.Addr
}

func (c *inboundHTTPConn) Close() error {
	c.session.close()
	return nil
}

func (c *inboundHTTPConn) RemoteAddr() net.Addr {
	return c.source
}

func (c *inboundHTTPConn) LocalAddr() net.Addr {
	return c.local
}

func (s *serverSession) copyUpload(body io.ReadCloser) {
	stop := context.AfterFunc(s.ctx, func() {
		body.Close()
	})
	defer stop()
	if _, err := io.Copy(s.wire, body); err != nil {
		s.close()
		return
	}
	// A clean upload EOF ends only the application's read side. The handler
	// may still need to generate a response or drain the download direction.
	s.wire.CloseWrite()
}
