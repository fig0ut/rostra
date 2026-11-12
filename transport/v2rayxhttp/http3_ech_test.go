//go:build with_quic

package v2rayxhttp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	stdTLS "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/common/tls"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

type echTestDNSRouter struct {
	mu      sync.Mutex
	records map[string][]byte
	ttl     uint32
	queries []string
}

func (r *echTestDNSRouter) Start(adapter.StartStage, *adapter.Scope) error { return nil }
func (r *echTestDNSRouter) Close() error                                   { return nil }
func (r *echTestDNSRouter) ClearCache()                                    {}
func (r *echTestDNSRouter) ResetNetwork()                                  {}

func (r *echTestDNSRouter) Exchange(_ context.Context, message *mDNS.Msg, _ adapter.DNSQueryOptions) (*mDNS.Msg, error) {
	resp := new(mDNS.Msg)
	resp.SetReply(message)
	if len(message.Question) == 0 {
		return resp, nil
	}
	q := message.Question[0]
	r.mu.Lock()
	r.queries = append(r.queries, q.Name)
	echConfigList := r.records[q.Name]
	ttl := r.ttl
	r.mu.Unlock()

	if q.Qtype == mDNS.TypeHTTPS && len(echConfigList) > 0 {
		httpsRR := &mDNS.HTTPS{
			SVCB: mDNS.SVCB{
				Hdr: mDNS.RR_Header{
					Name:   q.Name,
					Rrtype: mDNS.TypeHTTPS,
					Class:  mDNS.ClassINET,
					Ttl:    ttl,
				},
				Priority: 1,
				Target:   ".",
				Value: []mDNS.SVCBKeyValue{
					&mDNS.SVCBECHConfig{
						ECH: echConfigList,
					},
				},
			},
		}
		resp.Answer = append(resp.Answer, httpsRR)
	}
	return resp, nil
}

func (r *echTestDNSRouter) ExchangeAsync(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions, callback func(response *mDNS.Msg, err error)) {
	callback(r.Exchange(ctx, message, options))
}

func (r *echTestDNSRouter) Lookup(context.Context, string, adapter.DNSQueryOptions) ([]netip.Addr, error) {
	return nil, errors.New("unexpected Lookup call")
}

func (r *echTestDNSRouter) LookupReverseMapping(netip.Addr) (string, bool) {
	return "", false
}

func (r *echTestDNSRouter) querySnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.queries))
	copy(out, r.queries)
	return out
}

type echoTransportHandler struct{}

func (echoTransportHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, onClose N.CloseHandlerFunc) {
	defer func() {
		conn.Close()
		if onClose != nil {
			onClose(nil)
		}
	}()
	_, _ = io.Copy(conn, conn)
}

func decodePEMBytes(t *testing.T, pemText string) []byte {
	t.Helper()
	block, rest := pem.Decode([]byte(pemText))
	require.NotNil(t, block)
	require.Empty(t, rest)
	return block.Bytes
}

func generateSANTestCert(t *testing.T, dnsNames ...string) (keyPEM []byte, certPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsNames[0]},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return keyPEM, certPEM
}

func startXHTTP3ECHServer(
	t *testing.T,
	certPEM []byte,
	keyPEM []byte,
	echKeyPEM string,
	allowRejectionRetry bool,
) (M.Socksaddr, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	serverTLS, err := tls.NewSTDServer(ctx, log.NewNOPFactory().Logger(), option.InboundTLSOptions{
		Enabled:     true,
		ServerName:  "secret.example.org",
		Certificate: strings.Split(strings.TrimSpace(string(certPEM)), "\n"),
		Key:         strings.Split(strings.TrimSpace(string(keyPEM)), "\n"),
		ALPN:        []string{"h3"},
		ECH: &option.InboundECHOptions{
			Enabled: true,
			Key:     strings.Split(strings.TrimSpace(echKeyPEM), "\n"),
		},
	})
	require.NoError(t, err)

	var acceptedHandshakes atomic.Int32
	rawServerTLS, err := serverTLS.STDConfig()
	require.NoError(t, err)
	if allowRejectionRetry {
		for i := range rawServerTLS.EncryptedClientHelloKeys {
			rawServerTLS.EncryptedClientHelloKeys[i].SendAsRetry = true
		}
	}
	rawServerTLS.GetConfigForClient = func(hello *stdTLS.ClientHelloInfo) (*stdTLS.Config, error) {
		if allowRejectionRetry && hello.ServerName == "public.example.org" {
			return nil, nil
		}
		if hello.ServerName != "secret.example.org" {
			return nil, errors.New("unexpected ClientHello ServerName after ECH: " + hello.ServerName)
		}
		return nil, nil
	}
	rawServerTLS.VerifyConnection = func(state stdTLS.ConnectionState) error {
		if allowRejectionRetry && !state.ECHAccepted && state.ServerName == "public.example.org" {
			return nil
		}
		if !state.ECHAccepted {
			return errors.New("server did not accept ECH")
		}
		if state.ServerName != "secret.example.org" {
			return errors.New("unexpected inner ServerName: " + state.ServerName)
		}
		acceptedHandshakes.Add(1)
		return nil
	}

	require.NoError(t, serverTLS.Start())
	server, err := NewServer(ctx, logger.NOP(), option.V2RayXHTTPOptions{
		Path:        "/xhttp",
		HTTPVersion: "3",
	}, serverTLS, echoTransportHandler{})
	require.NoError(t, err)

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.ServePacket(packetConn)
	}()

	t.Cleanup(func() {
		cancel()
		_ = server.Close()
		_ = packetConn.Close()
		_ = serverTLS.Close()
		<-done
	})

	return M.SocksaddrFromNet(packetConn.LocalAddr()), &acceptedHandshakes
}

