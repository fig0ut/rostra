package group

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
	"github.com/stretchr/testify/require"
)

type balanceTestOutbound struct {
	outbound.Adapter
	dial func(context.Context, string, M.Socksaddr) (net.Conn, error)
}

func (o *balanceTestOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if o.dial != nil {
		return o.dial(ctx, network, destination)
	}
	return nil, errors.New("offline")
}

func (*balanceTestOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused")
}

func balanceMember(tag string, networks ...string) *balanceTestOutbound {
	if len(networks) == 0 {
		networks = []string{"tcp", "udp"}
	}
	return &balanceTestOutbound{Adapter: outbound.NewAdapter("test", tag, networks, nil)}
}

func newTestBalance(t *testing.T, strategy string, members ...adapter.Outbound) *LoadBalance {
	t.Helper()
	value, err := NewLoadBalance(context.Background(), nil, nil, "balance", option.LoadBalanceOutboundOptions{
		Outbounds: []string{"placeholder"},
		Strategy:  strategy,
	})
	require.NoError(t, err)
	b := value.(*LoadBalance)
	b.UpdateOutbounds(members)
	t.Cleanup(func() {
		require.NoError(t, b.close())
	})
	return b
}

func balanceInput(host string) adapter.InboundContext {
	return adapter.InboundContext{
		Network:     "tcp",
		Source:      M.ParseSocksaddr("192.0.2.1:1234"),
		Destination: M.ParseSocksaddr(host + ":443"),
	}
}

func TestLoadBalanceRoundRobinInspection(t *testing.T) {
	b := newTestBalance(t, "round_robin", balanceMember("a"), balanceMember("b"), balanceMember("tcp-only", "tcp"))
	metadata := balanceInput("example.org")
	for i := range 12 {
		require.Nil(t, b.Selected("tcp"))
		require.Equal(t, "balance", RealTag(b, "tcp"))
		require.Len(t, b.All(), 3)
		require.Len(t, b.Health(), 3)
		selected, err := b.SelectForConnection(&metadata)
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b", "tcp-only"}[i%3], selected.Tag())
	}
	metadata.Network = "udp"
	for i := range 4 {
		selected, err := b.SelectForConnection(&metadata)
		require.NoError(t, err)
		require.Equal(t, []string{"a", "b"}[i%2], selected.Tag())
	}
	metadata.Network = "icmp"
	_, err := b.SelectForConnection(&metadata)
	require.Error(t, err)
}

func TestLoadBalanceConsistentHash(t *testing.T) {
	a, b, c, d := balanceMember("a"), balanceMember("b"), balanceMember("c"), balanceMember("d")
	group := newTestBalance(t, "", a, b, c)
	original := make(map[string]string)
	counts := make(map[string]int)
	for i := range 1000 {
		host := fmt.Sprintf("host%d.example", i)
		input := balanceInput(host)
		selected, err := group.SelectForConnection(&input)
		require.NoError(t, err)
		original[host] = selected.Tag()
		counts[selected.Tag()]++
	}
	for _, tag := range []string{"a", "b", "c"} {
		require.Greater(t, counts[tag], 200)
	}
	group.UpdateOutbounds([]adapter.Outbound{c, b, a})
	for host, tag := range original {
		input := balanceInput(host)
		selected, err := group.SelectForConnection(&input)
		require.NoError(t, err)
		require.Equal(t, tag, selected.Tag())
	}
	group.UpdateOutbounds([]adapter.Outbound{a, b, c, d})
	moved := 0
	for host, tag := range original {
		input := balanceInput(host)
		selected, err := group.SelectForConnection(&input)
		require.NoError(t, err)
		if selected.Tag() != tag {
			require.Equal(t, "d", selected.Tag())
			moved++
		}
	}
	require.Greater(t, moved, 100)
	group.UpdateOutbounds([]adapter.Outbound{a, b})
	for host, tag := range original {
		if tag == "c" {
			continue
		}
		input := balanceInput(host)
		selected, err := group.SelectForConnection(&input)
		require.NoError(t, err)
		require.Equal(t, tag, selected.Tag())
	}
	input := balanceInput("EXAMPLE.ORG.")
	other := balanceInput("example.org")
	other.Source.Port = 9999
	other.Destination.Port = 80
	require.Equal(t, balanceKey(&input, "source_ip_domain"), balanceKey(&other, "source_ip_domain"))
	other.Source = M.ParseSocksaddr("192.0.2.2:9999")
	require.NotEqual(t, balanceKey(&input, "source_ip_domain"), balanceKey(&other, "source_ip_domain"))
}

