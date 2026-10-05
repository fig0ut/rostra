package include

import (
	"context"

	"github.com/fig0ut/rostra"
	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/adapter/certificate"
	"github.com/fig0ut/rostra/adapter/endpoint"
	"github.com/fig0ut/rostra/adapter/inbound"
	"github.com/fig0ut/rostra/adapter/outbound"
	"github.com/fig0ut/rostra/adapter/service"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/dns"
	"github.com/fig0ut/rostra/dns/transport"
	"github.com/fig0ut/rostra/dns/transport/fakeip"
	"github.com/fig0ut/rostra/dns/transport/hosts"
	"github.com/fig0ut/rostra/dns/transport/local"
	"github.com/fig0ut/rostra/dns/transport/mdns"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"
	"github.com/fig0ut/rostra/protocol/anytls"
	"github.com/fig0ut/rostra/protocol/block"
	"github.com/fig0ut/rostra/protocol/bridge"
	"github.com/fig0ut/rostra/protocol/direct"
	"github.com/fig0ut/rostra/protocol/group"
	"github.com/fig0ut/rostra/protocol/http"
	"github.com/fig0ut/rostra/protocol/masque"
	"github.com/fig0ut/rostra/protocol/mixed"
	"github.com/fig0ut/rostra/protocol/naive"
	"github.com/fig0ut/rostra/protocol/redirect"
	"github.com/fig0ut/rostra/protocol/shadowsocks"
	"github.com/fig0ut/rostra/protocol/shadowtls"
	"github.com/fig0ut/rostra/protocol/snell"
	"github.com/fig0ut/rostra/protocol/socks"
	"github.com/fig0ut/rostra/protocol/ssh"
	"github.com/fig0ut/rostra/protocol/tor"
	"github.com/fig0ut/rostra/protocol/trojan"
	"github.com/fig0ut/rostra/protocol/tun"
	"github.com/fig0ut/rostra/protocol/vless"
	"github.com/fig0ut/rostra/protocol/vmess"
	"github.com/fig0ut/rostra/service/api"
	originca "github.com/fig0ut/rostra/service/origin_ca"
	"github.com/fig0ut/rostra/service/resolved"
	"github.com/fig0ut/rostra/service/ssmapi"

	E "github.com/sagernet/sing/common/exceptions"
)

func Context(ctx context.Context) context.Context {
	return box.Context(ctx, InboundRegistry(), OutboundRegistry(), EndpointRegistry(), DNSTransportRegistry(), ServiceRegistry(), CertificateProviderRegistry())
}

func InboundRegistry() *inbound.Registry {
	registry := inbound.NewRegistry()

	tun.RegisterInbound(registry)
	redirect.RegisterRedirect(registry)
	redirect.RegisterTProxy(registry)
	direct.RegisterInbound(registry)

	socks.RegisterInbound(registry)
	http.RegisterInbound(registry)
	mixed.RegisterInbound(registry)

	shadowsocks.RegisterInbound(registry)
	snell.RegisterInbound(registry)
	vmess.RegisterInbound(registry)
	trojan.RegisterInbound(registry)
	naive.RegisterInbound(registry)
	shadowtls.RegisterInbound(registry)
	vless.RegisterInbound(registry)
	anytls.RegisterInbound(registry)

	registerQUICInbounds(registry)
	registerCloudflaredInbound(registry)
	registerTailcatInbound(registry)
	registerStubForRemovedInbounds(registry)

	return registry
}

func OutboundRegistry() *outbound.Registry {
	registry := outbound.NewRegistry()

	direct.RegisterOutbound(registry)
	bridge.RegisterOutbound(registry)

	block.RegisterOutbound(registry)

	group.RegisterSelector(registry)
	group.RegisterURLTest(registry)
	group.RegisterLoadBalance(registry)

	socks.RegisterOutbound(registry)
	http.RegisterOutbound(registry)
	shadowsocks.RegisterOutbound(registry)
	snell.RegisterOutbound(registry)
	vmess.RegisterOutbound(registry)
	trojan.RegisterOutbound(registry)
	registerNaiveOutbound(registry)
	tor.RegisterOutbound(registry)
	ssh.RegisterOutbound(registry)
	shadowtls.RegisterOutbound(registry)
	vless.RegisterOutbound(registry)
	anytls.RegisterOutbound(registry)

	registerQUICOutbounds(registry)
	registerTailcatOutbound(registry)
	registerStubForRemovedOutbounds(registry)

	return registry
}

func EndpointRegistry() *endpoint.Registry {
	registry := endpoint.NewRegistry()

	registerWireGuardEndpoint(registry)
	registerOpenConnectEndpoint(registry)
	registerOpenVPNEndpoints(registry)
	masque.RegisterEndpoint(registry)
	registerTailscaleEndpoint(registry)

	return registry
}

func DNSTransportRegistry() *dns.TransportRegistry {
	registry := dns.NewTransportRegistry()

	transport.RegisterTCP(registry)
	transport.RegisterUDP(registry)
	transport.RegisterTLS(registry)
	transport.RegisterHTTPS(registry)
	hosts.RegisterTransport(registry)
	local.RegisterTransport(registry)
	mdns.RegisterTransport(registry)
	fakeip.RegisterTransport(registry)
	resolved.RegisterTransport(registry)

	registerQUICTransports(registry)
	registerDHCPTransport(registry)
	registerTailscaleTransport(registry)
	registerOpenConnectDNSTransport(registry)
	registerOpenVPNDNSTransport(registry)

	return registry
}

func ServiceRegistry() *service.Registry {
	registry := service.NewRegistry()

	api.RegisterService(registry)
	resolved.RegisterService(registry)
	ssmapi.RegisterService(registry)

	registerQUICServices(registry)
	registerDERPService(registry)
	registerCCMService(registry)
	registerOCMService(registry)
	registerOOMKillerService(registry)
	registerUSBIPServices(registry)

	return registry
}

func CertificateProviderRegistry() *certificate.Registry {
	registry := certificate.NewRegistry()

	registerACMECertificateProvider(registry)
	registerTailscaleCertificateProvider(registry)
	originca.RegisterCertificateProvider(registry)

	return registry
}

func registerStubForRemovedInbounds(registry *inbound.Registry) {
	inbound.Register[option.ShadowsocksInboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (adapter.Inbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in sing-box 1.6.0")
	})
}

func registerStubForRemovedOutbounds(registry *outbound.Registry) {
	outbound.Register[option.ShadowsocksROutboundOptions](registry, C.TypeShadowsocksR, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksROutboundOptions) (adapter.Outbound, error) {
		return nil, E.New("ShadowsocksR is deprecated and removed in sing-box 1.6.0")
	})
	outbound.Register[option.StubOptions](registry, C.TypeWireGuard, func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.StubOptions) (adapter.Outbound, error) {
		return nil, E.New("WireGuard outbound is deprecated in sing-box 1.11.0 and removed in sing-box 1.13.0, use WireGuard endpoint instead")
	})
}
