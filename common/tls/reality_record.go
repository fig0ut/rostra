//go:build with_utls

package tls

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/fig0ut/rostra/adapter"

	utls "github.com/metacubex/utls"
)

const (
	realityProbeTimeout     = 10 * time.Second
	realityRecordWindow     = 5 * time.Second
	realityRecordTTL        = time.Hour
	realityRecordRetry      = time.Minute
	realityMaxProbeRecords  = 16
	realityMaxRecordPayload = 16384 + 256
)

type realityRecordKey struct {
	serverName string
	profile    int
}

type realityRecordEntry struct {
	done    chan struct{}
	lengths []int
	expires time.Time
}

// Each cache belongs to one server configuration and its target/dialer. Clones
// get their own cache so closing or changing one config cannot affect another.
type realityRecordCache struct {
	ctx     context.Context
	cancel  context.CancelFunc
	config  *utls.RealityConfig
	mu      sync.Mutex
	entries map[realityRecordKey]*realityRecordEntry
}

func newRealityRecordCache(ctx context.Context, config *utls.RealityConfig) *realityRecordCache {
	ctx, cancel := context.WithCancel(ctx)
	return &realityRecordCache{ctx: ctx, cancel: cancel, config: config, entries: make(map[realityRecordKey]*realityRecordEntry)}
}

func realityRecordProfile(protocols []string) int {
	if len(protocols) == 0 {
		return 0
	}
	if protocols[0] == "h2" {
		return 2
	}
	return 1
}

func (c *realityRecordCache) start(key realityRecordKey) *realityRecordEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry != nil && (entry.expires.IsZero() || time.Now().Before(entry.expires)) {
		return entry
	}
	entry = &realityRecordEntry{done: make(chan struct{})}
	c.entries[key] = entry
	go func() {
		lengths, err := c.probe(key)
		ttl := realityRecordTTL
		if err != nil {
			ttl = realityRecordRetry
			lengths = nil
			if c.config.Log != nil && c.ctx.Err() == nil {
				c.config.Log("REALITY post-handshake probe for %s (profile %d): %v", key.serverName, key.profile, err)
			}
		}
		c.mu.Lock()
		entry.lengths = lengths
		entry.expires = time.Now().Add(ttl)
		close(entry.done)
		c.mu.Unlock()
	}()
	return entry
}

func (c *realityRecordCache) get(ctx context.Context, serverName string, protocols []string) []int {
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return nil
	}
	entry := c.start(realityRecordKey{serverName, realityRecordProfile(protocols)})
	select {
	case <-entry.done:
		return entry.lengths
	case <-ctx.Done():
		return nil
	case <-c.ctx.Done():
		return nil
	}
}

func (c *realityRecordCache) warm(serverName string) {
	if c.ctx.Err() == nil {
		for profile := range 3 {
			c.start(realityRecordKey{serverName, profile})
		}
	}
}

func (c *realityRecordCache) probe(key realityRecordKey) ([]int, error) {
	ctx, cancel := context.WithTimeout(c.ctx, realityProbeTimeout)
	defer cancel()
	conn, err := c.config.DialContext(ctx, c.config.Type, c.config.Dest)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := realityCloseOnCancel(ctx, conn)
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	fingerprint := utls.HelloGolang
	var protocols []string
	switch key.profile {
	case 1:
		protocols = []string{"http/1.1"}
	case 2:
		protocols = []string{"h2", "http/1.1"}
		fingerprint = utls.HelloChrome_Auto
	}
	// Return at most one record at a time during the handshake. Otherwise TLS
	// read-ahead can hide post-handshake records in the client's private buffer.
	framed := &realityProbeConn{Conn: conn}
	client := utls.UClient(framed, &utls.Config{
		ServerName: key.serverName,
		NextProtos: protocols,
		MinVersion: utls.VersionTLS13,
		RootCAs:    adapter.RootPoolFromContext(c.ctx),
		Time:       c.config.Time,
	}, fingerprint)
	if err := client.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	if client.ConnectionState().Version != utls.VersionTLS13 {
		return nil, errors.New("target did not negotiate TLS 1.3")
	}
	if window := time.Now().Add(realityRecordWindow); window.Before(deadline) {
		deadline = window
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	return readRealityRecordLengths(conn)
}

// readRealityRecordLengths collects complete encrypted records only, with
// bounded memory and count. A timeout between records ends the observation;
// truncation, invalid framing, or excess records discard the sample.
func readRealityRecordLengths(conn net.Conn) ([]int, error) {
	var lengths []int
	var header [5]byte
	for {
		n, err := io.ReadFull(conn, header[:])
		if err != nil {
			var netErr net.Error
			if n == 0 && (errors.Is(err, io.EOF) || (errors.As(err, &netErr) && netErr.Timeout())) {
				return lengths, nil
			}
			return nil, err
		}
		length := int(binary.BigEndian.Uint16(header[3:]))
		if header[0] != 23 || header[1] != 3 || header[2] != 3 || length < 17 || length > 16384+1+16 {
			return nil, errors.New("invalid post-handshake record")
		}
		if len(lengths) == realityMaxProbeRecords {
			return nil, errors.New("too many post-handshake records")
		}
		if _, err := io.CopyN(io.Discard, conn, int64(length)); err != nil {
			return nil, err
		}
		lengths = append(lengths, length+5)
	}
}

type realityProbeConn struct {
	net.Conn
	header    [5]byte
	pending   []byte
	remaining int
}

func (c *realityProbeConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if len(c.pending) == 0 && c.remaining == 0 {
		if _, err := io.ReadFull(c.Conn, c.header[:]); err != nil {
			return 0, err
		}
		c.remaining = int(binary.BigEndian.Uint16(c.header[3:]))
		if c.remaining > realityMaxRecordPayload {
			return 0, errors.New("oversized target TLS record")
		}
		c.pending = c.header[:]
	}
	if len(c.pending) != 0 {
		n := copy(b, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	n, err := c.Conn.Read(b[:min(len(b), c.remaining)])
	c.remaining -= n
	return n, err
}