func TestLoadBalanceHealthAndReplacement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	good, bad, udp := balanceMember("good"), balanceMember("bad"), balanceMember("udp", "udp")
	good.dial = func(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, destination.String())
	}
	b := newTestBalance(t, "round_robin", good, bad, udp)
	b.link = server.URL
	result, err := b.URLTest(context.Background())
	require.NoError(t, err)
	require.Contains(t, result, "good")
	require.NotContains(t, result, "bad")
	health := b.Health()
	require.Equal(t, "healthy", health["good"].Status)
	require.Equal(t, "unhealthy", health["bad"].Status)
	require.Equal(t, "unknown", health["udp"].Status)
	input := balanceInput("example.org")
	for range 5 {
		selected, err := b.SelectForConnection(&input)
		require.NoError(t, err)
		require.Equal(t, "good", selected.Tag())
	}
	b.UpdateOutboundsWithChanges([]adapter.Outbound{good, bad, udp}, []string{"good"})
	require.Equal(t, "unknown", b.Health()["good"].Status)
	require.Equal(t, "unhealthy", b.Health()["bad"].Status)
	b.UpdateOutbounds([]adapter.Outbound{bad})
	_, err = b.SelectForConnection(&input)
	require.ErrorContains(t, err, "no eligible")
	b.UpdateOutbounds([]adapter.Outbound{balanceMember("bad")})
	require.Equal(t, "unknown", b.Health()["bad"].Status)
	require.Len(t, b.Health(), 1)
}

func TestLoadBalanceDiscardsStaleProbe(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	old := balanceMember("a")
	old.dial = func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("old failure")
	}
	b := newTestBalance(t, "", old)
	done := make(chan error, 1)
	go func() { _, err := b.URLTest(context.Background()); done <- err }()
	<-started
	b.UpdateOutbounds([]adapter.Outbound{balanceMember("a")})
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, "unknown", b.Health()["a"].Status)
}

func TestLoadBalanceShutdownCancelsProbe(t *testing.T) {
	started := make(chan struct{})
	member := balanceMember("a")
	member.dial = func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	b := newTestBalance(t, "", member)
	done := make(chan error, 1)
	go func() { _, err := b.URLTest(context.Background()); done <- err }()
	<-started
	require.NoError(t, b.close())
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("probe did not stop")
	}
}

func TestLoadBalanceConcurrentRefresh(t *testing.T) {
	a, b := balanceMember("a"), balanceMember("b")
	group := newTestBalance(t, "round_robin", a, b)
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 100 {
				input := balanceInput("example.org")
				_, _ = group.SelectForConnection(&input)
				_ = group.Health()
				_ = group.All()
			}
		})
	}
	for range 100 {
		group.UpdateOutboundsWithChanges([]adapter.Outbound{a, b}, []string{"a"})
	}
	workers.Wait()
}

