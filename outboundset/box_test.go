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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/outboundset"
	"github.com/sagernet/sing-box/protocol/group"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

func startHTTPProxy(t *testing.T) (*httptest.Server, <-chan string) {
	t.Helper()
	requests := make(chan string, 16)
	var workers sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "expected CONNECT", http.StatusBadRequest)
			return
		}
		workers.Add(1)
		defer workers.Done()
		upstream, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		upstream.SetDeadline(time.Now().Add(10 * time.Second))
		requests <- r.Host
		if _, err = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err = buffered.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			io.Copy(conn, upstream)
			conn.Close()
			close(done)
		}()
		io.Copy(upstream, buffered)
		upstream.Close()
		<-done
	}))
	t.Cleanup(func() {
		server.Close()
		workers.Wait()
	})
	return server, requests
}

func proxyDefinition(tag string, server *httptest.Server) string {
	host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	return fmt.Sprintf(`{"type":"http","tag":%q,"server":%q,"server_port":%s}`, tag, host, port)
}

func expectProxyDestination(t *testing.T, requests <-chan string, destination string) {
	t.Helper()
	select {
	case got := <-requests:
		require.Equal(t, destination, got)
	case <-time.After(5 * time.Second):
		t.Fatal("proxy was bypassed")
	}
}

func TestBoxRelayIsolation(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "through relay and exit")
	}))
	defer target.Close()
	relayA, requestsA := startHTTPProxy(t)
	relayB, requestsB := startHTTPProxy(t)
	exitA, exitRequestsA := startHTTPProxy(t)
	exitB, exitRequestsB := startHTTPProxy(t)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":1,"outbounds":[%s,%s]}`, proxyDefinition("one", exitA), proxyDefinition("two", exitB))
	}))
	defer source.Close()
	ctx, options := parse(t, fmt.Sprintf(`{
		"log":{"disabled":true},
		"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
		"outbound_set":[
			{"type":"remote","tag":"a","format":"source","url":%q,"override":{"detour":"relay-a"}},
			{"type":"remote","tag":"b","format":"source","url":%q,"override":{"detour":"relay-b"}}
		],
		"outbounds":[
			{"type":"selector","tag":"select-a","outbound_set":"a"},
			{"type":"selector","tag":"select-b","outbound_set":"b"},%s,%s
		]
	}`, source.URL, source.URL, proxyDefinition("relay-a", relayA), proxyDefinition("relay-b", relayB)))
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, instance.Close()) })
	require.NoError(t, instance.Start())
	for _, test := range []struct {
		selector string
		member   string
		relay    <-chan string
		exit     *httptest.Server
		requests <-chan string
	}{
		{"select-a", "a/one", requestsA, exitA, exitRequestsA},
		{"select-b", "b/one", requestsB, exitA, exitRequestsA},
		{"select-a", "a/two", requestsA, exitB, exitRequestsB},
	} {
		t.Run(test.selector+"/"+test.member, func(t *testing.T) {
			outbound, loaded := instance.Outbound().Outbound(test.selector)
			require.True(t, loaded)
			require.True(t, outbound.(*group.Selector).SelectOutbound(test.member))
			conn, err := outbound.DialContext(context.Background(), "tcp", M.ParseSocksaddr(target.Listener.Addr().String()))
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
			_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target.Listener.Addr())
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, "through relay and exit", string(body))
			expectProxyDestination(t, test.relay, test.exit.Listener.Addr().String())
			expectProxyDestination(t, test.requests, target.Listener.Addr().String())
		})
	}
}

