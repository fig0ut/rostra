package outboundset_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/outboundset"
	"github.com/sagernet/sing-box/protocol/group"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

func finishHTTPRequest(t *testing.T, conn net.Conn, target *httptest.Server) {
	t.Helper()
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	_, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target.Listener.Addr())
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))
}

func TestRuntimeUpdateAndConnectionDrain(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer target.Close()
	exitA, requestsA := startHTTPProxy(t)
	exitB, requestsB := startHTTPProxy(t)
	var content atomic.Value
	document := func(definitions ...string) string {
		result := `{"version":1,"outbounds":[`
		for i, definition := range definitions {
			if i > 0 {
				result += ","
			}
			result += definition
		}
		return result + `]}`
	}
	content.Store(document(proxyDefinition("one", exitA), proxyDefinition("two", exitB)))
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, content.Load().(string)) }))
	defer source.Close()
	cache := filepath.Join(t.TempDir(), "cache.db")
	ctx, options := parse(t, fmt.Sprintf(`{
  "log":{"disabled":true}, "dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
  "outbound_set":[{"type":"remote","tag":"set","url":%q,"format":"source"}],
  "outbounds":[
   {"type":"selector","tag":"select","outbound_set":"set","default":"set/one"},
   {"type":"urltest","tag":"auto","outbound_set":"set","url":%q},
   {"type":"http","tag":"static","server":"127.0.0.1","server_port":8080,"detour":"set/one"}
  ],
  "experimental":{"cache_file":{"enabled":true,"path":%q}}
 }`, source.URL, target.URL, cache))
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, instance.Close()) })
	require.NoError(t, instance.PreStart())
	manager := service.PtrFromContext[outboundset.Manager](ctx)
	raw, _ := instance.Outbound().Outbound("select")
	selector := raw.(*group.Selector)
	raw, _ = instance.Outbound().Outbound("auto")
	auto := raw.(*group.URLTest)
	originalHandle, _ := instance.Outbound().Outbound("set/one")
	held, err := originalHandle.DialContext(context.Background(), "tcp", M.ParseSocksaddr(target.Listener.Addr().String()))
	require.NoError(t, err)
	defer held.Close()
	expectProxyDestination(t, requestsA, target.Listener.Addr().String())
	require.True(t, selector.SelectOutbound("set/two"))
	content.Store(document(proxyDefinition("three", exitA), proxyDefinition("one", exitB)))
	require.NoError(t, manager.Refresh(context.Background(), "set"))
	require.Equal(t, []string{"set/three", "set/one"}, selector.All())
	require.Equal(t, selector.All(), auto.All())
	require.Equal(t, "set/one", selector.Selected("tcp").Tag(), "removed selection falls back to default")
	_, exists := instance.Outbound().Outbound("set/two")
	require.False(t, exists)
	newHandle, _ := instance.Outbound().Outbound("set/one")
	require.Same(t, originalHandle, newHandle)
	// This connection was established through the old exit before publication.
	finishHTTPRequest(t, held, target)
	conn, err := originalHandle.DialContext(context.Background(), "tcp", M.ParseSocksaddr(target.Listener.Addr().String()))
	require.NoError(t, err)
	expectProxyDestination(t, requestsB, target.Listener.Addr().String())
	finishHTTPRequest(t, conn, target)
	paths, err := filepath.Glob(cache + ".outbound-set/*.json")
	require.NoError(t, err)
	require.Len(t, paths, 1)
	saved, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	for _, invalid := range []string{
		`{"version":1,"outbounds":[]}`,
		document(proxyDefinition("three", exitA)), // set/one is a static detour.
		document(`{"type":"direct","tag":"one","detour":"static"}`),
		document(`{"type":"direct","tag":"one","detour":"set/three"}`, proxyDefinition("three", exitA)),
	} {
		content.Store(invalid)
		require.Error(t, manager.Refresh(context.Background(), "set"))
		require.Equal(t, []string{"set/three", "set/one"}, selector.All())
		after, err := os.ReadFile(paths[0])
		require.NoError(t, err)
		require.Equal(t, saved, after)
	}
	// Selection by tag survives a successful reorder, even if the default differs.
	require.True(t, selector.SelectOutbound("set/three"))
	content.Store(document(proxyDefinition("one", exitB), proxyDefinition("three", exitA)))
	require.NoError(t, manager.Refresh(context.Background(), "set"))
	require.Equal(t, "set/three", selector.Selected("tcp").Tag())
}

