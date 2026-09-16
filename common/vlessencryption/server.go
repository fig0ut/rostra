package vlessencryption

import (
	"context"
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	randv2 "math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"
)

// Server keeps immutable key material and a bounded, expiring ticket cache.
type Server struct {
	x25519                   *ecdh.PrivateKey
	kem                      *mlkem.DecapsulationKey768
	mode                     wireMode
	public                   []byte
	lifetimeFrom, lifetimeTo int
	sessionMu                sync.Mutex
	sessions                 map[[16]byte]*serverSession
	nextExpiry               time.Time
	replayCount              int
	closed                   bool
}

func NewServer(configuration string) (*Server, error) {
	if configuration == "" || configuration == "none" {
		return nil, nil
	}
	fields := strings.Split(configuration, ".")
	if len(fields) != 4 || fields[0] != "mlkem768x25519plus" {
		return nil, fmt.Errorf("VLESS decryption: expected mlkem768x25519plus.<native|xorpub|random>.<seconds>s.<private-key>; relay chains and custom padding are not implemented")
	}
	mode, err := parseWireMode(fields[1])
	if err != nil {
		return nil, err
	}
	from, to, err := parseLifetime(fields[2])
	if err != nil {
		return nil, err
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(fields[3])
	if err != nil {
		return nil, fmt.Errorf("VLESS decryption: invalid private-key encoding")
	}
	s := &Server{mode: mode, lifetimeFrom: from, lifetimeTo: to}
	switch len(key) {
	case 32:
		s.x25519, err = ecdh.X25519().NewPrivateKey(key)
	case 64:
		s.kem, err = mlkem.NewDecapsulationKey768(key)
	default:
		return nil, fmt.Errorf("VLESS decryption: private key must be a 32-byte X25519 key or 64-byte ML-KEM-768 seed")
	}
	if err != nil {
		return nil, fmt.Errorf("VLESS decryption: invalid private key")
	}
	if s.x25519 != nil {
		s.public = s.x25519.PublicKey().Bytes()
	} else {
		s.public = s.kem.EncapsulationKey().Bytes()
	}
	return s, nil
}

func (s *Server) Handshake(ctx context.Context, raw net.Conn) (net.Conn, error) {
	return handshakeContext(ctx, raw, s.handshake)
}

func (s *Server) handshake(raw net.Conn) (*Conn, error) {
	exchangeSize := 32
	if s.kem != nil {
		exchangeSize = 1088
	}
	prefix := make([]byte, 16+exchangeSize)
	if _, err := io.ReadFull(raw, prefix); err != nil {
		return nil, err
	}
	iv, exchange := prefix[:16], prefix[16:]
	if s.mode != modeNative {
		newMask(s.public, iv).XORKeyStream(exchange, exchange)
	}
	var shared []byte
	var err error
	if s.x25519 != nil {
		if exchange[31]&128 != 0 {
			return nil, fmt.Errorf("noncanonical X25519 exchange")
		}
		peer, err := ecdh.X25519().NewPublicKey(exchange)
		if err != nil {
			return nil, err
		}
		shared, err = s.x25519.ECDH(peer)
		if err != nil {
			return nil, err
		}
	} else {
		shared, err = s.kem.Decapsulate(exchange)
		if err != nil {
			return nil, err
		}
	}
	// There is no cipher flag: the first authenticated length selects AES-GCM
	// or ChaCha20-Poly1305. A failed probe must not advance the selected counter.
	encryptedLength := make([]byte, 18)
	if _, err := io.ReadFull(raw, encryptedLength); err != nil {
		return nil, err
	}
	initial := newRecordCipher(iv, shared)
	length, err := initial.open(nil, encryptedLength, nil)
	if err != nil {
		initial = newRecordCipherWithAlgorithm(iv, shared, true)
		length, err = initial.open(nil, encryptedLength, nil)
		if err != nil {
			return nil, fmt.Errorf("authenticate client hello: %w", err)
		}
	}
	if binary.BigEndian.Uint16(length) == 32 {
		return s.resumeConnection(raw, iv, shared, initial)
	}
	if binary.BigEndian.Uint16(length) != 1184+32+16 {
		return nil, fmt.Errorf("unsupported client key exchange length")
	}
	public, err := readHandshakeValue(raw, initial, 1184+32+16)
	if err != nil {
		return nil, err
	}
	encapsulation, err := mlkem.NewEncapsulationKey768(public[:1184])
	if err != nil {
		return nil, err
	}
	peerDH, err := ecdh.X25519().NewPublicKey(public[1184:])
	if err != nil {
		return nil, err
	}
	dh, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	dhSecret, err := dh.ECDH(peerDH)
	if err != nil {
		return nil, err
	}
	kemSecret, ciphertext := encapsulation.Encapsulate()
	serverPublic := append(ciphertext, dh.PublicKey().Bytes()...)
	secret := make([]byte, 0, 96)
	secret = append(secret, kemSecret...)
	secret = append(secret, dhSecret...)
	secret = append(secret, shared...)
	conn := &Conn{
		Conn: raw, secret: secret,
		send:    newRecordCipherWithAlgorithm(serverPublic, secret, initial.chacha),
		receive: newRecordCipherWithAlgorithm(public, secret, initial.chacha),
	}
	// Consume and authenticate the complete client hello before exposing it to
	// VLESS. Each allocation is bounded by the authenticated 16-bit length.
	length, err = readHandshakeValue(raw, initial, 18)
	if err != nil {
		return nil, err
	}
	paddingSize := int(binary.BigEndian.Uint16(length))
	if paddingSize < 16 {
		return nil, fmt.Errorf("invalid client padding length")
	}
	if _, err := readHandshakeValue(raw, initial, paddingSize); err != nil {
		return nil, err
	}

	var reserved [12]byte
	for i := range reserved {
		reserved[i] = 255
	}
	reply := initial.aead.Seal(nil, reserved[:], serverPublic, nil)
	ticket, err := s.issueTicket(secret[:64])
	if err != nil {
		return nil, err
	}
	reply = conn.send.seal(reply, ticket, nil)
	padding := make([]byte, 111+randv2.IntN(1001)-34)
	if _, err := rand.Read(padding); err != nil {
		return nil, err
	}
	reply = appendPair(reply, conn.send, padding)
	if _, err := writeFull(raw, reply); err != nil {
		return nil, err
	}
	if s.mode == modeRandom {
		conn.sendMask = newMask(secret, ticket)
		conn.receiveMask = newMask(secret, iv)
	}
	return conn, nil
}
