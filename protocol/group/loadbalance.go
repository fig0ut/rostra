package group

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

func RegisterLoadBalance(registry *outbound.Registry) {
	outbound.Register[option.LoadBalanceOutboundOptions](registry, C.TypeLoadBalance, NewLoadBalance)
}

var (
	_ adapter.ConnectionSelectingOutboundGroup = (*LoadBalance)(nil)
	_ adapter.OutboundGroupChangeUpdater       = (*LoadBalance)(nil)
	_ adapter.URLTestGroup                     = (*LoadBalance)(nil)
	_ adapter.Referrer                         = (*LoadBalance)(nil)
	_ adapter.InterfaceUpdateListener          = (*LoadBalance)(nil)
)

const (
	balanceConsistentHash    = "consistent_hash"
	balanceRoundRobin        = "round_robin"
	balanceSourceIPDomain    = "source_ip_domain"
	balanceSourceIPHost      = "source_ip_host"
	balanceDestinationDomain = "destination_domain"
	balanceUnknown           = "unknown"
	balanceHealthy           = "healthy"
	balanceUnhealthy         = "unhealthy"
)

// Membership snapshots are immutable. A replacement gets a new entry even when
// its outbound-set handle is unchanged, so old probes cannot update its health.
type loadBalanceMember struct {
	outbound adapter.Outbound
	tag      string
	health   atomic.Pointer[loadBalanceHealth]
	wake     chan struct{}
}

// A probe result applies to the selected path it actually tested.
type loadBalanceHealth struct {
	state LoadBalanceHealth
	path  []adapter.Outbound
}

type LoadBalanceHealth struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
	Delay     uint16    `json:"delay,omitempty"`
}

type LoadBalance struct {
	outbound.Adapter
	ctx         context.Context
	cancel      context.CancelFunc
	manager     adapter.OutboundManager
	history     *urltest.HistoryStorage
	tags        []string
	strategy    string
	hashKey     string
	link        string
	interval    time.Duration
	idleTimeout time.Duration
	access      sync.RWMutex
	members     []*loadBalanceMember
	byTag       map[string]*loadBalanceMember
	maxTagLen   int
	nextTCP     uint64
	nextUDP     uint64
	lastActive  time.Time
	probeGate   chan struct{}
	wake        chan struct{}
	workers     sync.WaitGroup
}

func NewLoadBalance(ctx context.Context, _ adapter.Router, _ log.ContextLogger, tag string, options option.LoadBalanceOutboundOptions) (adapter.Outbound, error) {
	if len(options.Outbounds) == 0 {
		return nil, E.New("missing tags")
	}
	strategy := options.Strategy
	if strategy == "" {
		strategy = balanceConsistentHash
	}
	if strategy != balanceConsistentHash && strategy != balanceRoundRobin {
		return nil, E.New("unknown loadbalance strategy: ", strategy)
	}
	hashKey := options.HashKey
	if strategy == balanceRoundRobin && hashKey != "" {
		return nil, E.New("hash_key requires consistent_hash strategy")
	}
	if hashKey == "" {
		hashKey = balanceSourceIPDomain
	}
	switch hashKey {
	case balanceSourceIPDomain, balanceSourceIPHost, balanceDestinationDomain:
	default:
		return nil, E.New("unknown loadbalance hash_key: ", hashKey)
	}
	interval, idleTimeout := time.Duration(options.Interval), time.Duration(options.IdleTimeout)
	if interval < 0 || idleTimeout < 0 {
		return nil, E.New("interval and idle_timeout must not be negative")
	}
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	ctx, cancel := context.WithCancel(ctx)
	return &LoadBalance{
		Adapter:     outbound.NewAdapter(C.TypeLoadBalance, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:         ctx,
		cancel:      cancel,
		manager:     service.FromContext[adapter.OutboundManager](ctx),
		history:     service.PtrFromContext[urltest.HistoryStorage](ctx),
		tags:        slices.Clone(options.Outbounds),
		strategy:    strategy,
		hashKey:     hashKey,
		link:        options.URL,
		interval:    interval,
		idleTimeout: idleTimeout,
		byTag:       make(map[string]*loadBalanceMember),
		probeGate:   make(chan struct{}, 1),
		wake:        make(chan struct{}, 1),
		lastActive:  time.Now(),
	}, nil
}

func (b *LoadBalance) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	switch stage {
	case adapter.StartStateInitialize:
		scope.Add(b.close)
	case adapter.StartStateStart:
		var members []adapter.Outbound
		for _, tag := range b.tags {
			value, found := b.manager.Outbound(tag)
			if !found {
				return E.New("outbound not found: ", tag)
			}
			members = append(members, value)
		}
		b.UpdateOutbounds(members)
	case adapter.StartStateStarted:
		b.workers.Add(1)
		go b.loop()
	}
	return nil
}

