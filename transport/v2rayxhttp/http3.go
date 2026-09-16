//go:build with_quic

package v2rayxhttp

import (
	"context"
	stdTLS "crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/tls"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func http3Factory(d N.Dialer, addr M.Socksaddr, tc tls.Config, keepAlive time.Duration) (func() http.RoundTripper, error) {
	config, err := tc.STDConfig()
	if err != nil {
		return nil, fmt.Errorf("xhttp: HTTP/3 TLS: %w", err)
	}
	config = config.Clone()
	config.NextProtos = []string{"h3"}

	return func() http.RoundTripper {
		return &http3.Transport{TLSClientConfig: config, QUICConfig: &quic.Config{KeepAlivePeriod: keepAlive}, Dial: func(ctx context.Context, _ string, tc *stdTLS.Config, qc *quic.Config) (*quic.Conn, error) {
			conn, err := d.DialContext(ctx, N.NetworkUDP, addr)
			if err != nil {
				return nil, err
			}

			q, err := quic.DialEarlyConn(ctx, conn, tc, qc)
			if err != nil {
				conn.Close()
				return nil, err
			}

			go func() { <-q.Context().Done(); conn.Close() }()

			return q, nil
		}}
	}, nil
}
