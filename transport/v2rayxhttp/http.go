package v2rayxhttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/net/http2"
)

func transportFactory(d N.Dialer, addr M.Socksaddr, version string, tc tls.Config, keepAlive time.Duration) (func() http.RoundTripper, error) {
	if version == "3" {
		return http3Factory(d, addr, tc, keepAlive)
	}

	reality := isReality(tc)

	if tc != nil {
		tc = tc.Clone()
		proto := "h2"
		if version == "1.1" {
			proto = "http/1.1"
		}
		tc.SetNextProtos([]string{proto})
	}

	dial := func(ctx context.Context) (net.Conn, error) {
		if tc == nil {
			return d.DialContext(ctx, N.NetworkTCP, addr)
		}

		conn, err := tls.NewDialer(d, tc).DialTLSContext(ctx, addr)
		if err != nil {
			return nil, err
		}
		state := conn.ConnectionState()

		// XHTTP uses HTTP/2 inside authenticated REALITY regardless of the
		// camouflage handshake ALPN. Ordinary TLS must negotiate h2.
		if version == "2" && !reality && state.NegotiatedProtocol != "h2" {
			conn.Close()
			return nil, fmt.Errorf("xhttp: server did not negotiate h2")
		}

		return conn, nil
	}

	return func() http.RoundTripper {
		if version == "2" {
			return &http2.Transport{
				DisableCompression: true,
				AllowHTTP:          tc == nil,
				IdleConnTimeout:    5 * time.Minute,
				ReadIdleTimeout:    keepAlive,
				PingTimeout:        15 * time.Second,
				DialTLSContext: func(ctx context.Context, network, host string, _ *tls.STDConfig) (net.Conn, error) {
					return dial(ctx)
				},
			}
		}
		return &http.Transport{
			Proxy:                 nil,
			DisableCompression:    true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			DialContext: func(ctx context.Context, network, host string) (net.Conn, error) {
				return d.DialContext(ctx, N.NetworkTCP, addr)
			},
			DialTLSContext: func(ctx context.Context, network, host string) (net.Conn, error) {
				return dial(ctx)
			},
		}
	}, nil
}
