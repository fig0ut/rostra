//go:build with_quic

package v2rayxhttp

import (
	"context"
	stdTLS "crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fig0ut/rostra/common/tls"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	qtls "github.com/sagernet/sing-quic"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func http3Factory(d N.Dialer, addr M.Socksaddr, tc tls.Config, keepAlive time.Duration) (func() http.RoundTripper, error) {
	if tc == nil {
		return nil, fmt.Errorf("xhttp: HTTP/3 requires TLS")
	}
	if ktlsConfig, isKTLS := tc.(*tls.KTLSClientConfig); isKTLS {
		tc = ktlsConfig.Config
	}
	tc = tc.Clone()
	tc.SetNextProtos([]string{http3.NextProtoH3})
	if _, err := tc.STDConfig(); err != nil {
		return nil, fmt.Errorf("xhttp: HTTP/3 TLS: %w", err)
	}

	return func() http.RoundTripper {
		return &http3.Transport{
			QUICConfig: &quic.Config{KeepAlivePeriod: keepAlive},
			Dial: func(ctx context.Context, _ string, _ *stdTLS.Config, qc *quic.Config) (*quic.Conn, error) {
				return dialHTTP3(ctx, d, addr, tc, qc, true)
			},
		}
	}, nil
}

func dialHTTP3(ctx context.Context, d N.Dialer, addr M.Socksaddr, tc tls.Config, qc *quic.Config, echRetry bool) (*quic.Conn, error) {
	conn, err := d.DialContext(ctx, N.NetworkUDP, addr)
	if err != nil {
		return nil, err
	}
	var quicConfig *quic.Config
	if qc != nil {
		quicConfig = qc.Clone()
	}
	q, err := qtls.DialEarly(ctx, conn, tc, quicConfig)
	if err != nil {
		conn.Close()
		var echErr *stdTLS.ECHRejectionError
		if _, isDynamicECH := tc.(*tls.ECHClientConfig); !isDynamicECH && echRetry && errors.As(err, &echErr) && len(echErr.RetryConfigList) > 0 {
			if echConfig, isECH := tc.(tls.ECHCapableConfig); isECH {
				echConfig.SetECHConfigList(echErr.RetryConfigList)
				return dialHTTP3(ctx, d, addr, tc, qc, false)
			}
		}
		return nil, err
	}

	go func() { <-q.Context().Done(); conn.Close() }()

	return q, nil
}
