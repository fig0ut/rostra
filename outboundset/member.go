package outboundset

import (
	"context"
	"net"
	"net/netip"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// A generation owns the concrete proxy instances. Stable member handles switch
// generations while connections retain the generation they were opened with.
type generation struct {
	members    map[string]adapter.Outbound
	instances  map[string]*ownedInstance
	order      []adapter.Outbound
	access     sync.Mutex
	refs       int
	retired    bool
	once       sync.Once
	onClose    func()
	flowPinned bool
}

// Unchanged members may be shared by successive generations. A concrete
// instance closes only after the last generation that owns it has drained.
type ownedInstance struct {
	value  adapter.Outbound
	scope  *adapter.Scope
	access sync.Mutex
	owners int
}

func (i *ownedInstance) retain() {
	i.access.Lock()
	i.owners++
	i.access.Unlock()
}

func (i *ownedInstance) release() {
	i.access.Lock()
	i.owners--
	closeNow := i.owners == 0
	i.access.Unlock()
	if closeNow {
		_ = i.scope.Close()
	}
}

type generationKey struct{}

func (g *generation) release() {
	g.access.Lock()
	g.refs--
	closeNow := g.retired && g.refs == 0
	g.access.Unlock()
	if closeNow {
		g.close()
	}
}

func (g *generation) retire() {
	g.access.Lock()
	g.retired = true
	// Keep the generation alive while idle pools release their own detours.
	g.refs++
	g.access.Unlock()
	for _, value := range g.order {
		instance := g.instances[value.Tag()]
		instance.access.Lock()
		shared := instance.owners > 1
		instance.access.Unlock()
		if shared {
			continue
		}
		if keeper, ok := value.(adapter.IdleConnectionKeeper); ok {
			keeper.SetKeepIdleConnections(false)
			keeper.CloseIdleConnections()
		}
	}
	g.release()
}

func (g *generation) close() {
	g.once.Do(func() {
		for _, outbound := range slices.Backward(g.order) {
			g.instances[outbound.Tag()].release()
		}
		if g.onClose != nil {
			g.onClose()
		}
	})
}

var _ adapter.Lifecycle = (*member)(nil)

type member struct {
	manager      *Manager
	tag          string
	policyAccess sync.Mutex
	keepIdle     *bool
}

func (m *member) Tag() string { return m.tag }
func (m *member) current() adapter.Outbound {
	m.manager.currentAccess.RLock()
	defer m.manager.currentAccess.RUnlock()
	return m.manager.current.members[m.tag]
}

func (m *member) Type() string {
	if value := m.current(); value != nil {
		return value.Type()
	}
	return "removed"
}

func (m *member) Network() []string {
	if value := m.current(); value != nil {
		return value.Network()
	}
	return nil
}

func (m *member) Dependencies() []string {
	if value := m.current(); value != nil {
		return value.Dependencies()
	}
	return nil
}

func (m *member) MultiplexEnabled() bool {
	value, ok := m.current().(adapter.OutboundWithMultiplex)
	return ok && value.MultiplexEnabled()
}

func (m *member) IsEmpty() bool {
	value, ok := m.current().(interface{ IsEmpty() bool })
	return ok && value.IsEmpty()
}

func (m *member) SetKeepIdleConnections(keep bool) {
	m.policyAccess.Lock()
	defer m.policyAccess.Unlock()
	m.keepIdle = &keep
	m.applyIdlePolicyLocked()
}

func (m *member) applyIdlePolicy() {
	m.policyAccess.Lock()
	defer m.policyAccess.Unlock()
	m.applyIdlePolicyLocked()
}

func (m *member) applyIdlePolicyLocked() {
	if m.keepIdle == nil {
		return
	}
	_, raw, release, err := m.acquire(context.Background())
	if err != nil {
		return
	}
	defer release()
	if value, ok := raw.(adapter.IdleConnectionKeeper); ok {
		value.SetKeepIdleConnections(*m.keepIdle)
	}
}

func (m *member) CloseIdleConnections() {
	_, raw, release, err := m.acquire(context.Background())
	if err != nil {
		return
	}
	defer release()
	if value, ok := raw.(adapter.IdleConnectionKeeper); ok {
		value.CloseIdleConnections()
	}
}

// The set manager owns the concrete instance's scope independently of the
// stable handle's parent scope, so retirement can wait for active connections.
func (m *member) Start(stage adapter.StartStage, _ *adapter.Scope) error {
	return m.manager.current.instances[m.tag].start(stage)
}

func (i *ownedInstance) start(stage adapter.StartStage) error {
	if lifecycle, ok := i.value.(adapter.Lifecycle); ok {
		return i.scope.Start("outbound/"+i.value.Type()+"["+i.value.Tag()+"]", lifecycle, stage)
	}
	return nil
}

func (m *member) InterfaceUpdated(ctx context.Context) {
	ctx, value, release, err := m.acquire(ctx)
	if err != nil {
		return
	}
	defer release()
	if listener, ok := value.(adapter.InterfaceUpdateListener); ok {
		listener.InterfaceUpdated(ctx)
	}
}

func (m *member) PreferredRoutesSupported() bool {
	_, ok := m.current().(adapter.OutboundWithPreferredRoutes)
	return ok
}

func (m *member) PreferredDomain(metadata *adapter.InboundContext, domain string) bool {
	_, value, release, err := m.acquire(context.Background())
	if err != nil {
		return false
	}
	defer release()
	routes, ok := value.(adapter.OutboundWithPreferredRoutes)
	return ok && routes.PreferredDomain(metadata, domain)
}

func (m *member) PreferredAddress(metadata *adapter.InboundContext, address netip.Addr) bool {
	_, value, release, err := m.acquire(context.Background())
	if err != nil {
		return false
	}
	defer release()
	routes, ok := value.(adapter.OutboundWithPreferredRoutes)
	return ok && routes.PreferredAddress(metadata, address)
}

func (m *member) FlowOutbound(network string, destination netip.Addr) adapter.FlowOutbound {
	m.manager.currentAccess.RLock()
	defer m.manager.currentAccess.RUnlock()
	if m.manager.closed {
		return nil
	}
	g := m.manager.current
	value, ok := g.members[m.tag].(adapter.FlowOutbound)
	if !ok || value.PreMatchFlow(network, destination) == adapter.PreMatchContinue {
		return nil
	}
	// sing-tun caches ports and detaches their return paths only when its
	// dispatcher closes. Keep the concrete port (and its dependencies) alive
	// until shutdown instead of switching an existing NAT table's backend.
	g.access.Lock()
	if !g.flowPinned {
		g.flowPinned = true
		g.refs++
	}
	g.access.Unlock()
	return value
}

func (m *member) acquire(ctx context.Context) (context.Context, adapter.Outbound, func(), error) {
	m.manager.currentAccess.RLock()
	defer m.manager.currentAccess.RUnlock()
	if m.manager.closed {
		return ctx, nil, nil, os.ErrClosed
	}
	g := m.manager.current
	value := g.members[m.tag]
	if value == nil {
		return ctx, nil, nil, os.ErrNotExist
	}
	if ctx.Value(generationKey{}) == g {
		return ctx, value, func() {}, nil
	}
	g.access.Lock()
	g.refs++
	g.access.Unlock()
	return context.WithValue(ctx, generationKey{}, g), value, g.release, nil
}

func (m *member) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	ctx, value, release, err := m.acquire(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := value.DialContext(ctx, network, destination)
	if err != nil {
		release()
		return nil, err
	}
	return &trackedConn{Conn: conn, release: release}, nil
}

func (m *member) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ctx, value, release, err := m.acquire(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := value.ListenPacket(ctx, destination)
	if err != nil {
		release()
		return nil, err
	}
	return &trackedPacketConn{PacketConn: conn, release: release}, nil
}

func (m *member) DialParallel(ctx context.Context, network string, destination M.Socksaddr, addresses []netip.Addr) (net.Conn, error) {
	ctx, value, release, err := m.acquire(ctx)
	if err != nil {
		return nil, err
	}
	var conn net.Conn
	if parallel, ok := value.(N.ParallelDialer); ok {
		conn, err = parallel.DialParallel(ctx, network, destination, addresses)
	} else {
		conn, err = N.DialSerial(ctx, value, network, destination, addresses)
	}
	if err != nil {
		release()
		return nil, err
	}
	return &trackedConn{Conn: conn, release: release}, nil
}

func (m *member) DialParallelNetwork(ctx context.Context, network string, destination M.Socksaddr, addresses []netip.Addr, strategy *C.NetworkStrategy, interfaceType []C.InterfaceType, fallbackInterfaceType []C.InterfaceType, fallbackDelay time.Duration) (net.Conn, error) {
	ctx, value, release, err := m.acquire(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := dialer.DialSerialNetwork(ctx, value, network, destination, addresses, strategy, interfaceType, fallbackInterfaceType, fallbackDelay)
	if err != nil {
		release()
		return nil, err
	}
	return &trackedConn{Conn: conn, release: release}, nil
}

func (m *member) ListenSerialNetworkPacket(ctx context.Context, destination M.Socksaddr, addresses []netip.Addr, strategy *C.NetworkStrategy, interfaceType []C.InterfaceType, fallbackInterfaceType []C.InterfaceType, fallbackDelay time.Duration) (net.PacketConn, netip.Addr, error) {
	ctx, value, release, err := m.acquire(ctx)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	conn, address, err := dialer.ListenSerialNetworkPacket(ctx, value, destination, addresses, strategy, interfaceType, fallbackInterfaceType, fallbackDelay)
	if err != nil {
		release()
		return nil, netip.Addr{}, err
	}
	return &trackedPacketConn{PacketConn: conn, release: release}, address, nil
}

type trackedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// Do not expose Upstream: copy optimizations must retain the Close hook.
func (c *trackedConn) CloseRead() error            { return N.CloseRead(c.Conn) }
func (c *trackedConn) CloseWrite() error           { return N.CloseWrite(c.Conn) }
func (c *trackedConn) NeedHandshakeForRead() bool  { return N.NeedHandshakeForRead(c.Conn) }
func (c *trackedConn) NeedHandshakeForWrite() bool { return N.NeedHandshakeForWrite(c.Conn) }

type trackedPacketConn struct {
	net.PacketConn
	once    sync.Once
	release func()
}

func (c *trackedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.release)
	return err
}

func (c *trackedPacketConn) NeedHandshakeForRead() bool {
	return N.NeedHandshakeForReadAny(c.PacketConn)
}

func (c *trackedPacketConn) NeedHandshakeForWrite() bool {
	return N.NeedHandshakeForWriteAny(c.PacketConn)
}

var _ N.Dialer = (*member)(nil)
