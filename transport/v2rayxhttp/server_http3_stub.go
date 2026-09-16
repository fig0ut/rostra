//go:build !with_quic

package v2rayxhttp

import (
	"fmt"

	"github.com/sagernet/sing-box/common/tls"
)

func newHTTP3Server(_ *Server, _ tls.ServerConfig) (packetHTTPServer, error) {
	return nil, fmt.Errorf("xhttp: HTTP/3 requires the with_quic build tag")
}
