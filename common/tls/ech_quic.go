//go:build with_quic

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

func (s *ECHClientConfig) Dial(ctx context.Context, conn net.Conn, config *quic.Config) (*quic.Conn, error) {
	tlsConfig, err := s.prepareSTDConfig(ctx)
	if err != nil {
		return nil, err
	}
	quicConn, err := quic.DialConn(ctx, conn, tlsConfig, config)
	if err == nil {
		return quicConn, nil
	}
	var echErr *tls.ECHRejectionError
	if errors.As(err, &echErr) && len(echErr.RetryConfigList) > 0 {
		s.SetECHConfigList(echErr.RetryConfigList)
		retryConfig := tlsConfig.Clone()
		retryConfig.EncryptedClientHelloConfigList = echErr.RetryConfigList
		return quic.DialConn(ctx, conn, retryConfig, config)
	}
	return nil, err
}

func (s *ECHClientConfig) DialEarly(ctx context.Context, conn net.Conn, config *quic.Config) (*quic.Conn, error) {
	tlsConfig, err := s.prepareSTDConfig(ctx)
	if err != nil {
		return nil, err
	}
	quicConn, err := quic.DialEarlyConn(ctx, conn, tlsConfig, config)
	if err == nil {
		return quicConn, nil
	}
	var echErr *tls.ECHRejectionError
	if errors.As(err, &echErr) && len(echErr.RetryConfigList) > 0 {
		s.SetECHConfigList(echErr.RetryConfigList)
		retryConfig := tlsConfig.Clone()
		retryConfig.EncryptedClientHelloConfigList = echErr.RetryConfigList
		return quic.DialEarlyConn(ctx, conn, retryConfig, config)
	}
	return nil, err
}

func (s *ECHClientConfig) CreateTransport(conn net.Conn, quicConnPtr **quic.Conn, quicConfig *quic.Config) http.RoundTripper {
	handshakeTimeout := s.HandshakeTimeout()
	return &http3.Transport{
		QUICConfig: quicConfig,
		Dial: func(ctx context.Context, addr string, _ *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
			cfg = cloneQUICConfigWithHandshakeTimeout(cfg, handshakeTimeout)
			tlsConfig, err := s.prepareSTDConfig(ctx)
			if err != nil {
				return nil, err
			}
			tlsConfig.NextProtos = []string{http3.NextProtoH3}
			quicConn, err := quic.DialEarlyConn(ctx, conn, tlsConfig, cfg)
			if err != nil {
				var echErr *tls.ECHRejectionError
				if errors.As(err, &echErr) && len(echErr.RetryConfigList) > 0 {
					s.SetECHConfigList(echErr.RetryConfigList)
					retryConfig := tlsConfig.Clone()
					retryConfig.EncryptedClientHelloConfigList = echErr.RetryConfigList
					quicConn, err = quic.DialEarlyConn(ctx, conn, retryConfig, cfg)
				}
			}
			if err != nil {
				return nil, qtls.WrapError(err)
			}
			*quicConnPtr = quicConn
			return quicConn, nil
		},
	}
}

func (s *ECHClientConfig) CreatePacketTransport(conn net.PacketConn, remoteAddr net.Addr, quicConnPtr **quic.Conn, quicConfig *quic.Config) http.RoundTripper {
	handshakeTimeout := s.HandshakeTimeout()
	return &http3.Transport{
		QUICConfig: quicConfig,
		Dial: func(ctx context.Context, addr string, _ *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
			cfg = cloneQUICConfigWithHandshakeTimeout(cfg, handshakeTimeout)
			tlsConfig, err := s.prepareSTDConfig(ctx)
			if err != nil {
				return nil, err
			}
			tlsConfig.NextProtos = []string{http3.NextProtoH3}
			quicConn, err := quic.DialEarly(ctx, conn, remoteAddr, tlsConfig, cfg)
			if err != nil {
				var echErr *tls.ECHRejectionError
				if errors.As(err, &echErr) && len(echErr.RetryConfigList) > 0 {
					s.SetECHConfigList(echErr.RetryConfigList)
					retryConfig := tlsConfig.Clone()
					retryConfig.EncryptedClientHelloConfigList = echErr.RetryConfigList
					quicConn, err = quic.DialEarly(ctx, conn, remoteAddr, retryConfig, cfg)
				}
			}
			if err != nil {
				return nil, qtls.WrapError(err)
			}
			*quicConnPtr = quicConn
			return quicConn, nil
		},
	}
}

func cloneQUICConfigWithHandshakeTimeout(quicConfig *quic.Config, handshakeTimeout time.Duration) *quic.Config {
	if handshakeTimeout <= 0 {
		return quicConfig
	}
	if quicConfig == nil {
		return &quic.Config{HandshakeIdleTimeout: handshakeTimeout}
	}
	if quicConfig.HandshakeIdleTimeout != 0 {
		return quicConfig
	}
	cloned := quicConfig.Clone()
	cloned.HandshakeIdleTimeout = handshakeTimeout
	return cloned
}
