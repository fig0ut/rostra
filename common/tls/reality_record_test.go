//go:build with_utls

package tls

import (
	"bytes"
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/option"

	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

type realityProbeTestStore struct {
	adapter.CertificateStore
	pool *x509.CertPool
}

func (s realityProbeTestStore) Pool() *x509.CertPool { return s.pool }

func realityRecordTarget(t *testing.T, curve stdtls.CurveID) (context.Context, net.Listener) {
	t.Helper()
	config := realityTestTargetTLSConfig(t, curve)
	config.SessionTicketsDisabled = true
	certificate, err := x509.ParseCertificate(config.Certificates[0].Certificate[0])
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	ctx := service.ContextWith[adapter.CertificateStore](t.Context(), realityProbeTestStore{pool: pool})
	target := realityTestListener(t, func(conn net.Conn) {
		server := stdtls.Server(conn, config)
		if server.Handshake() != nil {
			return
		}
		// Two known wire lengths: five-byte header, 16-byte tag, content type,
		// and payload. Close the TCP connection without adding a TLS alert.
		server.Write(make([]byte, 31))
		server.Write(make([]byte, 95))
	})
	return ctx, target
}

func TestRealityRecordProbeCache(t *testing.T) {
	ctx, target := realityRecordTarget(t, stdtls.X25519MLKEM768)
	var dials atomic.Int32
	config := &utls.RealityConfig{Type: "tcp", Dest: target.Addr().String(), DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		if network != "tcp" || address != target.Addr().String() {
			t.Error("probe bypassed the configured target")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	cache := newRealityRecordCache(ctx, config)
	t.Cleanup(cache.cancel)
	for _, protocols := range [][]string{nil, {"http/1.1"}, {"h2", "http/1.1"}} {
		var wg sync.WaitGroup
		results := make(chan []int, 16)
		for range 16 {
			wg.Go(func() {
				results <- cache.get(t.Context(), "example.com", protocols)
			})
		}
		wg.Wait()
		close(results)
		for lengths := range results {
			require.Equal(t, []int{53, 117}, lengths)
		}
	}
	require.EqualValues(t, 3, dials.Load(), "concurrent handshakes must share a probe")
	cache.mu.Lock()
	cache.entries[realityRecordKey{"example.com", 0}].expires = time.Now().Add(-time.Second)
	cache.mu.Unlock()
	require.Equal(t, []int{53, 117}, cache.get(t.Context(), "example.com", nil))
	require.EqualValues(t, 4, dials.Load(), "expired samples must refresh")

	untrusted := newRealityRecordCache(t.Context(), config)
	t.Cleanup(untrusted.cancel)
	require.Nil(t, untrusted.get(t.Context(), "example.com", nil))
	require.Nil(t, untrusted.get(t.Context(), "example.com", nil))
	require.EqualValues(t, 5, dials.Load(), "failed probes must be cached briefly")
}

func TestRealityRecordProbeCancellation(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	cache := newRealityRecordCache(t.Context(), &utls.RealityConfig{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		defer close(finished)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	defer cache.cancel()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan []int, 1)
	go func() { done <- cache.get(ctx, "example.com", nil) }()
	<-started
	cancel()
	select {
	case lengths := <-done:
		require.Nil(t, lengths)
	case <-time.After(time.Second):
		t.Fatal("handshake cancellation did not release the waiter")
	}
	select {
	case <-finished:
		t.Fatal("one canceled handshake canceled the shared probe")
	default:
	}
	cache.cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("closing the server did not cancel its probe")
	}
}

func TestRealityRecordProbeStalledTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	peerClosed := make(chan struct{})
	cache := newRealityRecordCache(ctx, &utls.RealityConfig{DialContext: func(context.Context, string, string) (net.Conn, error) {
		conn, peer := net.Pipe()
		go func() {
			defer close(peerClosed)
			defer peer.Close()
			// Accept ClientHello, but never send a ServerHello.
			io.Copy(io.Discard, peer)
		}()
		return conn, nil
	}})
	defer cache.cancel()
	require.Nil(t, cache.get(t.Context(), "example.com", nil))
	select {
	case <-peerClosed:
	case <-time.After(time.Second):
		t.Fatal("probe deadline did not close the stalled target socket")
	}
}

type realityFragmentedTestConn struct {
	net.Conn
	reader *bytes.Reader
}

func (c realityFragmentedTestConn) Read(b []byte) (int, error) {
	return c.reader.Read(b[:min(len(b), 1)])
}

func TestRealityRecordParsing(t *testing.T) {
	record := func(length int) []byte {
		b := make([]byte, length)
		b[0], b[1], b[2] = 23, 3, 3
		binary.BigEndian.PutUint16(b[3:5], uint16(length-5))
		return b
	}
	for _, test := range []struct {
		name    string
		wire    []byte
		want    []int
		invalid bool
	}{
		{"empty", nil, nil, false},
		{"fragmented", append(record(53), record(117)...), []int{53, 117}, false},
		{"truncated-header", []byte{23, 3}, nil, true},
		{"truncated-payload", record(53)[:52], nil, true},
		{"too-short", record(21), nil, true},
		{"too-long", record(16407), nil, true},
		{"too-many", bytes.Repeat(record(53), realityMaxProbeRecords+1), nil, true},
		{"plaintext-alert", []byte{21, 3, 3, 0, 2, 2, 40}, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			lengths, err := readRealityRecordLengths(realityFragmentedTestConn{reader: bytes.NewReader(test.wire)})
			if test.invalid {
				require.Error(t, err)
				require.Nil(t, lengths)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.want, lengths)
			}
		})
	}
	// Coalesced records must remain separate while TLS consumes its handshake.
	framed := &realityProbeConn{Conn: realityFragmentedTestConn{reader: bytes.NewReader(append(record(53), record(117)...))}}
	first := make([]byte, 53)
	_, err := io.ReadFull(framed, first)
	require.NoError(t, err)
	lengths, err := readRealityRecordLengths(framed.Conn)
	require.NoError(t, err)
	require.Equal(t, []int{117}, lengths)
}