func TestLoadBalanceHashKeys(t *testing.T) {
	for _, test := range []struct{ name, mode, host, source, expected string }{
		{"registrable domain", "source_ip_domain", "login.Example.COM.", "192.0.2.1:1234", "192.0.2.1\x00example.com"},
		{"multi-label suffix", "source_ip_domain", "api.example.co.uk", "192.0.2.1:1234", "192.0.2.1\x00example.co.uk"},
		{"private suffix", "source_ip_domain", "api.tenant.github.io", "192.0.2.1:1234", "192.0.2.1\x00tenant.github.io"},
		{"full hostname", "source_ip_host", "login.Example.COM.", "192.0.2.1:1234", "192.0.2.1\x00login.example.com"},
		{"destination only", "destination_domain", "api.example.com", "192.0.2.1:1234", "example.com"},
		{"IPv4", "source_ip_domain", "203.0.113.10", "192.0.2.1:1234", "192.0.2.1\x00203.0.113.10"},
		{"IPv6", "source_ip_domain", "2001:db8::1", "[::ffff:192.0.2.1]:1234", "192.0.2.1\x002001:db8::1"},
		{"local hostname", "source_ip_domain", "LOCALHOST", "", "\x00localhost"},
		{"public suffix", "source_ip_domain", "co.uk", "", "\x00co.uk"},
		{"unicode hostname", "source_ip_domain", "api.bücher.de", "", "\x00xn--bcher-kva.de"},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := adapter.InboundContext{Destination: M.Socksaddr{Fqdn: test.host}}
			if test.source != "" {
				metadata.Source = M.ParseSocksaddr(test.source)
			}
			require.Equal(t, test.expected, balanceKey(&metadata, test.mode))
		})
	}
	metadata := adapter.InboundContext{Destination: M.ParseSocksaddr("203.0.113.10:443")}
	require.Equal(t, "\x00203.0.113.10", balanceKey(&metadata, "source_ip_domain"))
	metadata.Domain = "api.example.com"
	require.Equal(t, "\x00example.com", balanceKey(&metadata, "source_ip_domain"))
	metadata.Destination = M.Socksaddr{Fqdn: "api.other.net"}
	require.Equal(t, "\x00other.net", balanceKey(&metadata, "source_ip_domain"))
}

func TestLoadBalanceHashKeyOptions(t *testing.T) {
	for _, mode := range []string{"", "source_ip_domain", "source_ip_host", "destination_domain"} {
		value, err := NewLoadBalance(context.Background(), nil, nil, "balance", option.LoadBalanceOutboundOptions{Outbounds: []string{"a"}, HashKey: mode})
		require.NoError(t, err)
		b := value.(*LoadBalance)
		if mode == "" {
			require.Equal(t, "source_ip_domain", b.hashKey)
		} else {
			require.Equal(t, mode, b.hashKey)
		}
		require.NoError(t, b.close())
	}
	for _, options := range []option.LoadBalanceOutboundOptions{
		{Outbounds: []string{"a"}, HashKey: "invalid"},
		{Outbounds: []string{"a"}, Strategy: "round_robin", HashKey: "source_ip_domain"},
	} {
		_, err := NewLoadBalance(context.Background(), nil, nil, "balance", options)
		require.Error(t, err)
	}
	b := newTestBalance(t, "", balanceMember("a"), balanceMember("b"))
	input, other := balanceInput("login.example.co.uk"), balanceInput("api.example.co.uk")
	selected, err := b.SelectForConnection(&input)
	require.NoError(t, err)
	second, err := b.SelectForConnection(&other)
	require.NoError(t, err)
	require.Equal(t, selected.Tag(), second.Tag())
}

func TestLoadBalanceCanceledProbeWait(t *testing.T) {
	b := newTestBalance(t, "", balanceMember("a"))
	b.probeGate <- struct{}{}
	defer func() { <-b.probeGate }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.URLTest(ctx); done <- err }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled probe waited for another probe")
	}
}

func TestLoadBalancePublishesHealthBeforeBatchCompletes(t *testing.T) {
	fast, slow := balanceMember("fast"), balanceMember("slow")
	started, release := make(chan struct{}), make(chan struct{})
	slow.dial = func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("offline")
	}
	b := newTestBalance(t, "round_robin", fast, slow)
	done := make(chan error, 1)
	go func() { _, err := b.URLTest(context.Background()); done <- err }()
	<-started
	require.Eventually(t, func() bool {
		return b.Health()["fast"].Status == balanceUnhealthy
	}, time.Second, time.Millisecond)
	require.Equal(t, balanceUnknown, b.Health()["slow"].Status)
	input := balanceInput("example.org")
	selected, err := b.SelectForConnection(&input)
	require.NoError(t, err)
	require.Equal(t, "slow", selected.Tag())
	select {
	case <-done:
		t.Fatal("batch finished before slow probe was released")
	default:
	}
	close(release)
	require.NoError(t, <-done)
}

