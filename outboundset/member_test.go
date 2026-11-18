package outboundset

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	OB "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

type lifecycleOptions struct {
	option.DialerOptions
	Version int  `json:"version"`
	Fail    bool `json:"fail,omitempty"`
}

type lifecycleOutbound struct {
	OB.Adapter
	ctx          context.Context
	scopeContext context.Context
	options      lifecycleOptions
	dependency   adapter.Outbound
	starts       int
	closes       atomic.Int32
	peers        []net.Conn
}

func (o *lifecycleOutbound) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	o.starts++
	if stage == adapter.StartStateInitialize {
		o.scopeContext = scope.Context()
		scope.Add(o.cleanup)
	}
	if stage == adapter.StartStateStart {
		if o.options.Fail {
			return errors.New("startup failed")
		}
		if o.options.Detour != "" {
			var found bool
			o.dependency, found = service.FromContext[adapter.OutboundManager](o.ctx).Outbound(o.options.Detour)
			if !found {
				return errors.New("missing staged dependency")
			}
		}
	}
	return nil
}

func (o *lifecycleOutbound) cleanup() error {
	o.closes.Add(1)
	for _, peer := range o.peers {
		peer.Close()
	}
	return nil
}

func (o *lifecycleOutbound) DialContext(context.Context, string, M.Socksaddr) (net.Conn, error) {
	conn, peer := net.Pipe()
	o.peers = append(o.peers, peer)
	return conn, nil
}

func (o *lifecycleOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return &testPacketConn{}, nil
}

type testPacketConn struct{}

