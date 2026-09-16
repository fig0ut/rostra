package v2rayxhttp

import (
	"errors"
	"net"
	"time"
)

// duplexPipe uses a separate net.Pipe for each direction. Closing one writer
// delivers EOF to its peer without closing the reverse direction. net.Pipe
// retains unbuffered backpressure, concurrent I/O, and per-direction deadlines.
type duplexPipe struct {
	reader net.Conn
	writer net.Conn
}

func newDuplexPipe() (*duplexPipe, *duplexPipe) {
	uploadReader, uploadWriter := net.Pipe()
	downloadReader, downloadWriter := net.Pipe()
	return &duplexPipe{reader: uploadReader, writer: downloadWriter},
		&duplexPipe{reader: downloadReader, writer: uploadWriter}
}

func (p *duplexPipe) Read(b []byte) (int, error) {
	return p.reader.Read(b)
}

func (p *duplexPipe) Write(b []byte) (int, error) {
	return p.writer.Write(b)
}

func (p *duplexPipe) CloseRead() error {
	return p.reader.Close()
}

func (p *duplexPipe) CloseWrite() error {
	return p.writer.Close()
}

func (p *duplexPipe) Close() error {
	return errors.Join(p.CloseRead(), p.CloseWrite())
}

func (p *duplexPipe) LocalAddr() net.Addr {
	return p.reader.LocalAddr()
}

func (p *duplexPipe) RemoteAddr() net.Addr {
	return p.reader.RemoteAddr()
}

func (p *duplexPipe) SetReadDeadline(t time.Time) error {
	return p.reader.SetReadDeadline(t)
}

func (p *duplexPipe) SetWriteDeadline(t time.Time) error {
	return p.writer.SetWriteDeadline(t)
}

func (p *duplexPipe) SetDeadline(t time.Time) error {
	return errors.Join(p.SetReadDeadline(t), p.SetWriteDeadline(t))
}