func assertXHTTPRoundTrip(t *testing.T, client *Client, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.DialContext(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))

	_, err = conn.Write(payload)
	require.NoError(t, err)

	reply := make([]byte, len(payload))
	_, err = io.ReadFull(conn, reply)
	require.NoError(t, err)
	require.True(t, bytes.Equal(payload, reply))
}

func TestHTTP3DynamicECH(t *testing.T) {
	keyPEM, certPEM, err := tls.GenerateCertificate(nil, nil, time.Now, "secret.example.org", time.Now().Add(time.Hour))
	require.NoError(t, err)
	echConfigPEM, echKeyPEM, err := tls.ECHKeygenDefault("public.example.org")
	require.NoError(t, err)
	echConfigBytes := decodePEMBytes(t, echConfigPEM)

	// Verify miekg/dns HTTPS SvcParam formatting matches ECHClientConfig's base64 decoder.
	kv := &mDNS.SVCBECHConfig{ECH: echConfigBytes}
	decoded, err := base64.StdEncoding.DecodeString(kv.String())
	require.NoError(t, err)
	require.Equal(t, echConfigBytes, decoded)

	serverAddr, accepted := startXHTTP3ECHServer(t, certPEM, keyPEM, echKeyPEM, false)
	certLines := strings.Split(strings.TrimSpace(string(certPEM)), "\n")

	t.Run("default-server-name-and-ttl-cache", func(t *testing.T) {
		accepted.Store(0)
		dnsRouter := &echTestDNSRouter{
			records: map[string][]byte{
				"secret.example.org.": echConfigBytes,
			},
			ttl: 300,
		}
		ctx := service.ContextWith[adapter.DNSRouter](context.Background(), dnsRouter)

		clientTLS, err := tls.NewClient(ctx, logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{
			Enabled:     true,
			ServerName:  "secret.example.org",
			Certificate: certLines,
			ECH: &option.OutboundECHOptions{
				Enabled: true,
			},
		})
		require.NoError(t, err)

		client, err := NewClient(ctx, N.SystemDialer, serverAddr, option.V2RayXHTTPOptions{
			Path:        "/xhttp",
			Mode:        "stream-one",
			HTTPVersion: "3",
			Xmux: &option.V2RayXHTTPXMuxOptions{
				MaxConnections: &option.XHTTPRange{From: 2, To: 2},
				CMaxReuseTimes: &option.XHTTPRange{From: 1, To: 1},
			},
		}, clientTLS)
		require.NoError(t, err)
		defer client.Close()

		assertXHTTPRoundTrip(t, client, []byte("hello over xhttp h3 ech 1"))
		assertXHTTPRoundTrip(t, client, []byte("hello over xhttp h3 ech 2"))

		require.Equal(t, int32(2), accepted.Load())
		require.Equal(t, []string{"secret.example.org."}, dnsRouter.querySnapshot())
	})

	t.Run("query-server-name-and-download-settings", func(t *testing.T) {
		accepted.Store(0)
		dnsRouter := &echTestDNSRouter{
			records: map[string][]byte{
				"ech-lookup.example.org.": echConfigBytes,
			},
			ttl: 300,
		}
		ctx := service.ContextWith[adapter.DNSRouter](context.Background(), dnsRouter)

		tlsOptions := option.OutboundTLSOptions{
			Enabled:     true,
			ServerName:  "secret.example.org",
			Certificate: certLines,
			ECH: &option.OutboundECHOptions{
				Enabled:         true,
				QueryServerName: "ech-lookup.example.org",
			},
		}
		clientTLS, err := tls.NewClient(ctx, logger.NOP(), "127.0.0.1", tlsOptions)
		require.NoError(t, err)

		client, err := NewClient(ctx, N.SystemDialer, serverAddr, option.V2RayXHTTPOptions{
			Path:        "/xhttp",
			Mode:        "packet-up",
			HTTPVersion: "3",
			DownloadSettings: &option.V2RayXHTTPDownloadOptions{
				ServerOptions: option.ServerOptions{
					Server:     serverAddr.AddrString(),
					ServerPort: serverAddr.Port,
				},
				OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
					TLS: &tlsOptions,
				},
				Transport: &option.V2RayTransportOptions{
					Type: C.V2RayTransportTypeXHTTP,
					XHTTPOptions: option.V2RayXHTTPOptions{
						Path:        "/xhttp",
						HTTPVersion: "3",
					},
				},
			},
		}, clientTLS)
		require.NoError(t, err)
		defer client.Close()

		assertXHTTPRoundTrip(t, client, []byte("split upload/download over h3 ech"))
		require.GreaterOrEqual(t, accepted.Load(), int32(2))
		for _, q := range dnsRouter.querySnapshot() {
			require.Equal(t, "ech-lookup.example.org.", q)
		}
	})

	t.Run("missing-dns-ech-fails-closed", func(t *testing.T) {
		accepted.Store(0)
		dnsRouter := &echTestDNSRouter{
			records: map[string][]byte{},
			ttl:     300,
		}
		ctx := service.ContextWith[adapter.DNSRouter](context.Background(), dnsRouter)

		clientTLS, err := tls.NewClient(ctx, logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{
			Enabled:     true,
			ServerName:  "secret.example.org",
			Certificate: certLines,
			ECH: &option.OutboundECHOptions{
				Enabled: true,
			},
		})
		require.NoError(t, err)

		client, err := NewClient(ctx, N.SystemDialer, serverAddr, option.V2RayXHTTPOptions{
			Path:        "/xhttp",
			Mode:        "stream-one",
			HTTPVersion: "3",
		}, clientTLS)
		require.NoError(t, err)
		defer client.Close()

		conn, err := client.DialContext(context.Background())
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))

		_, _ = conn.Write([]byte("must fail"))
		buf := make([]byte, 16)
		_, err = conn.Read(buf)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no ECH config found in DNS records")
		require.Zero(t, accepted.Load())
	})
}

