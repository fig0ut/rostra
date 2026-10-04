package group

import (
	"context"
	"io"
	"maps"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

func RegisterURLTest(registry *outbound.Registry) {
	outbound.Register[option.URLTestOutboundOptions](registry, C.TypeURLTest, NewURLTest)
}

var (
	_ adapter.OutboundGroup           = (*URLTest)(nil)
	_ adapter.InterfaceUpdateListener = (*URLTest)(nil)
	_ adapter.Referrer                = (*URLTest)(nil)
)

type URLTest struct {
	outbound.Adapter
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	logger                       log.ContextLogger
	tags                         []string
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	group                        *URLTestGroup
	checkAccess                  sync.Mutex
	interruptExternalConnections bool
}

func NewURLTest(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.URLTestOutboundOptions) (adapter.Outbound, error) {
	outbound := &URLTest{
		Adapter:                      outbound.NewAdapter(C.TypeURLTest, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:                          ctx,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		tolerance:                    options.Tolerance,
		idleTimeout:                  time.Duration(options.IdleTimeout),
		interruptExternalConnections: options.InterruptExistConnections,
	}
	if len(outbound.tags) == 0 {
		return nil, E.New("missing tags")
	}
	return outbound, nil
}

func (s *URLTest) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	switch stage {
	case adapter.StartStateStart:
		outbounds := make([]adapter.Outbound, 0, len(s.tags))
		for i, tag := range s.tags {
			detour, loaded := s.outbound.Outbound(tag)
			if !loaded {
				return E.New("outbound ", i, " not found: ", tag)
			}
			outbounds = append(outbounds, detour)
		}
		group, err := NewURLTestGroup(s.ctx, s.outbound, s.logger, outbounds, s.link, s.interval, s.tolerance, s.idleTimeout, s.interruptExternalConnections)
		if err != nil {
			return err
		}
		s.group = group
	case adapter.StartStateStarted:
		s.group.PostStart()
		scope.Add(s.group.Close)
	}
	return nil
}

func (s *URLTest) All() []string {
	if s.group == nil {
		return slices.Clone(s.tags)
	}
	s.group.stateAccess.RLock()
	defer s.group.stateAccess.RUnlock()
	return common.Map(s.group.outbounds, func(it adapter.Outbound) string { return it.Tag() })
}

func (s *URLTest) Dependencies() []string { return s.All() }

func (s *URLTest) UpdateOutbounds(members []adapter.Outbound) {
	g := s.group
	g.stateAccess.Lock()
	g.outbounds = slices.Clone(members)
	g.revision++
	preserve := func(previous adapter.Outbound, network string) adapter.Outbound {
		if previous != nil {
			for _, member := range members {
				if member.Tag() == previous.Tag() && common.Contains(member.Network(), network) {
					return member
				}
			}
		}
		return nil
	}
	g.selectedOutboundTCP = preserve(g.selectedOutboundTCP, N.NetworkTCP)
	g.selectedOutboundUDP = preserve(g.selectedOutboundUDP, N.NetworkUDP)
	g.stateAccess.Unlock()
	g.access.Lock()
	started := g.started && !g.closed
	g.access.Unlock()
	if started {
		go g.CheckOutbounds(g.ctx, true)
	}
}

func (s *URLTest) Selected(network string) adapter.Outbound {
	return s.group.selected(network)
}

func (s *URLTest) AttachConnection(closer io.Closer) func() {
	s.group.Touch()
	return s.group.interruptGroup.Add(closer, true)
}

func (s *URLTest) References() []string {
	group := s.group
	if group == nil {
		return nil
	}
	group.stateAccess.RLock()
	defer group.stateAccess.RUnlock()
	var references []string
	if group.selectedOutboundTCP != nil {
		references = append(references, group.selectedOutboundTCP.Tag())
	}
	if group.selectedOutboundUDP != nil && group.selectedOutboundUDP != group.selectedOutboundTCP {
		references = append(references, group.selectedOutboundUDP.Tag())
	}
	return references
}

func (s *URLTest) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *URLTest) CheckOutbounds() {
	s.group.CheckOutbounds(s.ctx, true)
}

