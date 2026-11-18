//go:build with_openconnect

package include

import (
	"github.com/fig0ut/rostra/adapter/endpoint"
	"github.com/fig0ut/rostra/dns"
	"github.com/fig0ut/rostra/protocol/openconnect"
)

func registerOpenConnectEndpoint(registry *endpoint.Registry) {
	openconnect.RegisterEndpoint(registry)
}

func registerOpenConnectDNSTransport(registry *dns.TransportRegistry) {
	openconnect.RegisterDNSTransport(registry)
}