func TestLoadBalanceUnchangedRefresh(t *testing.T) {
	member := balanceMember("a")
	b := newTestBalance(t, "", member)
	<-b.wake
	original := b.members[0]
	b.UpdateOutboundsWithChanges([]adapter.Outbound{member}, []string{"unrelated/member"})
	require.Same(t, original, b.members[0])
	select {
	case <-b.wake:
		t.Fatal("unchanged group scheduled another health check")
	default:
	}
}

func TestLoadBalanceProbeSurvivesOtherMemberReplacement(t *testing.T) {
	stable, replaced := balanceMember("stable"), balanceMember("replaced")
	started, release := make(chan struct{}), make(chan struct{})
	stable.dial = func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("offline")
	}
	b := newTestBalance(t, "", stable, replaced)
	done := make(chan error, 1)
	go func() { _, err := b.URLTest(context.Background()); done <- err }()
	<-started
	// Stable set handles can remain identical while their underlying proxy changes.
	b.UpdateOutboundsWithChanges([]adapter.Outbound{stable, replaced}, []string{"replaced"})
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, balanceUnhealthy, b.Health()["stable"].Status)
	require.Equal(t, balanceUnknown, b.Health()["replaced"].Status)
}

func TestLoadBalanceRoundRobinSkipsIneligible(t *testing.T) {
	a, b, c, d := balanceMember("a"), balanceMember("b"), balanceMember("c", "udp"), balanceMember("d")
	group := newTestBalance(t, "round_robin", a, b, c, d)
	group.byTag["b"].health.Store(&loadBalanceHealth{state: LoadBalanceHealth{Status: balanceUnhealthy}, path: []adapter.Outbound{b}})
	input := balanceInput("example.org")
	for _, tag := range []string{"a", "d", "a", "d"} {
		selected, err := group.SelectForConnection(&input)
		require.NoError(t, err)
		require.Equal(t, tag, selected.Tag())
	}
	// A nested selector's advertised networks can change without a group refresh.
	c.Adapter = outbound.NewAdapter("test", "c", []string{"tcp"}, nil)
	selected, err := group.SelectForConnection(&input)
	require.NoError(t, err)
	require.Equal(t, "a", selected.Tag())
	selected, err = group.SelectForConnection(&input)
	require.NoError(t, err)
	require.Equal(t, "c", selected.Tag())
}

func BenchmarkLoadBalanceSelection(b *testing.B) {
	for _, strategy := range []string{balanceConsistentHash, balanceRoundRobin} {
		for _, size := range []int{10, 100, 1000} {
			b.Run(fmt.Sprintf("%s/%d", strategy, size), func(b *testing.B) {
				value, err := NewLoadBalance(context.Background(), nil, nil, "balance", option.LoadBalanceOutboundOptions{
					Outbounds: []string{"placeholder"},
					Strategy:  strategy,
				})
				if err != nil {
					b.Fatal(err)
				}
				group := value.(*LoadBalance)
				defer group.close()
				members := make([]adapter.Outbound, size)
				for i := range members {
					members[i] = balanceMember(fmt.Sprintf("proxies/member-%04d", i))
				}
				group.UpdateOutbounds(members)
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					input := balanceInput("api.example.co.uk")
					for pb.Next() {
						if _, err := group.SelectForConnection(&input); err != nil {
							b.Error(err)
						}
					}
				})
			})
		}
	}
}

func TestLoadBalanceNestedSelectorStaleProbe(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	old, next := balanceMember("old"), balanceMember("next")
	old.dial = func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, errors.New("old proxy failed")
	}
	raw, err := NewSelector(context.Background(), nil, nil, "nested", option.SelectorOutboundOptions{
		Outbounds: []string{"old", "next"},
	})
	require.NoError(t, err)
	nested := raw.(*Selector)
	nested.UpdateOutbounds([]adapter.Outbound{old, next})
	b := newTestBalance(t, "", nested)
	<-b.wake
	done := make(chan error, 1)
	go func() { _, err := b.URLTest(context.Background()); done <- err }()
	<-started
	require.True(t, nested.SelectOutbound("next"))
	b.UpdateOutboundsWithChanges([]adapter.Outbound{nested}, []string{"old"})
	close(release)
	require.NoError(t, <-done)
	require.NotEqual(t, balanceUnhealthy, b.Health()["nested"].Status, "a failure for the previous selected proxy must not exclude its untested replacement")
}