func (s *URLTest) PerformUpdateCheck() {
	s.group.performUpdateCheck()
}

func (s *URLTest) InterfaceUpdated(ctx context.Context) {
	group := s.group
	if group == nil {
		return
	}
	if group.pause.IsDevicePaused() || group.pause.IsNetworkPaused() {
		return
	}
	go func() {
		s.checkAccess.Lock()
		defer s.checkAccess.Unlock()
		if ctx.Err() != nil {
			return
		}
		group.CheckOutbounds(ctx, true)
	}()
}

func (s *URLTest) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	if N.NetworkName(network) != N.NetworkTCP && N.NetworkName(network) != N.NetworkUDP {
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	outbound := s.group.selected(N.NetworkName(network))
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := outbound.DialContext(ctx, network, destination)
	if err == nil {
		return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.group.history.DeleteURLTestHistory(outbound.Tag())
	return nil, err
}

func (s *URLTest) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	outbound := s.group.selected(N.NetworkUDP)
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := outbound.ListenPacket(ctx, destination)
	if err == nil {
		return s.group.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.group.history.DeleteURLTestHistory(outbound.Tag())
	return nil, err
}

type URLTestGroup struct {
	stateAccess                  sync.RWMutex
	revision                     uint64
	cancel                       context.CancelFunc
	closed                       bool
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	pause                        pause.Manager
	pauseCallback                *list.Element[pause.Callback]
	logger                       log.Logger
	outbounds                    []adapter.Outbound
	link                         string
	interval                     time.Duration
	tolerance                    uint16
	idleTimeout                  time.Duration
	history                      *urltest.HistoryStorage
	checking                     atomic.Bool
	selectedOutboundTCP          adapter.Outbound
	selectedOutboundUDP          adapter.Outbound
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	updateAccess                 sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      bool
	lastActive                   common.TypedValue[time.Time]
}

func NewURLTestGroup(ctx context.Context, outboundManager adapter.OutboundManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, tolerance uint16, idleTimeout time.Duration, interruptExternalConnections bool) (*URLTestGroup, error) {
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if tolerance == 0 {
		tolerance = 50
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	history := service.PtrFromContext[urltest.HistoryStorage](ctx)
	if history == nil {
		return nil, E.New("missing URL test history storage")
	}
	ctx, cancel := context.WithCancel(ctx)
	return &URLTestGroup{
		cancel:                       cancel,
		ctx:                          ctx,
		outbound:                     outboundManager,
		logger:                       logger,
		outbounds:                    outbounds,
		link:                         link,
		interval:                     interval,
		tolerance:                    tolerance,
		idleTimeout:                  idleTimeout,
		history:                      history,
		close:                        make(chan struct{}),
		pause:                        service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExternalConnections,
	}, nil
}

func (g *URLTestGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = true
	g.lastActive.Store(time.Now())
	go g.CheckOutbounds(g.ctx, false)
}

func (g *URLTestGroup) Touch() {
	g.access.Lock()
	defer g.access.Unlock()
	if !g.started || g.closed {
		return
	}
	if g.ticker != nil {
		g.lastActive.Store(time.Now())
		return
	}
	ticker := time.NewTicker(g.interval)
	g.ticker = ticker
	g.pauseCallback = pause.RegisterTicker(g.pause, ticker, g.interval, nil)
	go g.loopCheck(ticker, g.close)
}

func (g *URLTestGroup) Close() error {
	g.cancel()
	g.access.Lock()
	defer g.access.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	if g.ticker != nil {
		g.ticker.Stop()
		g.ticker = nil
		g.pause.UnregisterCallback(g.pauseCallback)
		g.pauseCallback = nil
	}
	close(g.close)
	return nil
}

func (g *URLTestGroup) selected(network string) adapter.Outbound {
	g.stateAccess.RLock()
	defer g.stateAccess.RUnlock()
	selected := g.selectedOutboundTCP
	if network == N.NetworkUDP {
		selected = g.selectedOutboundUDP
	}
	if selected == nil {
		selected, _ = g.selectLocked(network)
	}
	return selected
}

func (g *URLTestGroup) Select(network string) (adapter.Outbound, bool) {
	g.stateAccess.RLock()
	defer g.stateAccess.RUnlock()
	return g.selectLocked(network)
}

func (g *URLTestGroup) selectLocked(network string) (adapter.Outbound, bool) {
	var minDelay uint16
	var minOutbound adapter.Outbound
	switch network {
	case N.NetworkTCP:
		if g.selectedOutboundTCP != nil {
			if history := g.history.LoadURLTestHistory(RealTag(g.selectedOutboundTCP, N.NetworkTCP)); history != nil {
				minOutbound = g.selectedOutboundTCP
				minDelay = history.Delay
			}
		}
	case N.NetworkUDP:
		if g.selectedOutboundUDP != nil {
			if history := g.history.LoadURLTestHistory(RealTag(g.selectedOutboundUDP, N.NetworkUDP)); history != nil {
				minOutbound = g.selectedOutboundUDP
				minDelay = history.Delay
			}
		}
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(detour, network))
		if history == nil {
			continue
		}
		if minDelay == 0 || minDelay > history.Delay+g.tolerance {
			minDelay = history.Delay
			minOutbound = detour
		}
	}
	if minOutbound == nil {
		for _, detour := range g.outbounds {
			if !common.Contains(detour.Network(), network) {
				continue
			}
			return detour, false
		}
		return nil, false
	}
	return minOutbound, true
}

func (g *URLTestGroup) loopCheck(ticker *time.Ticker, closeChan <-chan struct{}) {
	if time.Since(g.lastActive.Load()) > g.interval {
		g.lastActive.Store(time.Now())
		g.CheckOutbounds(g.ctx, false)
	}
	for {
		select {
		case <-closeChan:
			return
		case <-ticker.C:
		}
		if time.Since(g.lastActive.Load()) > g.idleTimeout {
			g.access.Lock()
			if g.ticker == ticker {
				g.ticker.Stop()
				g.ticker = nil
				g.pause.UnregisterCallback(g.pauseCallback)
				g.pauseCallback = nil
			}
			g.access.Unlock()
			return
		}
		g.CheckOutbounds(g.ctx, false)
	}
}

func (g *URLTestGroup) CheckOutbounds(ctx context.Context, force bool) {
	_, _ = g.urlTest(ctx, force)
}

func (g *URLTestGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(ctx, true)
}

func (g *URLTestGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	if g.checking.Swap(true) {
		return make(map[string]uint16), nil
	}
	var retry bool
	defer func() {
		g.checking.Store(false)
		if retry && g.ctx.Err() == nil {
			go g.CheckOutbounds(g.ctx, true)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.ctx, cancel)
	defer stop()
	defer cancel()
	g.stateAccess.RLock()
	members, revision := slices.Clone(g.outbounds), g.revision
	g.stateAccess.RUnlock()
	result := URLTestOutbounds(ctx, g.outbound, g.history, g.logger, members, g.link, g.interval, force)
	g.stateAccess.RLock()
	stale := revision != g.revision
	g.stateAccess.RUnlock()
	if stale {
		for _, member := range members {
			g.history.DeleteURLTestHistory(member.Tag())
		}
		retry = true
		return map[string]uint16{}, nil
	}
	g.performUpdateCheck()
	return result, nil
}

type urlTestResult struct {
	delay uint16
	err   error
}

type urlTestBatch struct {
	ctx      context.Context
	outbound adapter.OutboundManager
	history  *urltest.HistoryStorage
	logger   log.Logger
	batch    *batch.Batch[any]
	checked  map[string]bool
	groups   []adapter.OutboundGroup
	access   sync.Mutex
	result   map[string]uint16
}

func URLTestOutbounds(ctx context.Context, outboundManager adapter.OutboundManager, history *urltest.HistoryStorage, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, force bool) map[string]uint16 {
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](10))
	testBatch := &urlTestBatch{
		ctx:      ctx,
		outbound: outboundManager,
		history:  history,
		logger:   logger,
		batch:    b,
		checked:  make(map[string]bool),
		result:   make(map[string]uint16),
	}
	testBatch.test(outbounds, link, interval, force)
	b.Wait()
	for _, outboundGroup := range testBatch.groups {
		groupHistory := history.LoadURLTestHistory(RealTag(outboundGroup, N.NetworkTCP))
		if groupHistory != nil {
			testBatch.result[outboundGroup.Tag()] = groupHistory.Delay
		}
	}
	return testBatch.result
}