func TestBoxRejectsInvalidUpdateBeforeCaching(t *testing.T) {
	var invalid atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if invalid.Load() {
			// Structurally valid, but the direct outbound rejects detour at construction.
			fmt.Fprint(w, `{"version":1,"outbounds":[{"type":"direct","tag":"node","detour":"relay"}]}`)
		} else {
			fmt.Fprint(w, document)
		}
	}))
	defer server.Close()
	ctx, options := parse(t, fmt.Sprintf(`{
		"log":{"disabled":true},
		"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
		"outbound_set":[{"type":"remote","tag":"set","url":%q,"format":"source"}],
		"outbounds":[{"type":"direct","tag":"relay"}]
	}`, server.URL))
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	options.Experimental = &option.ExperimentalOptions{CacheFile: &option.CacheFileOptions{Enabled: true, Path: cachePath}}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	t.Cleanup(func() { instance.Close() })
	require.NoError(t, instance.PreStart())
	paths, err := filepath.Glob(cachePath + ".outbound-set/*.json")
	require.NoError(t, err)
	require.Len(t, paths, 1)
	before, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	invalid.Store(true)
	err = service.PtrFromContext[outboundset.Manager](ctx).Refresh(context.Background(), "set")
	require.ErrorContains(t, err, "not supported in direct context")
	after, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestBoxURLTestMembership(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer target.Close()
	proxy, _ := startHTTPProxy(t)
	ctx, options := parse(t, fmt.Sprintf(`{
		"log":{"disabled":true},
		"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
		"outbound_set":[{"tag":"set","outbounds":[%s]}],
		"outbounds":[{"type":"urltest","tag":"auto","outbound_set":"set","url":%q}]
	}`, proxyDefinition("node", proxy), target.URL))
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	require.NoError(t, err)
	t.Cleanup(func() { instance.Close() })
	// PreStart initializes groups without starting periodic probes or platform
	// network-change callbacks. Run the probe synchronously for this membership test.
	require.NoError(t, instance.PreStart())
	outbound, loaded := instance.Outbound().Outbound("auto")
	require.True(t, loaded)
	require.Equal(t, []string{"set/node"}, outbound.(*group.URLTest).All())
	delays, err := outbound.(*group.URLTest).URLTest(context.Background())
	require.NoError(t, err)
	require.Contains(t, delays, "set/node")
	// Ensure the resolved group can dial as well as populate the UI list.
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	conn, err := outbound.DialContext(context.Background(), "tcp", M.ParseSocksaddrHostPort(host, uint16(port)))
	require.NoError(t, err)
	conn.Close()
}

func TestBoxSelectorDefault(t *testing.T) {
	for _, test := range []struct {
		name     string
		selector string
		selected string
	}{
		{"set member", `"outbound_set":["a","b"],"default":"b/node"`, "b/node"},
		{"first set member", `"outbound_set":["b","a"]`, "b/node"},
		{"mixed members with default", `"outbounds":["direct"],"outbound_set":"a","default":"a/node"`, "a/node"},
		{"first explicit member", `"outbounds":["direct"],"outbound_set":"a"`, "direct"},
		{"explicit set member", `"outbounds":["a/node"],"default":"a/node"`, "a/node"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, options := parse(t, `{
				"log":{"disabled":true},
				"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
				"outbound_set":[
					{"tag":"a","outbounds":[`+member+`]},
					{"tag":"b","outbounds":[`+member+`]}
				],
				"outbounds":[
					{"type":"selector","tag":"select",`+test.selector+`},
					{"type":"direct","tag":"direct"}
				]
			}`)
			instance, err := box.New(box.Options{Context: ctx, Options: options})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, instance.Close()) })
			require.NoError(t, instance.PreStart())
			outbound, loaded := instance.Outbound().Outbound("select")
			require.True(t, loaded)
			selector := outbound.(*group.Selector)
			require.Equal(t, test.selected, selector.Selected("tcp").Tag())
			require.Equal(t, test.selected, selector.Selected("udp").Tag())
		})
	}
}

func TestBoxSelectorCachedSetMember(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	for _, test := range []struct {
		name     string
		selector string
		selected string
		switchTo string
	}{
		{"save selection", `"outbound_set":["a","b"],"default":"a/node"`, "a/node", "b/node"},
		{"cache overrides default", `"outbound_set":["a","b"],"default":"a/node"`, "b/node", ""},
		{"removed member uses default", `"outbound_set":"a","default":"a/node"`, "a/node", ""},
		{"removed member uses first", `"outbound_set":"a"`, "a/node", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, options := parse(t, `{
				"log":{"disabled":true},
				"dns":{"servers":[{"type":"hosts","tag":"hosts"}]},
				"outbound_set":[
					{"tag":"a","outbounds":[`+member+`]},
					{"tag":"b","outbounds":[`+member+`]}
				],
				"outbounds":[{"type":"selector","tag":"select",`+test.selector+`}]
			}`)
			options.Experimental = &option.ExperimentalOptions{CacheFile: &option.CacheFileOptions{Enabled: true, Path: cachePath}}
			instance, err := box.New(box.Options{Context: ctx, Options: options})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, instance.Close()) })
			require.NoError(t, instance.PreStart())
			outbound, loaded := instance.Outbound().Outbound("select")
			require.True(t, loaded)
			selector := outbound.(*group.Selector)
			require.Equal(t, test.selected, selector.Selected("tcp").Tag())
			if test.switchTo != "" {
				require.True(t, selector.SelectOutbound(test.switchTo))
			}
		})
	}
}
