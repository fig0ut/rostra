package v2rayxhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/sync/semaphore"
)

var _ adapter.V2RayServerTransport = (*Server)(nil)

type packetHTTPServer interface {
	Serve(net.PacketConn) error
	Close() error
}

type Server struct {
	ctx         context.Context
	cancel      context.CancelFunc
	config      serverConfig
	tls         tls.ServerConfig
	handler     adapter.V2RayServerTransportHandler
	http        *http.Server
	http3       packetHTTPServer
	budget      *semaphore.Weighted
	mu          sync.Mutex
	closed      bool
	sessions    map[string]*serverSession
	active      map[*serverSession]struct{}
	connections map[*trackedHTTPConn]struct{}
}

func NewServer(ctx context.Context, _ logger.ContextLogger, options option.V2RayXHTTPOptions, tc tls.ServerConfig, handler adapter.V2RayServerTransportHandler) (*Server, error) {
	config, err := newServerConfig(options)
	if err != nil {
		return nil, err
	}
	if config.version == "" && tc != nil {
		if alpn := tc.NextProtos(); len(alpn) == 1 && alpn[0] == "h3" {
			config.version = "3"
		}
	}

	ctx, cancel := context.WithCancel(ctx)

	s := &Server{
		ctx: ctx, cancel: cancel, config: config, tls: tc, handler: handler,
		budget: semaphore.NewWeighted(serverBufferBudget), sessions: make(map[string]*serverSession),
		active: make(map[*serverSession]struct{}), connections: make(map[*trackedHTTPConn]struct{}),
	}

	if config.version == "3" {
		s.http3, err = newHTTP3Server(s, tc)
		if err != nil {
			cancel()
			return nil, err
		}
	} else {
		if tc != nil {
			alpn := []string{"h2", "http/1.1"}
			switch config.version {
			case "1.1":
				alpn = []string{"http/1.1"}
			case "2":
				alpn = []string{"h2"}
			}
			tc.SetNextProtos(alpn)
		}

		// net/http sees an ordinary byte stream after our TLS/REALITY
		// wrapper. Use Go's native prior-knowledge HTTP/2 support, avoiding
		// the legacy h2c upgrade handler's eager first-request body buffering.
		protocols := new(http.Protocols)
		protocols.SetHTTP1(config.version != "2")
		protocols.SetUnencryptedHTTP2(config.version != "1.1")

		s.http = &http.Server{
			Handler:           s,
			Protocols:         protocols,
			ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: config.maxHeaderBytes,
			BaseContext: func(net.Listener) context.Context {
				return ctx
			},
		}
	}
	return s, nil
}

func (s *Server) Network() []string {
	if s.http3 != nil {
		return []string{N.NetworkUDP}
	}
	return []string{N.NetworkTCP}
}

func (s *Server) Serve(listener net.Listener) error {
	if s.http == nil {
		listener.Close()
		return fmt.Errorf("xhttp: this server requires UDP")
	}

	err := s.http.Serve(&trackedHTTPListener{Listener: listener, server: s})

	if errors.Is(err, http.ErrServerClosed) {
		return net.ErrClosed
	}
	return err
}

func (s *Server) ServePacket(conn net.PacketConn) error {
	if s.http3 == nil {
		conn.Close()
		return fmt.Errorf("xhttp: this server requires TCP")
	}

	err := s.http3.Serve(conn)

	if errors.Is(err, http.ErrServerClosed) {
		return net.ErrClosed
	}
	return err
}

