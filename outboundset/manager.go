package outboundset

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	OB "github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service"
)

type remoteSource struct {
	set    option.OutboundSet
	tag    string
	client *http.Client
	next   time.Time
}

type Manager struct {
	ctx               context.Context
	cancel            context.CancelFunc
	baseContext       context.Context
	logger            logger.ContextLogger
	options           option.Options
	expanded          option.Options
	loaded            map[string][]option.Outbound
	loader            *loader
	outbound          *OB.Manager
	registry          adapter.OutboundRegistry
	router            adapter.Router
	remotes           []*remoteSource
	handles           map[string]*member
	updateAccess      sync.Mutex
	currentAccess     sync.RWMutex
	current           *generation
	closed            bool
	generationsAccess sync.Mutex
	generations       map[*generation]bool
	workers           sync.WaitGroup
	onUpdate          func(option.Options)
}

// Attach wraps generated members before the normal outbound startup stages.
// Static sets need no runtime manager unless they participate in a remote graph.
func (e *Expansion) Attach(ctx context.Context, manager *OB.Manager, registry adapter.OutboundRegistry, router adapter.Router, onUpdate func(option.Options)) *Manager {
	var remote bool
	for _, set := range e.original.OutboundSet {
		remote = remote || set.Type == C.OutboundSetTypeRemote
	}
	if !remote {
		return nil
	}
	updateCtx, cancel := context.WithCancel(ctx)
	m := &Manager{
		ctx: updateCtx, cancel: cancel, baseContext: ctx, logger: e.loader.log,
		options: e.original, expanded: e.Options, loaded: e.loaded, loader: newLoader(updateCtx, e.loader.log, e.original.Experimental),
		outbound: manager, registry: registry, router: router,
		handles: make(map[string]*member), generations: make(map[*generation]bool),
		onUpdate: onUpdate,
	}
	g := &generation{members: make(map[string]adapter.Outbound), instances: make(map[string]*ownedInstance)}
	var replacements []adapter.Outbound
	for _, options := range e.Options.Outbounds[len(e.original.Outbounds):] {
		value, _ := manager.Outbound(options.Tag)
		g.members[options.Tag] = value
		g.instances[options.Tag] = &ownedInstance{value: value, owners: 1, scope: adapter.NewScope(m.baseContext, m.logger)}
		g.order = append(g.order, value)
		handle := &member{manager: m, tag: options.Tag}
		m.handles[options.Tag] = handle
		replacements = append(replacements, handle)
	}
	m.current = g
	m.track(g)
	manager.Publish(replacements, nil, nil)
	for _, set := range e.original.OutboundSet {
		if set.Type != C.OutboundSetTypeRemote {
			continue
		}
		for _, tag := range set.Tag {
			next := time.Now().Add(updateInterval(set))
			if e.refresh[tag] {
				next = time.Now()
			}
			m.remotes = append(m.remotes, &remoteSource{set: set, tag: tag, next: next})
		}
	}
	service.MustRegisterPtr(ctx, m)
	return m
}

func updateInterval(set option.OutboundSet) time.Duration {
	if set.RemoteOptions.UpdateInterval > 0 {
		return time.Duration(set.RemoteOptions.UpdateInterval)
	}
	return 24 * time.Hour
}

func (m *Manager) Initialize() error {
	clients := service.FromContext[adapter.HTTPClientManager](m.baseContext)
	for _, source := range m.remotes {
		options, err := ResolveHTTPClient(m.options, source.set.RemoteOptions.HTTPClient)
		if err != nil {
			return err
		}
		if options.Detour != "" {
			if _, found := m.outbound.Outbound(options.Detour); !found {
				return E.New("outbound-set[", source.tag, "]: http_client detour not found: ", options.Detour)
			}
		}
		transport, err := clients.ResolveTransport(m.baseContext, m.logger, options)
		if err != nil {
			return E.Cause(err, "outbound-set[", source.tag, "]: http_client")
		}
		source.client = &http.Client{Transport: transport, Timeout: 30 * time.Second}
	}
	return nil
}

