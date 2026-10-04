package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/group"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/observable"
	"github.com/sagernet/sing/service"
	"github.com/stretchr/testify/require"
)

type healthTestOutbound struct{ outbound.Adapter }

func (*healthTestOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, destination.String())
}
func (*healthTestOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("unused")
}

type healthTestManager struct {
	adapter.OutboundManager
	values []adapter.Outbound
}

func (m *healthTestManager) Outbounds() []adapter.Outbound { return m.values }
func (m *healthTestManager) Outbound(tag string) (adapter.Outbound, bool) {
	for _, value := range m.values {
		if value.Tag() == tag {
			return value, true
		}
	}
	return nil, false
}

func TestLoadBalanceGroupHealthReporting(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer healthy.Close()
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer failed.Close()
	history := urltest.NewHistoryStorage()
	existing := &adapter.URLTestHistory{Time: time.Now(), Delay: 999}
	history.StoreURLTestHistory("shared", existing)
	subscriber := observable.NewSubscriber[struct{}](8)
	defer subscriber.Close()
	history.AddUpdateHook(subscriber)
	events, _ := subscriber.Subscription()
	ctx, cancel := context.WithCancel(service.ContextWithPtr(context.Background(), history))
	defer cancel()
	leaf := &healthTestOutbound{outbound.NewAdapter("test", "shared", []string{"tcp"}, nil)}
	var balances []*group.LoadBalance
	for i, link := range []string{healthy.URL, failed.URL} {
		tag := []string{"healthy-group", "failed-group"}[i]
		raw, err := group.NewLoadBalance(ctx, nil, nil, tag, option.LoadBalanceOutboundOptions{Outbounds: []string{"shared"}, URL: link})
		require.NoError(t, err)
		balance := raw.(*group.LoadBalance)
		balance.UpdateOutbounds([]adapter.Outbound{leaf})
		_, err = balance.URLTest(context.Background())
		require.NoError(t, err)
		select {
		case <-events:
		case <-time.After(time.Second):
			t.Fatal("health check did not notify subscribers")
		}
		balances = append(balances, balance)
	}
	require.Equal(t, "healthy", balances[0].Health()["shared"].Status)
	require.Equal(t, "unhealthy", balances[1].Health()["shared"].Status)
	require.Same(t, existing, history.LoadURLTestHistory("shared"), "per-group health must not overwrite shared history")
	server := &StartedService{instance: &Instance{outboundManager: &healthTestManager{values: []adapter.Outbound{leaf, balances[0], balances[1]}}, urlTestHistoryStorage: history}}
	groups := server.readGroups()
	require.Len(t, groups.Group, 2)
	for i, g := range groups.Group {
		require.Empty(t, g.Selected)
		require.Len(t, g.Items, 1)
		state := balances[i].Health()["shared"]
		require.Equal(t, state.CheckedAt.Unix(), g.Items[0].UrlTestTime)
		if i == 0 {
			require.Equal(t, int32(state.Delay), g.Items[0].UrlTestDelay)
		} else {
			require.Zero(t, g.Items[0].UrlTestDelay)
		}
	}
}
