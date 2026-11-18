//go:build go1.24 && with_quic

package tls

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	qtls "github.com/sagernet/sing-quic"
)

var _ qtls.Config = (*ECHClientConfig)(nil)

func (s *ECHClientConfig) dialWithECHRetry(ctx context.Context, dial func(*tls.Config) (*quic.Conn, error)) (*quic.Conn, error) {
	tlsConfig, err := s.prepareSTDConfig(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := dial(tlsConfig)
	var rejection *tls.ECHRejectionError
	if errors.As(err, &rejection) && len(rejection.RetryConfigList) > 0 {
		s.SetECHConfigList(rejection.RetryConfigList)
		// Use the authenticated retry config directly, even when the DNS TTL
		// is zero. A second lookup could return the same stale config.
		retryConfig := tlsConfig.Clone()
		retryConfig.EncryptedClientHelloConfigList = rejection.RetryConfigList
		return dial(retryConfig)
	}
	return conn, err
}

func (s *ECHClientConfig) Dial(ctx context.Context, conn net.Conn, config *quic.Config) (*quic.Conn, error) {
	return s.dialWithECHRetry(ctx, func(tlsConfig *tls.Config) (*quic.Conn, error) {
		return quic.DialConn(ctx, conn, tlsConfig, config)
	})
}

func (s *ECHClientConfig) DialEarly(ctx context.Context, conn net.Conn, config *quic.Config) (*quic.Conn, error) {
	return s.dialWithECHRetry(ctx, func(tlsConfig *tls.Config) (*quic.Conn, error) {
		return quic.DialEarlyConn(ctx, conn, tlsConfig, config)
	})
}

func (s *ECHClientConfig) CreateTransport(conn net.Conn, quicConnPtr **quic.Conn, quicConfig *quic.Config) http.RoundTripper {
	return s.createHTTP3Transport(quicConnPtr, quicConfig, func(ctx context.Context, tlsConfig *tls.Config, config *quic.Config) (*quic.Conn, error) {
		return quic.DialEarlyConn(ctx, conn, tlsConfig, config)
	})
}

func (s *ECHClientConfig) CreatePacketTransport(conn net.PacketConn, remoteAddr net.Addr, quicConnPtr **quic.Conn, quicConfig *quic.Config) http.RoundTripper {
	return s.createHTTP3Transport(quicConnPtr, quicConfig, func(ctx context.Context, tlsConfig *tls.Config, config *quic.Config) (*quic.Conn, error) {
		return quic.DialEarly(ctx, conn, remoteAddr, tlsConfig, config)
	})
}

func (s *ECHClientConfig) createHTTP3Transport(quicConnPtr **quic.Conn, quicConfig *quic.Config, dial func(context.Context, *tls.Config, *quic.Config) (*quic.Conn, error)) http.RoundTripper {
	handshakeTimeout := s.HandshakeTimeout()
	return &http3.Transport{
		QUICConfig: quicConfig,
		Dial: func(ctx context.Context, _ string, _ *tls.Config, config *quic.Config) (*quic.Conn, error) {
			config = cloneQUICConfigWithHandshakeTimeout(config, handshakeTimeout)
			conn, err := s.dialWithECHRetry(ctx, func(tlsConfig *tls.Config) (*quic.Conn, error) {
				tlsConfig.NextProtos = []string{http3.NextProtoH3}
				return dial(ctx, tlsConfig, config)
			})
			if err != nil {
				return nil, qtls.WrapError(err)
			}
			*quicConnPtr = conn
			return conn, nil
		},
	}
}

func cloneQUICConfigWithHandshakeTimeout(config *quic.Config, timeout time.Duration) *quic.Config {
	if timeout <= 0 {
		return config
	}
	if config == nil {
		return &quic.Config{HandshakeIdleTimeout: timeout}
	}
	if config.HandshakeIdleTimeout != 0 {
		return config
	}
	cloned := config.Clone()
	cloned.HandshakeIdleTimeout = timeout
	return cloned
}
