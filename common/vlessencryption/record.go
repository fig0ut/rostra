package vlessencryption

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
	"lukechampine.com/blake3"
)

const (
	recordHeaderSize = 5
	recordTagSize    = 16
	maxRecordSize    = 16640
	writeChunkSize   = 8192
)

type recordCipher struct {
	aead   cipher.AEAD
	chacha bool
	nonce  [12]byte
}

func newRecordCipher(context, secret []byte) *recordCipher {
	return newRecordCipherWithAlgorithm(context, secret, false)
}

func newRecordCipherWithAlgorithm(context, secret []byte, chacha bool) *recordCipher {
	var key [32]byte
	blake3.DeriveKey(key[:], string(context), secret)
	if chacha {
		aead, _ := chacha20poly1305.New(key[:])
		return &recordCipher{aead: aead, chacha: true}
	}
	// AES-256-GCM is always supported by the protocol. The server detects the
	// cipher from the authenticated first length; hardware support is optional.
	block, _ := aes.NewCipher(key[:])
	aead, _ := cipher.NewGCM(block)
	return &recordCipher{aead: aead}
}

func (c *recordCipher) nextNonce() []byte {
	for i := len(c.nonce) - 1; i >= 0; i-- {
		c.nonce[i]++
		if c.nonce[i] != 0 {
			break
		}
	}
	return c.nonce[:]
}

func (c *recordCipher) seal(dst, plaintext, additional []byte) []byte {
	return c.aead.Seal(dst, c.nextNonce(), plaintext, additional)
}

func (c *recordCipher) open(dst, ciphertext, additional []byte) ([]byte, error) {
	return c.aead.Open(dst, c.nextNonce(), ciphertext, additional)
}

func (c *recordCipher) atLimit() bool {
	for _, b := range c.nonce {
		if b != 255 {
			return false
		}
	}
	return true
}

// Conn deliberately exposes no unwrapping or raw-copy interface: callers must
// not bypass encryption. Each direction owns its lock, buffer, and cipher state.
type Conn struct {
	net.Conn
	secret                           []byte
	send, receive                    *recordCipher
	writeMu, readMu                  sync.Mutex
	writeBuffer, readBuffer, pending []byte
	writeErr, readErr                error
	preWrite                         []byte
	sendMask, receiveMask            cipher.Stream
	random                           bool
	invalidate                       func()
	confirmed                        atomic.Bool
}

func (c *Conn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if c.writeBuffer == nil {
		c.writeBuffer = make([]byte, recordHeaderSize+writeChunkSize+recordTagSize)
	}
	written := 0
	for len(p) > 0 {
		size := min(len(p), writeChunkSize)
		header := c.writeBuffer[:recordHeaderSize]
		header[0], header[1], header[2] = 23, 3, 3
		binary.BigEndian.PutUint16(header[3:], uint16(size+recordTagSize))
		rotate := c.send.atLimit()
		record := c.send.seal(header, p[:size], header)
		if rotate {
			c.send = newRecordCipherWithAlgorithm(record, c.secret, c.send.chacha)
		}
		if c.sendMask != nil {
			c.sendMask.XORKeyStream(record[:recordHeaderSize], record[:recordHeaderSize])
		}
		if c.preWrite != nil {
			record = append(c.preWrite, record...)
			c.preWrite = nil
		}
		sent, err := writeFull(c.Conn, record)
		if sent == len(record) {
			written += size
		}
		if err != nil {
			c.writeErr = err
			// A failed early write may indicate a rejected ticket. Do not replay data.
			if c.invalidate != nil && !c.confirmed.Load() {
				c.invalidate()
			}
			c.Conn.Close()
			return written, err
		}
		p = p[size:]
	}
	return written, nil
}

func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.readErr != nil {
		return 0, c.readErr
	}
	if len(c.pending) == 0 {
		if err := c.readRecord(); err != nil {
			c.readErr = err
			if c.invalidate != nil && !c.confirmed.Load() {
				c.invalidate()
			}
			c.Conn.Close()
			return 0, err
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *Conn) readRecord() error {
	if c.receive == nil {
		var prefix [16]byte
		if _, err := io.ReadFull(c.Conn, prefix[:]); err != nil {
			return err
		}
		c.receive = newRecordCipher(prefix[:], c.secret)
		if c.random {
			c.receiveMask = newMask(c.secret, prefix[:])
		}
	}
	var header [recordHeaderSize]byte
	if _, err := io.ReadFull(c.Conn, header[:]); err != nil {
		return err
	}
	if c.receiveMask != nil {
		c.receiveMask.XORKeyStream(header[:], header[:])
	}
	size := int(binary.BigEndian.Uint16(header[3:]))
	if header[0] != 23 || header[1] != 3 || header[2] != 3 || size <= recordTagSize || size > maxRecordSize {
		return fmt.Errorf("VLESS encryption: invalid record header")
	}
	if cap(c.readBuffer) < size {
		c.readBuffer = make([]byte, size)
	}
	data := c.readBuffer[:size]
	if _, err := io.ReadFull(c.Conn, data); err != nil {
		return err
	}
	var next *recordCipher
	if c.receive.atLimit() {
		// Rekey context includes ciphertext; derive before in-place decryption.
		context := make([]byte, recordHeaderSize+size)
		copy(context, header[:])
		copy(context[recordHeaderSize:], data)
		next = newRecordCipherWithAlgorithm(context, c.secret, c.receive.chacha)
	}
	plaintext, err := c.receive.open(data[:0], data, header[:])
	if err != nil {
		return fmt.Errorf("VLESS encryption: authenticate record: %w", err)
	}
	if next != nil {
		c.receive = next
	}
	c.confirmed.Store(true)
	c.pending = plaintext
	return nil
}

func writeFull(writer io.Writer, p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n, err := writer.Write(p)
		if n < 0 || n > len(p) {
			return written, io.ErrShortWrite
		}
		written += n
		p = p[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}
