//go:build with_quic

package include

import (
	"github.com/fig0ut/rostra/adapter/inbound"
	"github.com/fig0ut/rostra/adapter/outbound"
	"github.com/fig0ut/rostra/adapter/service"
	"github.com/fig0ut/rostra/dns"
	"github.com/fig0ut/rostra/dns/transport/quic"
	"github.com/fig0ut/rostra/protocol/hysteria"
	"github.com/fig0ut/rostra/protocol/hysteria2"
	_ "github.com/fig0ut/rostra/protocol/naive/quic"
	"github.com/fig0ut/rostra/protocol/tuic"
	_ "github.com/fig0ut/rostra/transport/v2rayquic"
)

func registerQUICInbounds(registry *inbound.Registry) {
	hysteria.RegisterInbound(registry)
	tuic.RegisterInbound(registry)
	hysteria2.RegisterInbound(registry)
}

func registerQUICOutbounds(registry *outbound.Registry) {
	hysteria.RegisterOutbound(registry)
	tuic.RegisterOutbound(registry)
	hysteria2.RegisterOutbound(registry)
}

func registerQUICTransports(registry *dns.TransportRegistry) {
	quic.RegisterTransport(registry)
	quic.RegisterHTTP3Transport(registry)
}

func registerQUICServices(registry *service.Registry) {
	hysteria2.RegisterRealmService(registry)
}