func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true

	sessions := make([]*serverSession, 0, len(s.active))
	for session := range s.active {
		sessions = append(sessions, session)
	}

	connections := make([]*trackedHTTPConn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}

	s.mu.Unlock()
	s.cancel()

	for _, session := range sessions {
		session.close()
	}
	// Track connections across both protocol versions so shutdown also
	// interrupts lazy TLS handshakes and active multiplexed streams.
	for _, conn := range connections {
		conn.Close()
	}

	var err error
	if s.http != nil {
		err = s.http.Close()
	}
	if s.http3 != nil {
		err = errors.Join(err, s.http3.Close())
	}
	return err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.config.host != "" && !strings.EqualFold(r.Host, s.config.host) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if (s.config.version == "1.1" && r.ProtoMajor != 1) || (s.config.version == "2" && r.ProtoMajor != 2) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	id, sequence, err := s.config.metadataFrom(r)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	s.config.responseHeaders(w)
	// HTTP/3 can report -1 even for an empty GET. Unknown length is safe
	// on multiplexed streams; HTTP/1 body draining must not block the response.
	upload := r.Method != http.MethodGet && r.Method != http.MethodOptions || sequence != ""
	if !upload && (r.ContentLength > 0 || r.ProtoMajor == 1 && r.ContentLength < 0 || len(r.TransferEncoding) != 0) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, "+s.config.uplink.method)
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodHead || r.Method == http.MethodConnect {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.config.validPadding(r) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	mode := modePacket
	if !upload {
		if id == "" || s.config.mode == modeSingle {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	} else {
		if id == "" {
			if sequence != "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mode = modeSingle
		} else if sequence == "" {
			mode = modeStream
		}
		if s.config.mode != "" && s.config.mode != "auto" && s.config.mode != mode {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodGet && mode != modePacket || mode != modePacket && r.ProtoMajor < 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	if upload && mode == modePacket {
		s.packet(w, r, id, sequence)
		return
	}

	session := s.session(id)
	if session == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if upload && mode == modeStream {
		if !session.claimStream() {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			session.close()
			return
		}
		s.streamUpload(w, r, session)
		return
	}

	if !session.claimDownload() {
		w.WriteHeader(http.StatusConflict)
		return
	}

	if mode == modeSingle && !session.claimStream() {
		session.close()
		w.WriteHeader(http.StatusConflict)
		return
	}

	s.download(w, r, session, mode == modeSingle)
}

func (s *Server) packet(w http.ResponseWriter, r *http.Request, id, sequence string) {
	if r.ContentLength > int64(s.config.postSize) {
		rejectPacket(w, r, http.StatusRequestEntityTooLarge)
		return
	}

	reservation := int64(s.config.postSize)
	if !s.budget.TryAcquire(reservation) {
		rejectPacket(w, r, http.StatusServiceUnavailable)
		return
	}

	transferred := false
	defer func() {
		if !transferred {
			s.budget.Release(reservation)
		}
	}()

	controller := http.NewResponseController(w)
	readBody := r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
	if readBody {
		_ = controller.SetReadDeadline(time.Now().Add(serverJoinTimeout))
	}
	body, err := s.config.uplink.readPacket(r, s.config.postSize)
	if errors.Is(err, errPacketTooLarge) {
		rejectPacket(w, r, http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil || len(body) == 0 {
		rejectPacket(w, r, http.StatusBadRequest)
		return
	}

	if readBody {
		_ = controller.SetReadDeadline(time.Time{})
	}
	session := s.session(id)
	if session == nil {
		rejectPacket(w, r, http.StatusServiceUnavailable)
		return
	}

	number, _ := strconv.ParseUint(sequence, 10, 64) // validated before allocation
	// Queue admission has its own bound after body reception. Retain the
	// global fail-fast budget so waiters cannot accumulate unbounded payloads.
	admissionContext := r.Context()
	if r.ProtoMajor == 1 {
		// Xray's HTTP/1 uploader pipelines complete packets without waiting
		// for acknowledgements and may close the TCP connection immediately.
		// Once fully received, that packet belongs to the logical session.
		// Preserve it across TCP closure, but keep the queue timeout and
		// session/owner cancellation so stalled uploads remain bounded.
		admissionContext = context.WithoutCancel(admissionContext)
	}
	ctx, cancel := context.WithTimeout(admissionContext, serverJoinTimeout)
	defer cancel()
	if err := session.enqueue(ctx, number, body); err != nil {
		if errors.Is(err, errPacketConflict) {
			rejectPacket(w, r, http.StatusConflict)
		} else {
			rejectPacket(w, r, http.StatusServiceUnavailable)
		}
		return
	}

	transferred = true
	w.WriteHeader(http.StatusOK)
}

// An unsuccessful upload may still have an unread request body. Do not let
// HTTP/1's automatic draining delay this error or consume unbounded input.
func rejectPacket(w http.ResponseWriter, r *http.Request, status int) {
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "close")
	}
	w.WriteHeader(status)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, session *serverSession, single bool) {
	defer session.close()

	stop := context.AfterFunc(r.Context(), session.close)
	defer stop()

	controller := http.NewResponseController(w)

	if !s.config.noSSE {
		w.Header().Set("Content-Type", "text/event-stream")
	}

	w.WriteHeader(http.StatusOK)

	if err := controller.Flush(); err != nil {
		return
	}

	if single {
		go session.copyUpload(r.Body)
	}

	source := M.ParseSocksaddr(r.RemoteAddr)

	local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if local == nil {
		local = session.app.LocalAddr()
	}

	conn := &inboundHTTPConn{Conn: session.app, session: session, source: source, local: local}

	go s.handler.NewConnectionEx(r.Context(), conn, source, M.Socksaddr{}, N.OnceClose(func(error) {
		session.close()
	}))

	_, _ = io.Copy(&flushedResponse{writer: w, controller: controller}, session.wire)
}

type flushedResponse struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
}

func (w *flushedResponse) Write(p []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(serverJoinTimeout))
	defer w.controller.SetWriteDeadline(time.Time{})

	n, err := w.writer.Write(p)
	if err == nil {
		err = w.controller.Flush()
	}
	return n, err
}

type trackedHTTPListener struct {
	net.Listener
	server *Server
}

func (l *trackedHTTPListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	if l.server.tls != nil {
		conn = &serverTLSConn{Conn: conn, server: l.server}
	}

	tracked := &trackedHTTPConn{Conn: conn, server: l.server}

	l.server.mu.Lock()
	if l.server.closed {
		l.server.mu.Unlock()
		conn.Close()
		return nil, net.ErrClosed
	}
	l.server.connections[tracked] = struct{}{}
	l.server.mu.Unlock()

	return tracked, nil
}

type trackedHTTPConn struct {
	net.Conn
	server *Server
}

func (c *trackedHTTPConn) Close() error {
	err := c.Conn.Close()
	c.server.mu.Lock()
	delete(c.server.connections, c)
	c.server.mu.Unlock()
	return err
}

// Keep the raw connection immutable so Close and deadline operations can race
// safely with a lazy TLS/REALITY handshake. sing's generic LazyConn swaps its
// embedded Conn during the handshake, which is unsafe for concurrent shutdown.
type serverTLSConn struct {
	net.Conn
	server  *Server
	once    sync.Once
	secured net.Conn
	err     error
}

func (c *serverTLSConn) handshake() {
	c.once.Do(func() {
		c.secured, c.err = tls.ServerHandshake(c.server.ctx, c.Conn, c.server.tls)
	})
}

func (c *serverTLSConn) Read(p []byte) (int, error) {
	c.handshake()
	if c.err != nil {
		return 0, c.err
	}
	return c.secured.Read(p)
}

func (c *serverTLSConn) Write(p []byte) (int, error) {
	c.handshake()
	if c.err != nil {
		return 0, c.err
	}
	return c.secured.Write(p)
}

// Keep the streaming-upload response active for HTTP intermediaries. Only this
// handler writes the response; the upload goroutine owns the request body.
func (s *Server) streamUpload(w http.ResponseWriter, r *http.Request, session *serverSession) {
	defer session.close()

	go session.copyUpload(r.Body)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	writer := &flushedResponse{writer: w, controller: http.NewResponseController(w)}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-session.ctx.Done():
			return
		case <-ticker.C:
			value := s.config.padding.value(int(s.config.paddingSize.sample()))
			if _, err := writer.Write([]byte(value)); err != nil {
				return
			}
		}
	}
}