func (b *LoadBalance) close() error {
	b.cancel()
	b.workers.Wait()
	// Wait for an active manual probe too. New calls observe cancellation.
	b.probeGate <- struct{}{}
	<-b.probeGate
	return nil
}

func (b *LoadBalance) All() []string {
	b.access.RLock()
	defer b.access.RUnlock()
	return slices.Clone(b.tags)
}

func (b *LoadBalance) Dependencies() []string {
	return b.All()
}

func (b *LoadBalance) References() []string {
	return b.All()
}

// A balancer has no global selection. Inspection never consumes a turn.
func (*LoadBalance) Selected(string) adapter.Outbound {
	return nil
}

func (*LoadBalance) AttachConnection(io.Closer) func() {
	return func() {}
}

func (b *LoadBalance) Health() map[string]LoadBalanceHealth {
	b.access.RLock()
	members := b.members
	b.access.RUnlock()
	result := make(map[string]LoadBalanceHealth, len(members))
	for _, member := range members {
		health := member.health.Load()
		if health.state.Status != balanceUnknown && !balanceProbePathMatches(member.outbound, health.path) {
			result[member.tag] = LoadBalanceHealth{Status: balanceUnknown}
		} else {
			result[member.tag] = health.state
		}
	}
	return result
}

func (b *LoadBalance) UpdateOutbounds(members []adapter.Outbound) {
	b.UpdateOutboundsWithChanges(members, nil)
}

func (b *LoadBalance) UpdateOutboundsWithChanges(members []adapter.Outbound, changed []string) {
	changedTags := make(map[string]struct{}, len(changed))
	for _, tag := range changed {
		changedTags[tag] = struct{}{}
	}
	b.access.Lock()
	// The set manager notifies every group. Avoid rebuilding and probing groups
	// whose ordered membership and underlying instances have not changed.
	unchanged := len(members) == len(b.members)
	if unchanged {
		for i, member := range members {
			_, replaced := changedTags[member.Tag()]
			if replaced || b.members[i].outbound != member {
				unchanged = false
				break
			}
		}
	}
	if unchanged {
		b.access.Unlock()
		return
	}
	next := make([]*loadBalanceMember, 0, len(members))
	byTag := make(map[string]*loadBalanceMember, len(members))
	tags := make([]string, 0, len(members))
	maxTagLen := 0
	for _, outbound := range members {
		tag := outbound.Tag()
		if _, exists := byTag[tag]; exists {
			continue
		}
		member := b.byTag[tag]
		_, replaced := changedTags[tag]
		if member == nil || member.outbound != outbound || replaced {
			member = &loadBalanceMember{outbound: outbound, tag: tag, wake: b.wake}
			member.health.Store(&loadBalanceHealth{state: LoadBalanceHealth{Status: balanceUnknown}})
		}
		next = append(next, member)
		byTag[tag] = member
		tags = append(tags, tag)
		maxTagLen = max(maxTagLen, len(tag))
	}
	b.members, b.byTag, b.tags, b.maxTagLen = next, byTag, tags, maxTagLen
	b.access.Unlock()
	b.signal()
}

