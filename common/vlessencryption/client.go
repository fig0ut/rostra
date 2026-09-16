// Package vlessencryption implements Xray's VLESS encryption
// wire protocol. It is independent of the VLESS request codec and transport.
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

// Client keeps decoded server keys and at most one authenticated session ticket.
// Resumed connections still perform a fresh static key exchange.
type Client struct {
	x25519    *ecdh.PublicKey
	kem       *mlkem.EncapsulationKey768
	mode      wireMode
	public    []byte
	resume    bool
	sessionMu sync.Mutex
	session   *clientSession
}

// NewClient validates the single-key profile before any network operation.
func NewClient(configuration string) (*Client, error) {
	if configuration == "" || configuration == "none" {
		return nil, nil
	}
	fields := strings.Split(configuration, ".")
	if len(fields) != 4 || fields[0] != "mlkem768x25519plus" || (fields[2] != "1rtt" && fields[2] != "0rtt") {
		return nil, fmt.Errorf("VLESS encryption: expected mlkem768x25519plus.<native|xorpub|random>.<1rtt|0rtt>.<public-key>; relay chains and custom padding are not implemented")
	}
	mode, err := parseWireMode(fields[1])
	if err != nil {
		return nil, err
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(fields[3])
	if err != nil {
		return nil, fmt.Errorf("VLESS encryption: invalid public key: %w", err)
	}
	c := &Client{mode: mode, public: key, resume: fields[2] == "0rtt"}
	switch len(key) {
	case 32:
		c.x25519, err = ecdh.X25519().NewPublicKey(key)
		if err == nil {
			// A fixed validation scalar detects low-order public keys at
			// initialization. Actual handshakes always generate fresh keys.
			scalar := [32]byte{1}
			probe, _ := ecdh.X25519().NewPrivateKey(scalar[:])
			_, err = probe.ECDH(c.x25519)
		}
	case 1184:
		c.kem, err = mlkem.NewEncapsulationKey768(key)
	default:
		return nil, fmt.Errorf("VLESS encryption: public key must be 32 or 1184 bytes")
	}
	if err != nil {
		return nil, fmt.Errorf("VLESS encryption: invalid public key: %w", err)
	}
	return c, nil
}

// Handshake takes ownership of raw, closing it on failure or cancellation.
// Deadline and cancellation handling end before ownership passes to the caller.
func (c *Client) Handshake(ctx context.Context, raw net.Conn) (net.Conn, error) {
	return handshakeContext(ctx, raw, c.handshake)
}

func handshakeContext(ctx context.Context, raw net.Conn, handshake func(net.Conn) (*Conn, error)) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		raw.Close()
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		raw.Close()
		return nil, err
	}
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { raw.Close(); close(cancelled) })
	conn, err := handshake(raw)
	if !stop() {
		<-cancelled
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = raw.SetDeadline(time.Time{})
	}
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("VLESS encryption handshake: %w", err)
	}
	return conn, nil
}

func (c *Client) handshake(raw net.Conn) (*Conn, error) {
	var shared, exchange []byte
	if c.x25519 != nil {
		ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		shared, err = ephemeral.ECDH(c.x25519)
		if err != nil {
			return nil, err
		}
		exchange = ephemeral.PublicKey().Bytes()
	} else {
		shared, exchange = c.kem.Encapsulate()
	}
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	if c.mode != modeNative {
		newMask(c.public, iv).XORKeyStream(exchange, exchange)
	}
	initial := newRecordCipher(iv, shared)
	if session := c.cachedSession(); session != nil {
		hello := append(iv, exchange...)
		hello = appendPair(hello, initial, session.ticket[:])
		secret := append(append([]byte(nil), session.secret[:]...), shared...)
		conn := &Conn{
			Conn: raw, secret: secret, send: newRecordCipher(hello[len(hello)-32:], secret), preWrite: hello,
			invalidate: func() { c.invalidateSession(session) }, random: c.mode == modeRandom,
		}
		if conn.random {
			conn.sendMask = newMask(secret, iv)
		}
		return conn, nil
	}
	kem, err := mlkem.GenerateKey768()
	if err != nil {
		return nil, err
	}
	dh, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	public := append(kem.EncapsulationKey().Bytes(), dh.PublicKey().Bytes()...)

	// The handshake has encrypted length/value pairs. Length includes the
	// authentication tag. Padding is sent with the hello, without timer goroutines.
	hello := append(iv, exchange...)
	hello = appendPair(hello, initial, public)
	// The total padding pair occupies 111–1111 bytes, including its
	// encrypted two-byte length and the two authentication tags.
	paddingSize := 111 + randv2.IntN(1001)
	padding := make([]byte, paddingSize-(2+2*recordTagSize))
	if _, err := rand.Read(padding); err != nil {
		return nil, err
	}
	hello = appendPair(hello, initial, padding)
	if _, err := writeFull(raw, hello); err != nil {
		return nil, err
	}

	reply := make([]byte, 1088+32+16)
	if _, err := io.ReadFull(raw, reply); err != nil {
		return nil, err
	}
	// The server key-exchange reply uses a reserved nonce, independently of the
	// client's length/value counter. Authenticate it before parsing any key.
	var replyNonce [12]byte
	for i := range replyNonce {
		replyNonce[i] = 255
	}
	peer, err := initial.aead.Open(reply[:0], replyNonce[:], reply, nil)
	if err != nil {
		return nil, fmt.Errorf("authenticate server key exchange: %w", err)
	}
	kemSecret, err := kem.Decapsulate(peer[:1088])
	if err != nil {
		return nil, err
	}
	peerDH, err := ecdh.X25519().NewPublicKey(peer[1088:])
	if err != nil {
		return nil, err
	}
	dhSecret, err := dh.ECDH(peerDH)
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 0, 96)
	secret = append(secret, kemSecret...)
	secret = append(secret, dhSecret...)
	secret = append(secret, shared...)
	conn := &Conn{Conn: raw, secret: secret, send: newRecordCipher(public, secret), receive: newRecordCipher(peer, secret)}
	// Publish the ticket only after the entire reply has authenticated.
	ticket, err := readHandshakeValue(raw, conn.receive, 32)
	if err != nil {
		return nil, err
	}
	length, err := readHandshakeValue(raw, conn.receive, 18)
	if err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(length))
	if size < 16 {
		return nil, fmt.Errorf("invalid server padding length %d", size)
	}
	if _, err := readHandshakeValue(raw, conn.receive, size); err != nil {
		return nil, err
	}
	if c.mode == modeRandom {
		conn.sendMask = newMask(secret, iv)
		conn.receiveMask = newMask(secret, ticket)
	}
	c.saveSession(ticket, secret[:64])
	return conn, nil
}

func appendPair(dst []byte, state *recordCipher, value []byte) []byte {
	length := [2]byte{}
	binary.BigEndian.PutUint16(length[:], uint16(len(value)+16))
	dst = state.seal(dst, length[:], nil)
	return state.seal(dst, value, nil)
}

func readHandshakeValue(conn net.Conn, state *recordCipher, size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(conn, value); err != nil {
		return nil, err
	}
	return state.open(value[:0], value, nil)
}