func TestOutboundSetHTTPClientBootstrap(t *testing.T) {
	downloadRelay, requests := startHTTPProxy(t)
	var downloads atomic.Int32
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer subscription" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		downloads.Add(1)
		fmt.Fprint(w, document)
	}))
	defer source.Close()
	for _, mode := range []string{"inline", "named", "default"} {
		t.Run(mode, func(t *testing.T) {
			client := `{"version":1,"detour":"download","headers":{"Authorization":"Bearer subscription"},"tls":{"enabled":true,"insecure":true}}`
			definitions := ""
			if mode != "inline" {
				definitions = `"http_clients":[{"tag":"subscription","version":1,"detour":"download","headers":{"Authorization":"Bearer subscription"},"tls":{"enabled":true,"insecure":true}}],`
				client = `"subscription"`
				if mode == "default" {
					client = "null"
				}
			}
			ctx, options := parse(t, fmt.Sprintf(`{
    "log":{"disabled":true},"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},%s
    "outbound_set":[{"type":"remote","tag":"set","url":%q,"format":"source","http_client":%s}],
    "outbounds":[{"type":"selector","tag":"select","outbound_set":"set"},%s]
   }`, definitions, source.URL, client, proxyDefinition("download", downloadRelay)))
			instance, err := box.New(box.Options{Context: ctx, Options: options})
			require.NoError(t, err)
			defer instance.Close()
			expectProxyDestination(t, requests, source.Listener.Addr().String())
			require.NoError(t, instance.PreStart())
			require.NoError(t, service.PtrFromContext[outboundset.Manager](ctx).Refresh(context.Background(), "set"))
			expectProxyDestination(t, requests, source.Listener.Addr().String())
		})
	}
	require.EqualValues(t, 6, downloads.Load())
}

func TestOutboundSetBootstrapFromLaterInlineSet(t *testing.T) {
	relay, requests := startHTTPProxy(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, document) }))
	defer source.Close()
	ctx, options := parse(t, fmt.Sprintf(`{
		"log":{"disabled":true},"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
		"outbound_set":[
			{"type":"remote","tag":"remote","url":%q,"format":"source","http_client":{"version":1,"detour":"bootstrap"}},
			{"tag":"local","outbounds":[%s]}
		],
		"outbounds":[{"type":"selector","tag":"select","outbound_set":"remote"},{"type":"selector","tag":"bootstrap","outbound_set":"local"}]
	}`, source.URL, proxyDefinition("relay", relay)))
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	defer instance.Close()
	expectProxyDestination(t, requests, source.Listener.Addr().String())
	require.NoError(t, instance.PreStart())
}

func TestOutboundSetBootstrapCycleAndInitialPath(t *testing.T) {
	relay, requests := startHTTPProxy(t)
	var downloads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		fmt.Fprintf(w, `{"version":1,"outbounds":[%s]}`, proxyDefinition("node", relay))
	}))
	defer source.Close()
	config := fmt.Sprintf(`{
  "log":{"disabled":true},"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
  "outbound_set":[{"type":"remote","tag":"set","url":%q,"format":"source","http_client":{"version":1,"detour":"select"}}],
  "outbounds":[{"type":"selector","tag":"select","outbound_set":"set"}]
 }`, source.URL)
	ctx, options := parse(t, config)
	_, err := box.New(box.Options{Context: ctx, Options: options})
	require.ErrorContains(t, err, "provide cache or initial_path")
	require.Zero(t, downloads.Load())
	path := filepath.Join(t.TempDir(), "initial.json")
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`{"version":1,"outbounds":[%s]}`, proxyDefinition("node", relay))), 0o600))
	ctx, options = parse(t, config)
	options.OutboundSet[0].RemoteOptions.InitialPath = path
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	defer instance.Close()
	require.Zero(t, downloads.Load(), "initial_path avoids the bootstrap download")
	require.NoError(t, instance.PreStart())
	require.NoError(t, service.PtrFromContext[outboundset.Manager](ctx).Refresh(context.Background(), "set"))
	expectProxyDestination(t, requests, source.Listener.Addr().String())
	require.EqualValues(t, 1, downloads.Load())
}

