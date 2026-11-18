//go:build !with_openvpn

package include

import (
	"context"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/adapter/endpoint"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/dns"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	E "github.com/sagernet/sing/common/exceptions"
)

func registerOpenVPNEndpoints(registry *endpoint.Registry) {
	endpoint.Register[option.OpenVPNClientEndpointOptions](registry, C.TypeOpenVPNClient, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.OpenVPNClientEndpointOptions) (adapter.Endpoint, error) {
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
	endpoint.Register[option.OpenVPNServerEndpointOptions](registry, C.TypeOpenVPNServer, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.OpenVPNServerEndpointOptions) (adapter.Endpoint, error) {
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
}

func registerOpenVPNDNSTransport(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.OpenVPNDNSServerOptions](registry, C.DNSTypeOpenVPN, func(ctx context.Context, logger log.ContextLogger, tag string, options option.OpenVPNDNSServerOptions) (adapter.DNSTransport, error) {
		return nil, E.New(`OpenVPN is not included in this build, rebuild with -tags with_openvpn`)
	})
}