func (b *LoadBalance) SelectForConnection(metadata *adapter.InboundContext) (adapter.Outbound, error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	network := N.NetworkName(metadata.Network)
	if network != N.NetworkTCP && network != N.NetworkUDP {
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	b.access.Lock()
	now := time.Now()
	if now.Sub(b.lastActive) > b.idleTimeout {
		b.signal()
	}
	b.lastActive = now
	if b.strategy == balanceRoundRobin {
		selected := b.selectRoundRobinLocked(network)
		b.access.Unlock()
		if selected != nil {
			return selected, nil
		}
	} else {
		members, maxTagLen := b.members, b.maxTagLen
		b.access.Unlock()
		key := balanceKey(metadata, b.hashKey)
		if selected := selectRendezvous(members, network, key, maxTagLen); selected != nil {
			return selected, nil
		}
	}
	return nil, E.New("loadbalance[", b.Tag(), "]: no eligible ", network, " outbound")
}

func (b *LoadBalance) selectRoundRobinLocked(network string) adapter.Outbound {
	counter := &b.nextTCP
	if network == N.NetworkUDP {
		counter = &b.nextUDP
	}
	for range b.members {
		index := *counter % uint64(len(b.members))
		*counter = index + 1
		member := b.members[index]
		if member.eligible(network) {
			return member.outbound
		}
	}
	return nil
}

func (m *loadBalanceMember) eligible(network string) bool {
	// Network capabilities may change for nested selectors and stable set handles.
	health := m.health.Load()
	status := health.state.Status
	if status != balanceUnknown && !balanceProbePathMatches(m.outbound, health.path) {
		status = balanceUnknown
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	return status != balanceUnhealthy && slices.Contains(m.outbound.Network(), network)
}

func selectRendezvous(members []*loadBalanceMember, network, key string, maxTagLen int) adapter.Outbound {
	var storage [512]byte
	payload := storage[:0]
	if size := len(key) + 1 + maxTagLen; size > cap(payload) {
		payload = make([]byte, 0, size)
	}
	payload = append(payload, key...)
	payload = append(payload, 0)
	prefixLen := len(payload)
	var selected *loadBalanceMember
	var best uint64
	for _, member := range members {
		if !member.eligible(network) {
			continue
		}
		payload = append(payload[:prefixLen], member.tag...)
		sum := sha256.Sum256(payload)
		score := binary.BigEndian.Uint64(sum[:8])
		if selected == nil || score > best || (score == best && member.tag < selected.tag) {
			selected, best = member, score
		}
	}
	if selected == nil {
		return nil
	}
	return selected.outbound
}

func balanceKey(metadata *adapter.InboundContext, mode string) string {
	host := metadata.Destination.Fqdn
	if host == "" {
		host = metadata.Domain
	}
	if host == "" {
		host = metadata.Destination.Addr.Unmap().String()
	}
	host = normalizeBalanceHost(host, mode != balanceSourceIPHost)
	if mode == balanceDestinationDomain {
		return host
	}
	var source string
	if metadata.Source.Addr.IsValid() {
		source = metadata.Source.Addr.Unmap().String()
	}
	return source + "\x00" + host
}

func normalizeBalanceHost(host string, registrable bool) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	// IP literals must not be interpreted as domain names by the suffix list.
	if address, err := netip.ParseAddr(host); err == nil {
		host = address.Unmap().String()
	} else {
		if ascii, err := idna.Lookup.ToASCII(host); err == nil {
			host = strings.TrimSuffix(ascii, ".")
		}
		if registrable {
			if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
				host = domain
			}
		}
	}
	return host
}

func balanceMetadata(ctx context.Context, network string, destination M.Socksaddr) adapter.InboundContext {
	var metadata adapter.InboundContext
	if original := adapter.ContextFrom(ctx); original != nil {
		metadata = *original
	}
	metadata.Network, metadata.Destination = network, destination
	return metadata
}

func (b *LoadBalance) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	metadata := balanceMetadata(ctx, network, destination)
	selected, err := b.SelectForConnection(&metadata)
	if err != nil {
		return nil, err
	}
	return selected.DialContext(ctx, network, destination)
}

func (b *LoadBalance) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	metadata := balanceMetadata(ctx, N.NetworkUDP, destination)
	selected, err := b.SelectForConnection(&metadata)
	if err != nil {
		return nil, err
	}
	return selected.ListenPacket(ctx, destination)
}

func (b *LoadBalance) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *LoadBalance) InterfaceUpdated(context.Context) {
	b.signal()
}

func (b *LoadBalance) PerformUpdateCheck() {
	b.signal()
}

func (b *LoadBalance) loop() {
	defer b.workers.Done()
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-b.wake:
		case <-ticker.C:
			b.access.RLock()
			idle := time.Since(b.lastActive) > b.idleTimeout
			b.access.RUnlock()
			if idle {
				continue
			}
		}
		_, _ = b.URLTest(b.ctx)
	}
}

