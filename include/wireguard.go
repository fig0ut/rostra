//go:build with_wireguard

package include

import (
	"github.com/fig0ut/rostra/adapter/endpoint"
	"github.com/fig0ut/rostra/protocol/wireguard"
)

func registerWireGuardEndpoint(registry *endpoint.Registry) {
	wireguard.RegisterEndpoint(registry)
}
