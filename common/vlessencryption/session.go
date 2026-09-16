package vlessencryption

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	randv2 "math/rand/v2"
	"net"
	"strconv"
	"strings"
	"time"
)

// Limits apply before accepting early data. Saturation disables new tickets or
// rejects resumption; it never evicts replay entries for a still-valid ticket.
const (
	maxSessions       = 4096
	maxSessionReplays = 4096
	maxReplays        = 65536
)

type clientSession struct {
	ticket  [16]byte
	secret  [64]byte
	expires time.Time
}

type serverSession struct {
	secret  [64]byte
	expires time.Time
	used    map[[32]byte]struct{}
}

func parseLifetime(value string) (int, int, error) {
	parts := strings.Split(strings.TrimSuffix(value, "s"), "-")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("VLESS decryption: invalid ticket lifetime")
	}
	numbers := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 65535 {
			return 0, 0, fmt.Errorf("VLESS decryption: ticket lifetime must be 0–65535 seconds")
		}
		numbers[i] = n
	}
	from, to := numbers[0]/2, numbers[0]
	if len(numbers) == 2 {
		from, to = numbers[0], numbers[1]
	}
	if from > to {
		return 0, 0, fmt.Errorf("VLESS decryption: inverted ticket lifetime range")
	}
	return from, to, nil
}

func (c *Client) cachedSession() *clientSession {
	if !c.resume {
		return nil
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.session != nil && !time.Now().Before(c.session.expires) {
		c.session = nil
	}
	return c.session
}

func (c *Client) saveSession(ticket, secret []byte) {
	if !c.resume {
		return
	}
	seconds := binary.BigEndian.Uint16(ticket)
	if seconds == 0 {
		return
	}
	session := &clientSession{expires: time.Now().Add(time.Duration(seconds) * time.Second)}
	copy(session.ticket[:], ticket)
	copy(session.secret[:], secret)
	c.sessionMu.Lock()
	c.session = session
	c.sessionMu.Unlock()
}

func (c *Client) invalidateSession(session *clientSession) {
	c.sessionMu.Lock()
	if c.session == session {
		c.session = nil
	}
	c.sessionMu.Unlock()
}

// expireLocked scans only when the earliest ticket expires. There is no timer
// goroutine per inbound or session, and idle caches remain bounded.
func (s *Server) expireLocked(now time.Time) {
	if s.nextExpiry.IsZero() || now.Before(s.nextExpiry) {
		return
	}
	s.nextExpiry = time.Time{}
	for ticket, session := range s.sessions {
		if !now.Before(session.expires) {
			s.replayCount -= len(session.used)
			delete(s.sessions, ticket)
		} else if s.nextExpiry.IsZero() || session.expires.Before(s.nextExpiry) {
			s.nextExpiry = session.expires
		}
	}
}

func (s *Server) issueTicket(secret []byte) ([]byte, error) {
	ticket := make([]byte, 16)
	if _, err := rand.Read(ticket); err != nil {
		return nil, err
	}
	seconds := s.lifetimeFrom + randv2.IntN(s.lifetimeTo-s.lifetimeFrom+1)
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.closed {
		return nil, net.ErrClosed
	}
	now := time.Now()
	s.expireLocked(now)
	if len(s.sessions) >= maxSessions {
		seconds = 0
	}
	binary.BigEndian.PutUint16(ticket, uint16(seconds))
	if seconds > 0 {
		session := &serverSession{expires: now.Add(time.Duration(seconds) * time.Second), used: make(map[[32]byte]struct{})}
		copy(session.secret[:], secret)
		if s.sessions == nil {
			s.sessions = make(map[[16]byte]*serverSession)
		}
		id := [16]byte(ticket)
		if _, exists := s.sessions[id]; exists {
			return nil, fmt.Errorf("VLESS decryption: ticket collision")
		}
		s.sessions[id] = session
		if s.nextExpiry.IsZero() || session.expires.Before(s.nextExpiry) {
			s.nextExpiry = session.expires
		}
	}
	return ticket, nil
}

func (s *Server) consumeTicket(ticket, shared []byte) ([64]byte, error) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	now := time.Now()
	s.expireLocked(now)
	session := s.sessions[[16]byte(ticket)]
	if s.closed || session == nil {
		return [64]byte{}, fmt.Errorf("VLESS decryption: expired or unknown ticket")
	}
	key := [32]byte(shared)
	if _, exists := session.used[key]; exists {
		return [64]byte{}, fmt.Errorf("VLESS decryption: replay detected")
	}
	if len(session.used) >= maxSessionReplays || s.replayCount >= maxReplays {
		return [64]byte{}, fmt.Errorf("VLESS decryption: replay cache full")
	}
	session.used[key] = struct{}{}
	s.replayCount++
	return session.secret, nil
}

func (s *Server) resumeConnection(raw net.Conn, iv, shared []byte, initial *recordCipher) (*Conn, error) {
	encrypted := make([]byte, 32)
	if _, err := io.ReadFull(raw, encrypted); err != nil {
		return nil, err
	}
	ticket, err := initial.open(nil, encrypted, nil)
	if err != nil {
		return nil, err
	}
	pfs, err := s.consumeTicket(ticket, shared)
	if err != nil {
		// Xray invalidates a cached ticket on an invalid response header, not EOF.
		// A bounded random reply permits its next connection to use a full hello.
		// No application bytes have been exposed to VLESS at this point.
		noise := make([]byte, 1279)
		if _, randomErr := rand.Read(noise); randomErr == nil {
			noise[16] = 0 // invalid unmasked TLS record type after the 16-byte prefix
			_, _ = writeFull(raw, noise)
		}
		return nil, err
	}
	secret := append(append([]byte(nil), pfs[:]...), shared...)
	prefix := make([]byte, 16)
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	conn := &Conn{
		Conn: raw, secret: secret, preWrite: prefix,
		send:    newRecordCipherWithAlgorithm(prefix, secret, initial.chacha),
		receive: newRecordCipherWithAlgorithm(encrypted, secret, initial.chacha),
	}
	if s.mode == modeRandom {
		conn.sendMask = newMask(secret, prefix)
		conn.receiveMask = newMask(secret, iv)
	}
	return conn, nil
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.sessionMu.Lock()
	s.closed = true
	s.sessions = nil
	s.replayCount = 0
	s.sessionMu.Unlock()
	return nil
}