func (*testPacketConn) ReadFrom([]byte) (int, net.Addr, error)    { return 0, nil, net.ErrClosed }
func (*testPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (*testPacketConn) Close() error                              { return nil }
func (*testPacketConn) LocalAddr() net.Addr                       { return &net.UDPAddr{} }
func (*testPacketConn) SetDeadline(time.Time) error               { return nil }
func (*testPacketConn) SetReadDeadline(time.Time) error           { return nil }
func (*testPacketConn) SetWriteDeadline(time.Time) error          { return nil }

func TestGenerationReuseDrainAndRollback(t *testing.T) {
	ctx := service.ContextWithDefaultRegistry(context.Background())
	registry := OB.NewRegistry()
	logger := log.NewNOPFactory().Logger()
	var constructed []*lifecycleOutbound
	OB.Register[lifecycleOptions](registry, "test", func(ctx context.Context, _ adapter.Router, _ log.ContextLogger, tag string, options lifecycleOptions) (adapter.Outbound, error) {
		value := &lifecycleOutbound{Adapter: OB.NewAdapterWithDialerOptions("test", tag, []string{"tcp", "udp"}, options.DialerOptions), ctx: ctx, options: options}
		constructed = append(constructed, value)
		return value, nil
	})
	definitions := func(version int, fail bool) []option.Outbound {
		return []option.Outbound{
			{Type: "test", Tag: "set/child", Options: &lifecycleOptions{Version: 1, DialerOptions: option.DialerOptions{Detour: "set/relay"}}},
			{Type: "test", Tag: "set/spare", Options: &lifecycleOptions{Version: 7}},
			{Type: "test", Tag: "set/relay", Options: &lifecycleOptions{Version: version, Fail: fail}},
		}
	}
	manager := &Manager{baseContext: ctx, registry: registry, logger: logger, outbound: OB.NewManager(registry, nil, "")}
	initial, err := manager.build(ctx, definitions(1, false))
	require.NoError(t, err)
	manager.current = initial
	manager.expanded.Outbounds = definitions(1, false)
	handle := &member{manager: manager, tag: "set/child"}
	tcp, err := handle.DialContext(ctx, "tcp", M.Socksaddr{})
	require.NoError(t, err)
	udp, err := handle.ListenPacket(ctx, M.Socksaddr{})
	require.NoError(t, err)
	// A rejected generation releases its ownership of reused instances and closes
	// all newly constructed ones, including members whose Start was not reached.
	count := len(constructed)
	_, err = manager.build(ctx, definitions(2, true))
	require.ErrorContains(t, err, "startup failed")
	for _, value := range constructed[count:] {
		require.EqualValues(t, 1, value.closes.Load())
		require.ErrorIs(t, value.scopeContext.Err(), context.Canceled)
	}
	for _, value := range initial.order {
		require.Zero(t, value.(*lifecycleOutbound).closes.Load())
	}
	next, err := manager.build(ctx, definitions(2, false))
	require.NoError(t, err)
	require.Same(t, initial.members["set/spare"], next.members["set/spare"])
	require.Equal(t, 4, next.members["set/spare"].(*lifecycleOutbound).starts, "unchanged proxy must not restart")
	require.NotSame(t, initial.members["set/child"], next.members["set/child"], "changed relay rebuilds dependents")
	require.Same(t, next.members["set/relay"], next.members["set/child"].(*lifecycleOutbound).dependency)
	manager.current = next
	initial.retire()
	require.NoError(t, initial.members["set/child"].(*lifecycleOutbound).scopeContext.Err())
	require.Zero(t, initial.members["set/child"].(*lifecycleOutbound).closes.Load())
	require.NoError(t, tcp.Close())
	require.Zero(t, initial.members["set/child"].(*lifecycleOutbound).closes.Load(), "UDP still pins the old generation")
	require.NoError(t, udp.Close())
	require.NoError(t, udp.Close())
	require.EqualValues(t, 1, initial.members["set/child"].(*lifecycleOutbound).closes.Load())
	require.EqualValues(t, 1, initial.members["set/relay"].(*lifecycleOutbound).closes.Load())
	require.Zero(t, next.members["set/spare"].(*lifecycleOutbound).closes.Load())
	require.ErrorIs(t, initial.members["set/child"].(*lifecycleOutbound).scopeContext.Err(), context.Canceled)
	require.NoError(t, next.members["set/spare"].(*lifecycleOutbound).scopeContext.Err())
	next.retire()
	for _, value := range next.order {
		require.EqualValues(t, 1, value.(*lifecycleOutbound).closes.Load())
	}
}

type flowTestOutbound struct {
	lifecycleOutbound
	updates  int
	keepIdle bool
}

func (o *flowTestOutbound) SetKeepIdleConnections(keep bool) { o.keepIdle = keep }
func (*flowTestOutbound) CloseIdleConnections()              {}

func (o *flowTestOutbound) InterfaceUpdated(context.Context) { o.updates++ }
func (o *flowTestOutbound) PreferredDomain(_ *adapter.InboundContext, domain string) bool {
	return domain == "example.com"
}

func (o *flowTestOutbound) PreferredAddress(_ *adapter.InboundContext, address netip.Addr) bool {
	return address.Is4()
}

func (o *flowTestOutbound) PreMatchFlow(network string, _ netip.Addr) adapter.PreMatchAction {
	if network == "icmp" {
		return adapter.PreMatchFlow
	}
	return adapter.PreMatchContinue
}

func (*flowTestOutbound) PortAddresses() (netip.Addr, netip.Addr) { return netip.Addr{}, netip.Addr{} }
func (*flowTestOutbound) PortMTU() uint32                         { return 1500 }
func (*flowTestOutbound) AttachReturn(tun.Return) error           { return nil }
func (*flowTestOutbound) DetachReturn(tun.Return) error           { return nil }
func (*flowTestOutbound) WritePackets([][]byte) error             { return nil }

func TestMemberPreservesFlowAndNetworkCapabilities(t *testing.T) {
	makeGeneration := func() (*generation, *flowTestOutbound) {
		value := &flowTestOutbound{lifecycleOutbound: lifecycleOutbound{Adapter: OB.NewAdapter("test", "set/node", []string{"tcp", "icmp"}, nil)}}
		scope := adapter.NewScope(context.Background(), log.NewNOPFactory().Logger())
		scope.Add(value.cleanup)
		g := &generation{
			members:   map[string]adapter.Outbound{"set/node": value},
			instances: map[string]*ownedInstance{"set/node": {value: value, owners: 1, scope: scope}},
			order:     []adapter.Outbound{value},
		}
		return g, value
	}
	initial, old := makeGeneration()
	manager := &Manager{current: initial}
	handle := &member{manager: manager, tag: "set/node"}
	address := netip.MustParseAddr("192.0.2.1")
	require.True(t, handle.PreferredRoutesSupported())
	require.True(t, handle.PreferredDomain(nil, "example.com"))
	require.True(t, handle.PreferredAddress(nil, address))
	handle.InterfaceUpdated(context.Background())
	require.Equal(t, 1, old.updates)
	require.Nil(t, handle.FlowOutbound("tcp", address))
	require.Zero(t, initial.refs)
	require.Same(t, old, handle.FlowOutbound("icmp", address))
	require.Same(t, old, handle.FlowOutbound("icmp", address))
	require.Equal(t, 1, initial.refs, "a port snapshot pins its generation once")
	next, current := makeGeneration()
	manager.current = next
	initial.retire()
	require.Zero(t, old.closes.Load(), "TUN may still use the original port")
	require.Same(t, current, handle.FlowOutbound("icmp", address))
	handle.InterfaceUpdated(context.Background())
	require.Equal(t, 1, current.updates)
	require.Equal(t, 1, old.updates)
	initial.close()
	next.close()
	require.EqualValues(t, 1, old.closes.Load())
	require.EqualValues(t, 1, current.closes.Load())
}

func TestMemberRetainsIdlePoolPolicy(t *testing.T) {
	old := &flowTestOutbound{keepIdle: true}
	next := &flowTestOutbound{keepIdle: true}
	manager := &Manager{current: &generation{members: map[string]adapter.Outbound{"set/node": old}}}
	handle := &member{manager: manager, tag: "set/node"}
	handle.SetKeepIdleConnections(false)
	require.False(t, old.keepIdle)
	manager.current = &generation{members: map[string]adapter.Outbound{"set/node": next}}
	handle.applyIdlePolicy()
	require.False(t, next.keepIdle, "replacement inherits policy cached against the stable handle")
	handle.SetKeepIdleConnections(true)
	require.True(t, next.keepIdle)
	require.False(t, old.keepIdle)
}

type earlyTestConn struct {
	net.Conn
	pending bool
}

func (c *earlyTestConn) NeedHandshakeForRead() bool  { return c.pending }
func (c *earlyTestConn) NeedHandshakeForWrite() bool { return c.pending }

func TestTrackedConnectionPreservesHandshake(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	raw := &earlyTestConn{Conn: conn, pending: true}
	releases := 0
	tracked := &trackedConn{Conn: raw, release: func() { releases++ }}
	require.True(t, N.NeedHandshakeForRead(tracked))
	require.True(t, N.NeedHandshakeForWrite(tracked))
	raw.pending = false
	require.False(t, N.NeedHandshakeForRead(tracked))
	require.False(t, N.NeedHandshakeForWrite(tracked))
	require.NoError(t, tracked.Close())
	require.NoError(t, tracked.Close())
	require.Equal(t, 1, releases)
}