func TestHTTP3ECHRejectionRetry(t *testing.T) {
	keyPEM, certPEM := generateSANTestCert(t, "secret.example.org", "public.example.org")
	staleConfigPEM, _, err := tls.ECHKeygenDefault("public.example.org")
	require.NoError(t, err)
	_, activeKeyPEM, err := tls.ECHKeygenDefault("public.example.org")
	require.NoError(t, err)

	serverAddr, accepted := startXHTTP3ECHServer(t, certPEM, keyPEM, activeKeyPEM, true)
	certLines := strings.Split(strings.TrimSpace(string(certPEM)), "\n")

	t.Run("static-config-retry", func(t *testing.T) {
		accepted.Store(0)
		clientTLS, err := tls.NewClient(context.Background(), logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{
			Enabled:     true,
			ServerName:  "secret.example.org",
			Certificate: certLines,
			ECH: &option.OutboundECHOptions{
				Enabled: true,
				Config:  strings.Split(strings.TrimSpace(staleConfigPEM), "\n"),
			},
		})
		require.NoError(t, err)

		client, err := NewClient(context.Background(), N.SystemDialer, serverAddr, option.V2RayXHTTPOptions{
			Path:        "/xhttp",
			Mode:        "stream-one",
			HTTPVersion: "3",
		}, clientTLS)
		require.NoError(t, err)
		defer client.Close()

		assertXHTTPRoundTrip(t, client, []byte("retry static ech over h3"))
		require.Equal(t, int32(1), accepted.Load())
	})

	t.Run("dynamic-dns-zero-ttl-retry", func(t *testing.T) {
		accepted.Store(0)
		dnsRouter := &echTestDNSRouter{
			records: map[string][]byte{
				"secret.example.org.": decodePEMBytes(t, staleConfigPEM),
			},
			ttl: 0,
		}
		ctx := service.ContextWith[adapter.DNSRouter](context.Background(), dnsRouter)

		clientTLS, err := tls.NewClient(ctx, logger.NOP(), "127.0.0.1", option.OutboundTLSOptions{
			Enabled:     true,
			ServerName:  "secret.example.org",
			Certificate: certLines,
			ECH: &option.OutboundECHOptions{
				Enabled: true,
			},
		})
		require.NoError(t, err)

		client, err := NewClient(ctx, N.SystemDialer, serverAddr, option.V2RayXHTTPOptions{
			Path:        "/xhttp",
			Mode:        "stream-one",
			HTTPVersion: "3",
		}, clientTLS)
		require.NoError(t, err)
		defer client.Close()

		assertXHTTPRoundTrip(t, client, []byte("retry dynamic ech over h3"))
		require.Equal(t, int32(1), accepted.Load())
	})
}
