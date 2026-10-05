//go:build with_openvpn

package include

import (
	"github.com/fig0ut/rostra/adapter/endpoint"
	"github.com/fig0ut/rostra/dns"
	"github.com/fig0ut/rostra/protocol/openvpn"
)

func registerOpenVPNEndpoints(registry *endpoint.Registry) {
	openvpn.RegisterEndpoint(registry)
}

func registerOpenVPNDNSTransport(registry *dns.TransportRegistry) {
	openvpn.RegisterDNSTransport(registry)
}
