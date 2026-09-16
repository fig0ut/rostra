package v2rayxhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/sagernet/sing/common/baderror"
)

// A pipe provides actual read/write deadlines and bounded backpressure, without
// applying a logical stream's deadline to a shared HTTP/2 or HTTP/3 connection.
type streamConn struct {
	net.Conn
	wire    net.Conn
	cancel  context.CancelFunc
	once    sync.Once
	mu      sync.Mutex
	err     error
	remote  net.Addr
	onClose func()
}

func newStream(ctx context.Context, remote net.Addr) (*streamConn, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	app, wire := net.Pipe()
	c := &streamConn{Conn: app, wire: wire, cancel: cancel, remote: remote}
	return c, ctx
}

func (c *streamConn) finish(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = baderror.WrapH2(err)
		c.mu.Unlock()

		c.cancel()
		c.wire.Close()
		c.Conn.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
}

func (c *streamConn) Close() error {
	c.finish(net.ErrClosed)
	return nil
}

func (c *streamConn) RemoteAddr() net.Addr {
	return c.remote
}

func (c *streamConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	return n, c.operationError(err)
}

func (c *streamConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	return n, c.operationError(err)
}

func (c *streamConn) operationError(err error) error {
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.err != nil {
			return c.err
		}
	}
	return err
}
