//go:build with_cloudflared

package include

import (
	"github.com/fig0ut/rostra/adapter/inbound"
	"github.com/fig0ut/rostra/protocol/cloudflare"
)

func registerCloudflaredInbound(registry *inbound.Registry) {
	cloudflare.RegisterInbound(registry)
}