func TestOutboundSetPeriodicRefresh(t *testing.T) {
	var content atomic.Value
	content.Store(document)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, content.Load().(string)) }))
	defer server.Close()
	ctx, options := parse(t, fmt.Sprintf(`{
  "log":{"disabled":true},"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
  "outbound_set":[{"type":"remote","tag":"set","url":%q,"format":"source","update_interval":"20ms"}],
  "outbounds":[{"type":"selector","tag":"select","outbound_set":"set","default":"set/node"}]
 }`, server.URL))
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	defer instance.Close()
	require.NoError(t, instance.Start())
	raw, _ := instance.Outbound().Outbound("select")
	selector := raw.(*group.Selector)
	content.Store(`{"version":1,"outbounds":[{"type":"direct","tag":"new"}]}`)
	require.Eventually(t, func() bool { return selector.Selected("tcp").Tag() == "set/new" }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"set/new"}, selector.All())
}

func TestConcurrentSetMembership(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	var version atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":1,"outbounds":[{"type":"direct","tag":"node-%d"}]}`, version.Load())
	}))
	defer source.Close()
	ctx, options := parse(t, fmt.Sprintf(`{
  "log":{"disabled":true},"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
  "outbound_set":[{"type":"remote","tag":"set","url":%q,"format":"source"}],
  "outbounds":[{"type":"selector","tag":"select","outbound_set":"set"},{"type":"urltest","tag":"auto","outbound_set":"set","url":%q}]
 }`, source.URL, target.URL))
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	defer instance.Close()
	require.NoError(t, instance.Start())
	raw, _ := instance.Outbound().Outbound("select")
	selector := raw.(*group.Selector)
	raw, _ = instance.Outbound().Outbound("auto")
	auto := raw.(*group.URLTest)
	var workers sync.WaitGroup
	stop := make(chan struct{})
	for _, group := range []adapter.OutboundGroup{selector, auto} {
		workers.Add(1)
		go func(group adapter.OutboundGroup) {
			defer workers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = group.All()
				_ = group.Selected("tcp")
				_ = group.Selected("udp")
				if group == selector {
					selector.SelectOutbound("set/node-0")
				} else {
					auto.URLTest(context.Background())
				}
			}
		}(group)
	}
	defer func() { close(stop); workers.Wait() }()
	manager := service.PtrFromContext[outboundset.Manager](ctx)
	for i := int32(1); i <= 10; i++ {
		version.Store(i)
		require.NoError(t, manager.Refresh(context.Background(), "set"))
	}
}

func TestCloseCancelsOutboundSetRefresh(t *testing.T) {
	var block atomic.Bool
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block.Load() {
			once.Do(func() { close(started) })
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		fmt.Fprint(w, document)
	}))
	defer server.Close()
	defer close(release)
	ctx, options := parse(t, fmt.Sprintf(`{
  "log":{"disabled":true},"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
  "outbound_set":[{"type":"remote","tag":"set","url":%q,"format":"source"}],
  "outbounds":[{"type":"selector","tag":"select","outbound_set":"set"}]
 }`, server.URL))
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	require.NoError(t, instance.PreStart())
	block.Store(true)
	refreshed := make(chan error, 1)
	go func() {
		refreshed <- service.PtrFromContext[outboundset.Manager](ctx).Refresh(context.Background(), "set")
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- instance.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("close did not cancel the active download")
	}
	require.Error(t, <-refreshed)
	require.ErrorIs(t, service.PtrFromContext[outboundset.Manager](ctx).Refresh(context.Background(), "set"), os.ErrClosed)
}
