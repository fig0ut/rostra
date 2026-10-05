//go:build with_dhcp

package include

import (
	"github.com/fig0ut/rostra/dns"
	"github.com/fig0ut/rostra/dns/transport/dhcp"
)

func registerDHCPTransport(registry *dns.TransportRegistry) {
	dhcp.RegisterTransport(registry)
}
