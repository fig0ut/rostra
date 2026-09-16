package v2rayxhttp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var (
	_ adapter.V2RayMultiplexClientTransport = (*Client)(nil)
	_ adapter.IdleConnectionKeeper          = (*Client)(nil)
)

type Client struct {
	ctx            context.Context
	config         clientConfig
	pool           *clientPool
	downloadConfig *clientConfig
	downloadPool   *clientPool
	mu             sync.Mutex
	streams        map[*streamConn]struct{}
}

func NewClient(ctx context.Context, rawDialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayXHTTPOptions, tlsConfig tls.Config) (*Client, error) {
	config, err := newConfig(options, serverAddr, tlsConfig)
	if err != nil {
		return nil, err
	}

	factory, err := transportFactory(rawDialer, serverAddr, config.version, tlsConfig, config.pool.keepAlive)
	if err != nil {
		return nil, err
	}

	client := &Client{
		ctx:     ctx,
		config:  config,
		pool:    newClientPool(config.pool, factory),
		streams: make(map[*streamConn]struct{}),
	}
	if options.DownloadSettings != nil {
		download, pool, err := newDownload(ctx, rawDialer, *options.DownloadSettings)
		if err != nil {
			return nil, fmt.Errorf("xhttp: download_settings: %w", err)
		}
		client.downloadConfig, client.downloadPool = &download, pool
	}
	return client, nil
}

func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}

	session := ""
	if c.config.mode != modeSingle {
		id, err := c.config.metadata.newSessionID()
		if err != nil {
			return nil, err
		}
		session = id
	}

	// DialContext returns a lazy connection so the application can supply the
	// first upload bytes. Its lifetime must outlive the caller's dial context.
	conn, streamCtx := newStream(context.WithoutCancel(ctx), c.config.server)
	// A stream owns its cancellation; transport Close resets all existing streams.
	c.mu.Lock()
	uploadLease := c.pool.acquire()
	downloadLease := uploadLease
	if c.downloadPool != nil {
		downloadLease = c.downloadPool.acquire()
	}
	c.streams[conn] = struct{}{}
	conn.onClose = func() {
		c.mu.Lock()
		delete(c.streams, conn)
		c.mu.Unlock()
		uploadLease.Close()
		if downloadLease != uploadLease {
			downloadLease.Close()
		}
	}
	c.mu.Unlock()

	stop := context.AfterFunc(c.ctx, func() {
		conn.finish(c.ctx.Err())
	})

	go func() {
		<-streamCtx.Done()
		stop()
		conn.finish(streamCtx.Err())
	}()

	setupTimeout := C.TCPTimeout
	if deadline, ok := ctx.Deadline(); ok {
		setupTimeout = time.Until(deadline)
	}

	setupCtx, cancelSetup := context.WithTimeout(streamCtx, setupTimeout)
	context.AfterFunc(setupCtx, func() {
		if setupCtx.Err() == context.DeadlineExceeded {
			conn.finish(os.ErrDeadlineExceeded)
		}
	})

	var pending atomic.Int32
	pending.Store(1)
	if c.config.mode == modeStream {
		pending.Store(2)
	}
	ready := func() {
		// Both directions of stream-up must receive successful headers. The
		// packet-up GET establishes the download; POSTs are sent as data arrives.
		if pending.Add(-1) == 0 {
			cancelSetup()
		}
	}

	if c.config.mode == modeSingle {
		go c.stream(streamCtx, conn, uploadLease, session, conn.wire, false, ready)
	} else {
		go c.stream(streamCtx, conn, downloadLease, session, nil, false, ready)
		if c.config.mode == modeStream {
			go c.stream(streamCtx, conn, uploadLease, session, conn.wire, true, ready)
		} else {
			go c.uploadPackets(streamCtx, conn, uploadLease, session)
		}
	}
	return conn, nil
}

func (c *Client) stream(ctx context.Context, conn *streamConn, lease *poolLease, session string, body io.Reader, upload bool, ready func()) {
	config := &c.config
	if body == nil && c.downloadConfig != nil {
		config = c.downloadConfig
	}
	req := config.newRequest(ctx, session, "", body, nil)
	response, err := lease.RoundTrip(req)
	if err != nil {
		conn.finish(err)
		return
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		conn.finish(fmt.Errorf("xhttp: unexpected HTTP status: %s", response.Status))
		return
	}

	ready()

	var destination io.Writer = conn.wire
	if upload {
		destination = io.Discard
	}

	_, err = io.Copy(destination, response.Body)

	if upload && err == nil {
		// The upload response can end before the independent download has
		// drained. Let the download close the connection after its final bytes.
		return
	}
	if err == nil {
		err = io.EOF
	}

	conn.finish(err)
}

func (c *Client) MultiplexEnabled() bool {
	return c.config.version != "1.1"
}

func (c *Client) SetKeepIdleConnections(keep bool) {
	c.pool.setKeepIdle(keep)
	if c.downloadPool != nil {
		c.downloadPool.setKeepIdle(keep)
	}
}

func (c *Client) CloseIdleConnections() {
	c.pool.closeIdle()
	if c.downloadPool != nil {
		c.downloadPool.closeIdle()
	}
}

func (c *Client) Close() error {
	c.mu.Lock()
	streams := make([]*streamConn, 0, len(c.streams))
	for s := range c.streams {
		streams = append(streams, s)
	}
	c.mu.Unlock()
	for _, s := range streams {
		s.Close()
	}
	c.pool.reset()
	if c.downloadPool != nil {
		c.downloadPool.reset()
	}
	return nil
}
