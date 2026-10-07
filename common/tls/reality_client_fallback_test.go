//go:build with_utls

package tls

import (
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing/common/logger"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

func TestRealityFallbackPath(t *testing.T) {
	for _, path := range []string{"https://other.example/", "//other.example/", "relative", "/#", "/a#fragment", "/bad escape", "/bad\r\nheader", "/bad\u00a0space", "/back\\slash", "/%zz"} {
		_, err := NewRealityClient(t.Context(), logger.NOP(), "example.com", option.OutboundTLSOptions{
			UTLS:    &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
			Reality: &option.OutboundRealityOptions{PublicKey: realityTestPublicKey, FallbackPath: path},
		})
		require.ErrorContains(t, err, "fallback_path", path)
	}
	for _, path := range []string{"", "/", "/news?q=a%2Fb&v=1", "/a%2Fb?x=1&x=2", "/%2F%2Fother.example/"} {
		config, err := NewRealityClient(t.Context(), logger.NOP(), "example.com", option.OutboundTLSOptions{
			UTLS:    &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
			Reality: &option.OutboundRealityOptions{PublicKey: realityTestPublicKey, FallbackPath: path},
		})
		require.NoError(t, err)
		want := path
		if want == "" {
			want = "/"
		}
		require.Equal(t, want, config.Clone().(*RealityClientConfig).fallbackPath)
	}
}

func realityFallbackHTTPServer(t *testing.T, protocol string, handler http.HandlerFunc) (*httptest.Server, pinnedTestCertificate, *atomic.Int32) {
	t.Helper()
	certificate := newPinnedTestCertificate(t, "example.com")
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = protocol == "h2"
	server.TLS = &stdtls.Config{Certificates: []stdtls.Certificate{certificate.Certificate}, MinVersion: stdtls.VersionTLS13}
	if protocol == "none" {
		server.TLS.NextProtos = []string{}
	} else {
		server.TLS.NextProtos = []string{protocol}
	}
	connections := new(atomic.Int32)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, certificate, connections
}

func realityFallbackHTTPClient(t *testing.T, server *httptest.Server, certificate pinnedTestCertificate, fingerprint, path string) (Config, net.Conn) {
	t.Helper()
	config, err := NewRealityClient(t.Context(), logger.NOP(), "example.com", option.OutboundTLSOptions{
		ServerName:  "example.com",
		Certificate: []string{certificate.certificatePEM},
		UTLS:        &option.OutboundUTLSOptions{Enabled: true, Fingerprint: fingerprint},
		Reality:     &option.OutboundRealityOptions{PublicKey: realityTestPublicKey, ShortID: "0123456789abcdef", FallbackPath: path},
	})
	require.NoError(t, err)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return config, conn
}

func TestRealityClientFallbackHTTP(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2", "none"} {
		for _, browser := range []struct {
			name   string
			wantUA string
		}{
			{"chrome", "Chrome/" + utls.HelloChrome_Auto.Version + ".0.0.0"},
			{"firefox", "Firefox/" + utls.HelloFirefox_Auto.Version + ".0"},
			{"safari", "Version/" + utls.HelloSafari_Auto.Version + " Safari/"},
			{"ios", "iPhone; CPU iPhone OS "},
			{"edge", "Edg/" + utls.HelloEdge_Auto.Version + ".0.0.0"},
		} {
			t.Run(protocol+"/"+browser.name, func(t *testing.T) {
				requests := make(chan *http.Request, 2)
				server, certificate, connections := realityFallbackHTTPServer(t, protocol, func(w http.ResponseWriter, r *http.Request) {
					requests <- r.Clone(context.Background())
					// A redirect must not produce another request or connection.
					location := "https://other.example/redirected"
					if protocol == "http/1.1" {
						location = "/redirected"
					}
					http.Redirect(w, r, location, http.StatusFound)
				})
				const path = "/news%2Farchive?a=one%20two&a=three"
				config, conn := realityFallbackHTTPClient(t, server, certificate, browser.name, path)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				result, err := ClientHandshake(ctx, conn, config.Clone())
				require.Nil(t, result)
				require.ErrorContains(t, err, "reality verification failed")
				require.NoError(t, ctx.Err())
				require.Len(t, requests, 1)
				request := <-requests
				require.Equal(t, path, request.RequestURI)
				require.Equal(t, "example.com", request.Host)
				require.Equal(t, http.MethodGet, request.Method)
				wantMajor := 1
				if protocol == "h2" {
					wantMajor = 2
				}
				require.Equal(t, wantMajor, request.ProtoMajor)
				require.Contains(t, request.UserAgent(), browser.wantUA)
				require.Contains(t, request.Header.Get("Accept"), "text/html")
				require.NotEmpty(t, request.Header.Get("Accept-Language"))
				require.Equal(t, "navigate", request.Header.Get("Sec-Fetch-Mode"))
				require.Equal(t, "document", request.Header.Get("Sec-Fetch-Dest"))
				require.Equal(t, "gzip, deflate, br", request.Header.Get("Accept-Encoding"))
				padding, err := request.Cookie("padding")
				require.NoError(t, err)
				require.GreaterOrEqual(t, len(padding.Value), 30)
				require.LessOrEqual(t, len(padding.Value), 61)
				require.EqualValues(t, 1, connections.Load())
			})
		}
	}
}

func TestRealityClientFallbackBounded(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2"} {
		for _, mode := range []string{"cancel", "body-limit"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				requestReceived := make(chan struct{})
				server, certificate, connections := realityFallbackHTTPServer(t, protocol, func(w http.ResponseWriter, r *http.Request) {
					close(requestReceived)
					if mode == "body-limit" {
						w.Header().Set("Content-Type", "text/html")
						// Keep the body open after its first MiB; an unbounded reader
						// would wait until the context expired.
						w.Write([]byte(strings.Repeat("x", realityFallbackMaxBody)))
						w.(http.Flusher).Flush()
					}
					<-r.Context().Done()
				})
				config, conn := realityFallbackHTTPClient(t, server, certificate, "chrome", "")
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := ClientHandshake(ctx, conn, config)
					done <- err
				}()
				select {
				case <-requestReceived:
				case <-ctx.Done():
					t.Fatal("fallback request was not received")
				}
				if mode == "cancel" {
					cancel()
				}
				select {
				case err := <-done:
					require.ErrorContains(t, err, "reality verification failed")
				case <-time.After(2 * time.Second):
					t.Fatal("fallback did not stop at its resource bound")
				}
				if mode == "body-limit" {
					require.NoError(t, ctx.Err())
				}
				require.EqualValues(t, 1, connections.Load())
			})
		}
	}
}

func TestRealityClientFallbackRequiresTrustedCertificate(t *testing.T) {
	for _, protocol := range []string{"http/1.1", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			var requests atomic.Int32
			server, certificate, _ := realityFallbackHTTPServer(t, protocol, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
			})
			config, conn := realityFallbackHTTPClient(t, server, certificate, "chrome", "/must-not-be-requested")
			config.(*RealityClientConfig).uClient.config.RootCAs = x509.NewCertPool()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := ClientHandshake(ctx, conn, config)
			require.ErrorContains(t, err, "certificate signed by unknown authority")
			require.NoError(t, ctx.Err())
			require.Zero(t, requests.Load())
		})
	}
}