func TestRealityRecordMatchingHandshake(t *testing.T) {
	ctx, target := realityRecordTarget(t, stdtls.X25519MLKEM768)
	address := M.ParseSocksaddr(target.Addr().String())
	config, err := NewRealityServer(ctx, nil, option.InboundTLSOptions{
		ServerName: "example.com",
		Reality: &option.InboundRealityOptions{
			PrivateKey: realityTestPrivateKey,
			ShortID:    []string{"0123456789abcdef"},
			Handshake:  option.InboundRealityHandshakeOptions{ServerOptions: option.ServerOptions{Server: address.AddrString(), ServerPort: address.Port}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { config.Close() })
	require.NoError(t, config.Start())
	server := config.(*RealityServerConfig)
	require.Equal(t, []int{53, 117}, server.recordCache.get(ctx, "example.com", []string{"h2", "http/1.1"}))
	clone := server.Clone().(*RealityServerConfig)
	require.NoError(t, clone.Close())
	require.NoError(t, server.recordCache.ctx.Err(), "closing a clone must not cancel the original")
	listener := realityTestListener(t, func(conn net.Conn) {
		tlsConn, err := ServerHandshake(ctx, conn, server)
		if err != nil {
			t.Error(err)
			return
		}
		defer tlsConn.Close()
		io.Copy(tlsConn, tlsConn)
	})
	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	// Framing prevents client read-ahead from consuming the records being checked.
	framed := &realityProbeConn{Conn: conn}
	client, err := ClientHandshake(ctx, framed, realityTestClientConfig(t, ""))
	require.NoError(t, err)
	defer client.Close()
	// Peek at the encrypted records and replay them to the TLS reader to verify
	// that they decrypt to no application data and preserve sequence numbers.
	var captured bytes.Buffer
	for _, length := range []int{53, 117} {
		record := make([]byte, length)
		_, err := io.ReadFull(conn, record)
		require.NoError(t, err)
		require.Equal(t, length, int(binary.BigEndian.Uint16(record[3:5]))+5)
		captured.Write(record)
	}
	framed.Conn = &realityReplayTestConn{Conn: conn, reader: io.MultiReader(&captured, conn)}
	_, err = client.Write([]byte("hello"))
	require.NoError(t, err)
	response := make([]byte, 5)
	_, err = io.ReadFull(client, response)
	require.NoError(t, err)
	require.Equal(t, "hello", string(response))
}

type realityReplayTestConn struct {
	net.Conn
	reader io.Reader
}

func (c *realityReplayTestConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func TestRealityRecordProbeWaitSurvivesTargetClose(t *testing.T) {
	targetConfig := realityTestTargetTLSConfig(t, stdtls.X25519MLKEM768)
	targetConn := make(chan net.Conn, 1)
	target := realityTestListener(t, func(conn net.Conn) {
		targetConn <- conn
		stdtls.Server(conn, targetConfig).Handshake()
	})
	server := realityTestServerConfig(t, target, "").(*RealityServerConfig)
	probeStarted := make(chan struct{})
	probeReady := make(chan struct{})
	defer close(probeReady)
	server.config.GetPostHandshakeRecordLengths = func(ctx context.Context, _ string, _ []string) []int {
		close(probeStarted)
		select {
		case <-probeReady:
		case <-ctx.Done():
		}
		return nil
	}
	listener := realityTestListener(t, func(conn net.Conn) {
		tlsConn, err := ServerHandshake(t.Context(), conn, server)
		if err == nil {
			defer tlsConn.Close()
			io.Copy(tlsConn, tlsConn)
		}
	})
	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	client, err := ClientHandshake(t.Context(), conn, realityTestClientConfig(t, ""))
	require.NoError(t, err)
	defer client.Close()
	<-probeStarted
	require.NoError(t, (<-targetConn).Close())
	// Allow the target reader to observe EOF while the callback is still pending.
	// Receiving no bytes before this short read deadline shows that the REALITY
	// connection remains open instead of inheriting the target's close.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	_, err = client.Read(make([]byte, 1))
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	require.True(t, netErr.Timeout())
}
