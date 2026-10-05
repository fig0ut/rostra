//go:build !with_cloudflared

package include

import (
	"context"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/adapter/inbound"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	E "github.com/sagernet/sing/common/exceptions"
)

func registerCloudflaredInbound(registry *inbound.Registry) {
	inbound.Register[option.CloudflaredInboundOptions](registry, C.TypeCloudflared, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.CloudflaredInboundOptions) (adapter.Inbound, error) {
		return nil, E.New(`Cloudflared is not included in this build, rebuild with -tags with_cloudflared`)
	})
}