func (b *LoadBalance) URLTest(ctx context.Context) (map[string]uint16, error) {
	select {
	case b.probeGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.ctx.Done():
		return nil, b.ctx.Err()
	}
	defer func() { <-b.probeGate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(b.ctx, cancel)
	defer stop()
	defer cancel()
	b.access.RLock()
	members := b.members
	b.access.RUnlock()
	states := make([]LoadBalanceHealth, len(members))
	var workers sync.WaitGroup
	// Also drain workers when cancellation interrupts scheduling, before releasing the gate.
	defer workers.Wait()
	permits := make(chan struct{}, 10)
	for i, member := range members {
		// An HTTP probe cannot assess a UDP-only member.
		if !slices.Contains(member.outbound.Network(), N.NetworkTCP) {
			b.publishHealth(ctx, member, &loadBalanceHealth{state: LoadBalanceHealth{Status: balanceUnknown}, path: balanceProbePath(member.outbound)})
			continue
		}
		select {
		case permits <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		workers.Add(1)
		go func(i int, member *loadBalanceMember) {
			defer workers.Done()
			defer func() { <-permits }()
			probeContext, cancel := context.WithTimeout(ctx, C.TCPTimeout)
			defer cancel()
			path := balanceProbePath(member.outbound)
			// Probing the captured leaf bypasses URLTest.DialContext. Preserve its
			// activity tracking so idle nested groups can discover working alternatives.
			for _, value := range path {
				if nested, ok := value.(*URLTest); ok {
					nested.group.Touch()
				}
			}
			// Dial the captured selection so a concurrent selector change cannot make
			// this probe test a different leaf than the one recorded in its result.
			target := path[len(path)-1]
			var delay uint16
			var err error
			if target == nil {
				err = E.New("no selected outbound")
			} else {
				delay, err = urltest.URLTest(probeContext, b.link, target)
			}
			status := balanceHealthy
			if err != nil {
				status = balanceUnhealthy
			}
			state := LoadBalanceHealth{Status: status, CheckedAt: time.Now(), Delay: delay}
			if b.publishHealth(ctx, member, &loadBalanceHealth{state: state, path: path}) {
				states[i] = state
			}
		}(i, member)
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.access.RLock()
	defer b.access.RUnlock()
	result := make(map[string]uint16)
	for i, member := range members {
		if b.byTag[member.tag] == member && states[i].Status == balanceHealthy && balanceProbePathMatches(member.outbound, member.health.Load().path) {
			result[member.tag] = states[i].Delay
		}
	}
	return result, nil
}

func (b *LoadBalance) publishHealth(ctx context.Context, member *loadBalanceMember, health *loadBalanceHealth) bool {
	b.access.RLock()
	if ctx.Err() != nil || b.ctx.Err() != nil || b.byTag[member.tag] != member {
		b.access.RUnlock()
		return false
	}
	if !balanceProbePathMatches(member.outbound, health.path) {
		b.access.RUnlock()
		b.signal()
		return false
	}
	member.health.Store(health)
	b.access.RUnlock()
	// Notify daemon subscribers without overwriting another group's test history.
	if b.history != nil {
		b.history.NotifyUpdated()
	}
	return true
}

func balanceSelectedChild(value adapter.Outbound) adapter.Outbound {
	if _, dynamic := value.(adapter.ConnectionSelectingOutboundGroup); dynamic {
		return nil
	}
	if group, ok := value.(adapter.OutboundGroup); ok {
		return group.Selected(N.NetworkTCP)
	}
	return nil
}

func balanceProbePath(value adapter.Outbound) []adapter.Outbound {
	path := []adapter.Outbound{value}
	for {
		if _, dynamic := value.(adapter.ConnectionSelectingOutboundGroup); dynamic {
			return path
		}
		group, ok := value.(adapter.OutboundGroup)
		if !ok {
			return path
		}
		value = group.Selected(N.NetworkTCP)
		path = append(path, value)
		if value == nil {
			return path
		}
	}
}

func balanceProbePathMatches(value adapter.Outbound, path []adapter.Outbound) bool {
	for _, expected := range path {
		if value != expected {
			return false
		}
		value = balanceSelectedChild(value)
	}
	return value == nil
}
