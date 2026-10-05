//go:build with_tailscale

package include

import (
	"github.com/fig0ut/rostra/adapter/certificate"
	"github.com/fig0ut/rostra/adapter/endpoint"
	"github.com/fig0ut/rostra/adapter/inbound"
	"github.com/fig0ut/rostra/adapter/outbound"
	"github.com/fig0ut/rostra/adapter/service"
	"github.com/fig0ut/rostra/dns"
	"github.com/fig0ut/rostra/protocol/tailscale"
	"github.com/fig0ut/rostra/service/derp"
)

func registerTailscaleEndpoint(registry *endpoint.Registry) {
	tailscale.RegisterEndpoint(registry)
}

func registerTailcatInbound(registry *inbound.Registry) {
	tailscale.RegisterTailcatInbound(registry)
}

func registerTailcatOutbound(registry *outbound.Registry) {
	tailscale.RegisterTailcatOutbound(registry)
}

func registerTailscaleTransport(registry *dns.TransportRegistry) {
	tailscale.RegistryTransport(registry)
}

func registerTailscaleCertificateProvider(registry *certificate.Registry) {
	tailscale.RegisterCertificateProvider(registry)
}

func registerDERPService(registry *service.Registry) {
	derp.Register(registry)
}
