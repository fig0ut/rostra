//go:build with_quic

package v2rayxhttp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	"github.com/sagernet/sing-box/common/tls"
)

func newHTTP3Server(handler *Server, config tls.ServerConfig) (packetHTTPServer, error) {
	if config == nil {
		return nil, fmt.Errorf("xhttp: HTTP/3 requires TLS")
	}
	tc, err := config.STDConfig()
	if err != nil {
		return nil, fmt.Errorf("xhttp: HTTP/3 TLS: %w", err)
	}
	return &http3.Server{
		Handler: handler, TLSConfig: tc.Clone(), MaxHeaderBytes: handler.config.maxHeaderBytes,
		ConnContext: func(ctx context.Context, conn *quic.Conn) context.Context {
			return context.WithValue(ctx, http.LocalAddrContextKey, conn.LocalAddr())
		},
		QUICConfig: &quic.Config{MaxIdleTimeout: 90 * time.Second, KeepAlivePeriod: 30 * time.Second},
	}, nil
}