func (m *Manager) Start() {
	m.workers.Go(func() {
		for {
			next := time.Now().Add(24 * time.Hour)
			for _, source := range m.remotes {
				if !time.Now().Before(source.next) {
					if err := m.Refresh(m.ctx, source.tag); err != nil && m.ctx.Err() == nil {
						m.logger.Error("update outbound-set[", source.tag, "]: ", err)
					}
					source.next = time.Now().Add(updateInterval(source.set))
				}
				if source.next.Before(next) {
					next = source.next
				}
			}
			timer := time.NewTimer(max(time.Until(next), time.Millisecond))
			select {
			case <-m.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	})
}

func (m *Manager) track(g *generation) {
	m.generationsAccess.Lock()
	m.generations[g] = true
	m.generationsAccess.Unlock()
	g.onClose = func() {
		m.generationsAccess.Lock()
		delete(m.generations, g)
		m.generationsAccess.Unlock()
	}
}

func (m *Manager) Close() error {
	m.cancel()
	m.workers.Wait()
	m.updateAccess.Lock()
	defer m.updateAccess.Unlock()
	m.currentAccess.Lock()
	m.closed = true
	m.currentAccess.Unlock()
	m.generationsAccess.Lock()
	generations := make([]*generation, 0, len(m.generations))
	for g := range m.generations {
		generations = append(generations, g)
	}
	m.generationsAccess.Unlock()
	for _, g := range generations {
		g.close()
	}
	for _, source := range m.remotes {
		if source.client != nil {
			source.client.CloseIdleConnections()
		}
	}
	m.loader.close()
	return nil
}

// Refresh performs one validated update. No live state or cached source is
// changed until all new members have been constructed and started successfully.
func (m *Manager) Refresh(ctx context.Context, tag string) error {
	m.updateAccess.Lock()
	defer m.updateAccess.Unlock()
	if m.ctx.Err() != nil {
		return os.ErrClosed
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	defer cancel()
	var source *remoteSource
	for _, candidate := range m.remotes {
		if candidate.tag == tag {
			source = candidate
			break
		}
	}
	if source == nil {
		return E.New("remote outbound-set not found: ", tag)
	}
	if source.client == nil {
		return E.New("outbound-set HTTP client is not initialized")
	}
	url := strings.ReplaceAll(source.set.RemoteOptions.URL, C.OutboundSetTagPlaceholder, tag)
	content, err := Download(ctx, source.client, url)
	if err != nil {
		return err
	}
	members, err := m.loader.decode(source.set, tag, content)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	before, err := json.MarshalContext(m.baseContext, m.loaded[tag])
	if err != nil {
		return err
	}
	after, err := json.MarshalContext(m.baseContext, members)
	if err != nil {
		return err
	}
	if bytes.Equal(before, after) {
		m.save(tag, url, content)
		return nil
	}
	loaded := maps.Clone(m.loaded)
	loaded[tag] = members
	expanded, err := expandLoaded(m.options, loaded, false)
	if err != nil {
		return err
	}
	available := make(map[string]bool)
	for i, value := range expanded.Outbounds {
		name := value.Tag
		if name == "" {
			name = strconv.Itoa(i)
		}
		available[name] = true
	}
	for i, value := range expanded.Endpoints {
		name := value.Tag
		if name == "" {
			name = strconv.Itoa(i)
		}
		available[name] = true
	}
	// References outside groups/detours (routes, DNS, HTTP clients, etc.) must
	// remain valid. A disappearing configured selector default is only a preference.
	protected := m.options
	protected.Outbounds, protected.Endpoints = nil, nil
	for _, reference := range referencedOutbounds(protected) {
		if !available[reference] {
			return E.New("update removes referenced outbound: ", reference)
		}
	}
	if currentDefault := m.outbound.Default(); currentDefault != nil && !available[currentDefault.Tag()] {
		return E.New("update removes default outbound: ", currentDefault.Tag())
	}
	// Reject dependency reversals that could cycle while an old in-flight dial
	// crosses a static group into the new generation.
	if err = validateTransition(m.expanded, expanded); err != nil {
		return err
	}
	g, err := m.build(ctx, expanded.Outbounds[len(m.options.Outbounds):])
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		g.close()
		return err
	}
	m.track(g)
	var replacements []adapter.Outbound
	for _, value := range g.order {
		handle := m.handles[value.Tag()]
		if handle == nil {
			handle = &member{manager: m, tag: value.Tag()}
			m.handles[value.Tag()] = handle
		}
		replacements = append(replacements, handle)
	}
	var removed []string
	for name := range m.current.members {
		if _, found := g.members[name]; !found {
			removed = append(removed, name)
		}
	}
	type groupUpdate struct {
		group   adapter.OutboundGroupUpdater
		members []adapter.Outbound
	}
	var groups []groupUpdate
	for i, definition := range expanded.Outbounds[:len(m.options.Outbounds)] {
		var tags []string
		switch options := definition.Options.(type) {
		case *option.SelectorOutboundOptions:
			tags = options.Outbounds
		case *option.URLTestOutboundOptions:
			tags = options.Outbounds
		case *option.LoadBalanceOutboundOptions:
			tags = options.Outbounds
		default:
			continue
		}
		name := definition.Tag
		if name == "" {
			name = strconv.Itoa(i)
		}
		raw, _ := m.outbound.Outbound(name)
		group, ok := raw.(adapter.OutboundGroupUpdater)
		if !ok {
			g.close()
			return E.New("outbound group cannot update: ", name)
		}
		var values []adapter.Outbound
		for _, name := range tags {
			if handle, found := m.handles[name]; found && g.members[name] != nil {
				values = append(values, handle)
			} else {
				value, _ := m.outbound.Outbound(name)
				values = append(values, value)
			}
		}
		groups = append(groups, groupUpdate{group, values})
	}
	previous := m.current
	history := service.PtrFromContext[urltest.HistoryStorage](m.baseContext)
	// A static group or detour can keep its identity while a dependency changes.
	// Include transitive dependents so their cached health is invalidated too.
	dependents := make(map[string][]string)
	// Include old edges as well: removed members disappear from the new graph.
	for _, definitions := range [][]option.Outbound{
		m.expanded.Outbounds[:len(m.options.Outbounds)],
		expanded.Outbounds[:len(m.options.Outbounds)],
	} {
		for i, definition := range definitions {
			name := definition.Tag
			if name == "" {
				name = strconv.Itoa(i)
			}
			for _, dependency := range referencedOutbounds(definition.Options) {
				dependents[dependency] = append(dependents[dependency], name)
			}
		}
	}
	changedSet := make(map[string]bool)
	var changed []string
	markChanged := func(name string) {
		if !changedSet[name] {
			changedSet[name] = true
			changed = append(changed, name)
		}
	}
	for name, value := range g.members {
		if previous.members[name] != value {
			markChanged(name)
		}
	}
	for _, name := range removed {
		markChanged(name)
	}
	for i := 0; i < len(changed); i++ {
		for _, name := range dependents[changed[i]] {
			markChanged(name)
		}
	}
	m.outbound.Publish(replacements, removed, func() {
		m.currentAccess.Lock()
		m.current = g
		m.currentAccess.Unlock()
		if history != nil {
			for name, value := range previous.members {
				if g.members[name] != value {
					history.DeleteURLTestHistory(name)
				}
			}
		}
		for _, update := range groups {
			if aware, ok := update.group.(adapter.OutboundGroupChangeUpdater); ok {
				aware.UpdateOutboundsWithChanges(update.members, changed)
			} else {
				update.group.UpdateOutbounds(update.members)
			}
		}
	})
	// ReferenceManager caches policy by stable handle identity. Reapply that
	// policy to replacements even when the handle's referenced state is unchanged.
	for _, value := range g.order {
		if previous.members[value.Tag()] != value {
			m.handles[value.Tag()].applyIdlePolicy()
		}
	}
	m.loaded, m.expanded = loaded, expanded
	for _, tag := range removed {
		delete(m.handles, tag)
	}
	if m.onUpdate != nil {
		m.onUpdate(expanded)
	}
	if history != nil {
		history.NotifyUpdated()
	}
	previous.retire()
	m.save(tag, url, content)
	m.logger.Info("updated outbound-set[", tag, "]")
	return nil
}

func (m *Manager) save(tag, url string, content []byte) {
	if path := m.loader.sourceCachePath(tag, url); path != "" {
		if err := m.loader.save(cacheEntry{path: path, content: content}); err != nil {
			m.logger.Warn(E.Cause(err, "save outbound-set cache"))
		}
	}
}

// The staging manager resolves generated dependencies inside the new generation.
// No constructors or startup stages can replace entries in the live manager.
type stagingManager struct {
	adapter.OutboundManager
	generated map[string]bool
	values    map[string]adapter.Outbound
}

func (s *stagingManager) Outbound(tag string) (adapter.Outbound, bool) {
	if s.generated[tag] {
		value, found := s.values[tag]
		return value, found
	}
	return s.OutboundManager.Outbound(tag)
}

func (s *stagingManager) Default() adapter.Outbound {
	value := s.OutboundManager.Default()
	if value != nil && s.generated[value.Tag()] {
		return s.values[value.Tag()]
	}
	return value
}

func (m *Manager) build(ctx context.Context, definitions []option.Outbound) (*generation, error) {
	g := &generation{members: make(map[string]adapter.Outbound), instances: make(map[string]*ownedInstance)}
	staging := &stagingManager{OutboundManager: m.outbound, generated: make(map[string]bool), values: g.members}
	for _, value := range definitions {
		staging.generated[value.Tag] = true
	}
	previous := make(map[string][]byte)
	for _, definition := range m.expanded.Outbounds[len(m.options.Outbounds):] {
		content, err := json.MarshalContext(m.baseContext, &definition)
		if err != nil {
			return nil, err
		}
		previous[definition.Tag] = content
	}
	changed := make(map[string]bool)
	for _, definition := range definitions {
		content, err := json.MarshalContext(m.baseContext, &definition)
		if err != nil {
			return nil, err
		}
		changed[definition.Tag] = !bytes.Equal(previous[definition.Tag], content)
	}
	// A proxy's dialer can retain a generated dependency. Rebuild the proxy
	// whenever that dependency changes, including indirect relay chains.
	for {
		progress := false
		for _, definition := range definitions {
			if changed[definition.Tag] {
				continue
			}
			if wrapper, ok := definition.Options.(option.DialerOptionsWrapper); ok && changed[wrapper.TakeDialerOptions().Detour] {
				changed[definition.Tag] = true
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	success := false
	defer func() {
		if !success {
			g.close()
		}
	}()
	// Configuration validation has already checked cycles. Construct and start
	// dependencies first, including protocols that resolve detours in constructors.
	var pending []option.Outbound
	for _, definition := range definitions {
		if !changed[definition.Tag] {
			instance := m.current.instances[definition.Tag]
			instance.retain()
			g.instances[definition.Tag] = instance
			g.members[definition.Tag] = instance.value
			g.order = append(g.order, instance.value)
		} else {
			pending = append(pending, definition)
		}
	}
	var created []adapter.Outbound
	for len(pending) > 0 {
		progress := false
		for i := 0; i < len(pending); {
			definition := pending[i]
			if wrapper, ok := definition.Options.(option.DialerOptionsWrapper); ok {
				dependency := wrapper.TakeDialerOptions().Detour
				if staging.generated[dependency] && g.members[dependency] == nil {
					i++
					continue
				}
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Keep only this proxy's generated dependencies in its service view.
			// Retaining whole historical snapshots would keep unrelated retired
			// instances reachable through otherwise unchanged proxies.
			view := &stagingManager{OutboundManager: m.outbound, generated: staging.generated, values: make(map[string]adapter.Outbound)}
			for _, dependency := range referencedOutbounds(definition.Options) {
				if value := g.members[dependency]; value != nil {
					view.values[dependency] = value
				}
			}
			instanceContext := service.ContextWith[adapter.OutboundManager](service.ExtendContext(m.baseContext), view)
			value, err := m.registry.CreateOutbound(instanceContext, m.router, m.logger, definition.Tag, definition.Type, definition.Options)
			if err != nil {
				return nil, E.Cause(err, "create member[", definition.Tag, "]")
			}
			g.members[definition.Tag] = value
			view.values[definition.Tag] = value
			g.instances[definition.Tag] = &ownedInstance{value: value, owners: 1, scope: adapter.NewScope(m.baseContext, m.logger)}
			g.order = append(g.order, value)
			created = append(created, value)
			pending = append(pending[:i], pending[i+1:]...)
			progress = true
		}
		if !progress {
			return nil, E.New("circular generated outbound dependency")
		}
	}
	for _, stage := range adapter.ListStartStages {
		for _, value := range created {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := g.instances[value.Tag()].start(stage); err != nil {
				return nil, E.Cause(err, "start member[", value.Tag(), "]")
			}
		}
	}
	success = true
	return g, nil
}

// referencedOutbounds reads the same reference annotations used by configuration
// tooling. It includes nested rules and inline HTTP/DNS client options.
func referencedOutbounds(value any) []string {
	var result []string
	var walk func(reflect.Value, bool)
	walk = func(v reflect.Value, reference bool) {
		if !v.IsValid() {
			return
		}
		if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			if !v.IsNil() {
				walk(v.Elem(), reference)
			}
			return
		}
		switch v.Kind() {
		case reflect.String:
			if reference && v.String() != "" {
				result = append(result, v.String())
			}
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				return
			}
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), reference)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				field := v.Type().Field(i)
				if field.IsExported() {
					walk(v.Field(i), field.Tag.Get("reference") == "outbound")
				}
			}
		}
	}
	walk(reflect.ValueOf(value), false)
	return result
}

func validateTransition(previous, next option.Options) error {
	// Represent the union as groups for the existing graph validator. Defaults
	// are excluded because membership already supplies the relevant edges.
	union := make(map[string][]string)
	for _, options := range []option.Options{previous, next} {
		for i, value := range options.Outbounds {
			tag := value.Tag
			if tag == "" {
				tag = strconv.Itoa(i)
			}
			dependencies := referencedOutbounds(value.Options)
			union[tag] = append(union[tag], dependencies...)
		}
		for i, value := range options.Endpoints {
			tag := value.Tag
			if tag == "" {
				tag = strconv.Itoa(i)
			}
			union[tag] = append(union[tag], referencedOutbounds(value.Options)...)
		}
	}
	var graph option.Options
	for tag, dependencies := range union {
		// Removed preferred defaults have no graph edge.
		dependencies = slices.DeleteFunc(dependencies, func(tag string) bool { _, found := union[tag]; return !found })
		graph.Outbounds = append(graph.Outbounds, option.Outbound{Tag: tag, Options: &option.SelectorOutboundOptions{Outbounds: dependencies}})
	}
	return validateDependencies(graph)
}
