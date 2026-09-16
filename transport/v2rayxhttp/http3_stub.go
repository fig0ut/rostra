//go:build !with_quic

package v2rayxhttp

import (
	"fmt"
	"net/http"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func http3Factory(_ N.Dialer, _ M.Socksaddr, _ tls.Config, _ time.Duration) (func() http.RoundTripper, error) {
	return nil, fmt.Errorf("xhttp: HTTP/3 requires the with_quic build tag")
}