func (b *urlTestBatch) test(outbounds []adapter.Outbound, link string, interval time.Duration, force bool) {
	for _, detour := range outbounds {
		tag := detour.Tag()
		if b.checked[tag] {
			continue
		}
		switch nested := detour.(type) {
		case *URLTest:
			b.checked[tag] = true
			b.groups = append(b.groups, nested)
			b.batch.Go(tag, func() (any, error) {
				nestedResult, _ := nested.group.urlTest(b.ctx, force)
				b.access.Lock()
				maps.Copy(b.result, nestedResult)
				b.access.Unlock()
				return nil, nil
			})
		case adapter.OutboundGroup:
			if _, dynamic := nested.(adapter.ConnectionSelectingOutboundGroup); dynamic {
				b.testOutbound(detour, link, interval, force)
				continue
			}
			b.checked[tag] = true
			b.groups = append(b.groups, nested)
			b.test(common.FilterNotNil(common.Map(nested.All(), func(it string) adapter.Outbound {
				member, _ := b.outbound.Outbound(it)
				return member
			})), link, interval, force)
		default:
			b.testOutbound(detour, link, interval, force)
		}
	}
}

func (b *urlTestBatch) testOutbound(detour adapter.Outbound, link string, interval time.Duration, force bool) {
	tag := detour.Tag()
	history := b.history.LoadURLTestHistory(tag)
	if !force && history != nil && time.Since(history.Time) < interval {
		return
	}
	b.checked[tag] = true
	b.batch.Go(tag, func() (any, error) {
		testCtx, cancel := context.WithTimeout(b.ctx, C.TCPTimeout)
		defer cancel()
		testChan := make(chan urlTestResult, 1)
		go func() {
			delay, testErr := urltest.URLTest(testCtx, link, detour)
			testChan <- urlTestResult{delay, testErr}
		}()
		var testResult urlTestResult
		select {
		case testResult = <-testChan:
		case <-testCtx.Done():
			testResult.err = testCtx.Err()
		}
		if testResult.err != nil {
			if b.ctx.Err() != nil {
				return nil, nil
			}
			b.logger.Debug("outbound ", tag, " unavailable: ", testResult.err)
			b.history.DeleteURLTestHistory(tag)
		} else {
			b.logger.Debug("outbound ", tag, " available: ", testResult.delay, "ms")
			b.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{
				Time:  time.Now(),
				Delay: testResult.delay,
			})
			b.access.Lock()
			b.result[tag] = testResult.delay
			b.access.Unlock()
		}
		return nil, nil
	})
}

func (g *URLTestGroup) performUpdateCheck() {
	g.updateAccess.Lock()
	defer g.updateAccess.Unlock()
	g.stateAccess.Lock()
	defer g.stateAccess.Unlock()
	var (
		updated  bool
		selected bool
	)
	if outbound, exists := g.selectLocked(N.NetworkTCP); outbound != nil && (g.selectedOutboundTCP == nil || (exists && outbound != g.selectedOutboundTCP)) {
		if g.selectedOutboundTCP != nil {
			updated = true
		}
		g.selectedOutboundTCP = outbound
		selected = true
	}
	if outbound, exists := g.selectLocked(N.NetworkUDP); outbound != nil && (g.selectedOutboundUDP == nil || (exists && outbound != g.selectedOutboundUDP)) {
		if g.selectedOutboundUDP != nil {
			updated = true
		}
		g.selectedOutboundUDP = outbound
		selected = true
	}
	if updated {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
	if selected {
		g.history.NotifyUpdated()
	}
}