func TestLoadBalanceUnicodeTrailingDot(t *testing.T) {
	require.Equal(t, normalizeBalanceHost("api.example.com.", true), normalizeBalanceHost("api.example.com。", true))
}

func TestLoadBalanceSelectionAfterNestedSwitch(t *testing.T) {
	old, next := balanceMember("old"), balanceMember("next")
	raw, err := NewSelector(context.Background(), nil, nil, "nested", option.SelectorOutboundOptions{
		Outbounds: []string{"old", "next"},
	})
	require.NoError(t, err)
	nested := raw.(*Selector)
	nested.UpdateOutbounds([]adapter.Outbound{old, next})
	b := newTestBalance(t, "", nested)
	_, err = b.URLTest(context.Background())
	require.NoError(t, err)
	require.Equal(t, balanceUnhealthy, b.Health()["nested"].Status)
	<-b.wake
	require.True(t, nested.SelectOutbound("next"))
	require.Equal(t, balanceUnknown, b.Health()["nested"].Status)
	select {
	case <-b.wake:
		t.Fatal("status inspection scheduled a probe")
	default:
	}
	input := balanceInput("example.org")
	selected, err := b.SelectForConnection(&input)
	require.NoError(t, err)
	require.Same(t, nested, selected)
	select {
	case <-b.wake:
	default:
		t.Fatal("new selection did not schedule a health check")
	}
}

func TestLoadBalanceNestedURLTestActivity(t *testing.T) {
	bad := balanceMember("bad")
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	inner := &URLTest{
		Adapter: outbound.NewAdapter("urltest", "inner", []string{"tcp"}, nil),
		group:   &URLTestGroup{started: true, ticker: ticker, selectedOutboundTCP: bad},
	}
	middle := &URLTest{
		Adapter: outbound.NewAdapter("urltest", "middle", []string{"tcp"}, nil),
		group:   &URLTestGroup{started: true, ticker: ticker, selectedOutboundTCP: inner},
	}
	old := time.Now().Add(-time.Hour)
	inner.group.lastActive.Store(old)
	middle.group.lastActive.Store(old)
	outer := newTestBalance(t, "", middle)
	_, err := outer.URLTest(context.Background())
	require.NoError(t, err)
	require.Equal(t, balanceUnhealthy, outer.Health()["middle"].Status)
	require.True(t, inner.group.lastActive.Load().After(old))
	require.True(t, middle.group.lastActive.Load().After(old))
}

func TestLoadBalanceWakesIdleURLTest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(server.Close)
	bad, good := balanceMember("bad"), balanceMember("good")
	good.dial = func(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, destination.String())
	}
	ctx := service.ContextWithPtr(pause.WithDefaultManager(context.Background()), urltest.NewHistoryStorage())
	innerGroup, err := NewURLTestGroup(ctx, nil, log.NewNOPFactory().Logger(), []adapter.Outbound{bad, good}, server.URL, time.Second, 1, time.Minute, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, innerGroup.Close()) })
	// Simulate a group whose idle timer stopped while the old proxy was selected.
	innerGroup.started = true
	innerGroup.selectedOutboundTCP = bad
	innerGroup.lastActive.Store(time.Now().Add(-time.Hour))
	inner := &URLTest{Adapter: outbound.NewAdapter("urltest", "inner", []string{"tcp"}, nil), group: innerGroup}
	outer := newTestBalance(t, "", inner)
	outer.link = server.URL
	_, err = outer.URLTest(context.Background())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return inner.Selected("tcp") == good
	}, 3*time.Second, time.Millisecond)
	_, err = outer.URLTest(context.Background())
	require.NoError(t, err)
	require.Equal(t, balanceHealthy, outer.Health()["inner"].Status)
	input := balanceInput("example.org")
	selected, err := outer.SelectForConnection(&input)
	require.NoError(t, err)
	require.Same(t, inner, selected)
}
