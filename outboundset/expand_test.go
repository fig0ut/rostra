package outboundset_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/outboundset"
	"github.com/sagernet/sing-box/schema"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service/filemanager"

	"github.com/stretchr/testify/require"
)

const (
	member   = `{"type":"socks","tag":"node","server":"127.0.0.1","server_port":1080}`
	document = `{"version":1,"outbounds":[` + member + `]}`
)

func parse(t *testing.T, content string) (context.Context, option.Options) {
	t.Helper()
	ctx := include.Context(context.Background())
	options, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(content))
	require.NoError(t, err)
	return ctx, options
}

func expand(ctx context.Context, options option.Options) (option.Options, error) {
	prepared, err := outboundset.Prepare(ctx, log.NewNOPFactory().Logger(), options)
	if err != nil {
		return option.Options{}, err
	}
	prepared.SaveCache()
	return prepared.Options, nil
}

func TestExpandIsolatedSets(t *testing.T) {
	ctx, options := parse(t, `{
		"outbound_set":[
			{"tag":"a","outbounds":[`+member+`],"override":{"detour":"relay-a"}},
			{"tag":"b","outbounds":[`+member+`],"override":{"detour":"relay-b"}}
		],
		"outbounds":[
			{"type":"selector","tag":"select","outbounds":["a/node"],"outbound_set":["a","b","a"],"default":"b/node"},
			{"type":"urltest","tag":"auto","outbound_set":"a"},
			{"type":"direct","tag":"relay-a"},
			{"type":"direct","tag":"relay-b"}
		]
	}`)
	original, err := json.MarshalContext(ctx, options)
	require.NoError(t, err)
	result, err := expand(ctx, options)
	require.NoError(t, err)
	require.Empty(t, result.OutboundSet)
	require.Len(t, result.Outbounds, 6)
	selector := result.Outbounds[0].Options.(*option.SelectorOutboundOptions)
	require.Equal(t, []string{"a/node", "b/node"}, selector.Outbounds)
	require.Equal(t, "b/node", selector.Default)
	require.Empty(t, selector.OutboundSet)
	require.Equal(t, []string{"a/node"}, result.Outbounds[1].Options.(*option.URLTestOutboundOptions).Outbounds)
	require.Equal(t, "relay-a", result.Outbounds[4].Options.(*option.SOCKSOutboundOptions).Detour)
	require.Equal(t, "relay-b", result.Outbounds[5].Options.(*option.SOCKSOutboundOptions).Detour)
	after, err := json.MarshalContext(ctx, options)
	require.NoError(t, err)
	require.Equal(t, original, after, "expansion must not mutate caller-owned configuration")
	second, err := expand(ctx, options)
	require.NoError(t, err)
	require.Equal(t, result, second, "reloading the same options must not accumulate members")
	require.NotSame(t, result.Outbounds[4].Options, second.Outbounds[4].Options)
}

func TestOverrideMerge(t *testing.T) {
	ctx, options := parse(t, `{
		"outbound_set":[{"tag":"proxies","outbounds":[{
			"type":"http","tag":"node","server":"127.0.0.1","server_port":8080,
			"detour":"old-relay","username":"remove-me","tcp_fast_open":true,
			"tls":{"enabled":true,"server_name":"example.org","insecure":true,"alpn":["h2","http/1.1"]}
		}],"override":{
			"detour":"relay","username":null,"tcp_fast_open":false,"server_port":0,
			"tls":{"insecure":false,"alpn":["http/1.1"]}
		}}],
		"outbounds":[{"type":"direct","tag":"relay"}]
	}`)
	result, err := expand(ctx, options)
	require.NoError(t, err)
	o := result.Outbounds[1].Options.(*option.HTTPOutboundOptions)
	require.Equal(t, "relay", o.Detour)
	require.Empty(t, o.Username)
	require.False(t, o.TCPFastOpen)
	require.Zero(t, o.ServerPort)
	require.True(t, o.TLS.Enabled)
	require.False(t, o.TLS.Insecure)
	require.Equal(t, "example.org", o.TLS.ServerName)
	require.EqualValues(t, []string{"http/1.1"}, o.TLS.ALPN)
}

func TestSourceLocalDetour(t *testing.T) {
	ctx, options := parse(t, `{"outbound_set":[{"tag":"set","outbounds":[
		{"type":"socks","tag":"exit","server":"127.0.0.1","server_port":1080,"detour":"node"},`+member+`
	]}]}`)
	result, err := expand(ctx, options)
	require.NoError(t, err)
	require.Equal(t, "set/node", result.Outbounds[0].Options.(*option.SOCKSOutboundOptions).Detour)
	// An override is a global reference, even when a source member shares its name.
	options.OutboundSet[0].Override = map[string]json.RawMessage{"detour": []byte(`"node"`)}
	options.Outbounds = []option.Outbound{{Type: "direct", Tag: "node", Options: &option.DirectOutboundOptions{}}}
	result, err = expand(ctx, options)
	require.NoError(t, err)
	require.Equal(t, "node", result.Outbounds[1].Options.(*option.SOCKSOutboundOptions).Detour)
}

func TestInvalidConfiguration(t *testing.T) {
	for _, test := range []struct{ name, config, message string }{
		{"missing set", `{"outbounds":[{"type":"selector","tag":"select","outbound_set":"missing"}]}`, "outbound-set not found"},
		{"duplicate set", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `]},{"tag":"a","outbounds":[` + member + `]}]}`, "duplicate outbound-set"},
		{"duplicate member", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `,` + member + `]}]}`, "duplicate member tag"},
		{"missing member tag", `{"outbound_set":[{"tag":"a","outbounds":[{"type":"direct"}]}]}`, "missing or invalid tag"},
		{"missing set tag", `{"outbound_set":[{"outbounds":[` + member + `]}]}`, "missing outbound-set tag"},
		{"empty set", `{"outbound_set":[{"tag":"a","outbounds":[]}]}`, "empty inline"},
		{"tag override", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `],"override":{"tag":"renamed"}}]}`, "must not change tag"},
		{"type override", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `],"override":{"type":null}}]}`, "must not change type"},
		{"unknown override", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `],"override":{"unknown":true}}]}`, "unknown field"},
		{"incompatible override", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `],"override":{"tls":{"enabled":true}}}]}`, "unknown field"},
		{"nested group", `{"outbound_set":[{"tag":"a","outbounds":[{"type":"selector","tag":"group","outbounds":["direct"]}]}]}`, "not groups"},
		{"missing relay", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `],"override":{"detour":"missing"}}]}`, "dependency not found"},
		{"cycle", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `],"override":{"detour":"relay"}}],"outbounds":[{"type":"selector","tag":"relay","outbound_set":"a"}]}`, "circular outbound dependency"},
		{"collision", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `]}],"outbounds":[{"type":"direct","tag":"a/node"}]}`, "duplicate outbound/endpoint tag"},
		{"bad default", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `]}],"outbounds":[{"type":"selector","tag":"s","outbound_set":"a","default":"node"}]}`, "default outbound is not a member"},
		{"default outside membership", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `]},{"tag":"b","outbounds":[` + member + `]}],"outbounds":[{"type":"selector","tag":"s","outbound_set":"a","default":"b/node"}]}`, "default outbound is not a member"},
		{"missing default member", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `]}],"outbounds":[{"type":"selector","tag":"s","outbound_set":"a","default":"a/missing"}]}`, "default outbound is not a member"},
		{"bad format", `{"outbound_set":[{"type":"local","tag":"a","path":"a.srs","format":"binary"}]}`, "unknown outbound-set format"},
		{"unknown field", `{"outbound_set":[{"tag":"a","outbounds":[` + member + `],"update_interval":"1h"}]}`, "unknown field"},
		{"remote bad scheme", `{"outbound_set":[{"type":"remote","tag":"a","url":"file:///tmp/a.json"}]}`, "HTTP or HTTPS"},
		{"negative update interval", `{"outbound_set":[{"type":"remote","tag":"a","url":"https://example.com/a.json","update_interval":"-1s"}]}`, "must be positive"},
		{"missing HTTP client", `{"outbound_set":[{"type":"remote","tag":"a","url":"https://example.com/a.json","http_client":"missing"}]}`, "http_client not found"},
		{"multiple inline tags", `{"outbound_set":[{"tag":["a","b"],"outbounds":[` + member + `]}]}`, "multiple tags"},
		{"missing placeholder", `{"outbound_set":[{"type":"local","tag":["a","b"],"path":"a.json"}]}`, "placeholder"},
		{"namespace delimiter", `{"outbound_set":[{"tag":"a/b","outbounds":[` + member + `]}]}`, "must not contain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := include.Context(context.Background())
			options, err := json.UnmarshalExtendedContext[option.Options](ctx, []byte(test.config))
			if err == nil {
				_, err = expand(ctx, options)
			}
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestLocalSourceAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), []byte(document), 0o600))
	}
	ctx, options := parse(t, `{"outbound_set":[{"type":"local","tag":["a","b"],"path":"{tag}.json"}]}`)
	ctx = filemanager.WithDefault(ctx, dir, dir, os.Getuid(), os.Getgid())
	content, err := json.MarshalContext(ctx, options)
	require.NoError(t, err)
	options, err = json.UnmarshalExtendedContext[option.Options](ctx, content)
	require.NoError(t, err)
	result, err := expand(ctx, options)
	require.NoError(t, err)
	require.Equal(t, "a/node", result.Outbounds[0].Tag)
	require.Equal(t, "b/node", result.Outbounds[1].Tag)
	// Source format remains strict, including its version and document boundary.
	for _, invalid := range []string{
		`{"outbounds":[` + member + `]}`,
		`{"version":2,"outbounds":[` + member + `]}`,
		`{"version":1,"outbounds":[` + member + `],"unknown":1}`,
		document + `{}`,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(invalid), 0o600))
		_, err = expand(ctx, options)
		require.Error(t, err)
	}
}

func TestRemoteCache(t *testing.T) {
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			fmt.Fprint(w, document)
		case 1:
			fmt.Fprint(w, `{"version":1,"outbounds":[{"type":"invalid","tag":"bad"}]}`)
		case 2:
			http.Error(w, "offline", http.StatusServiceUnavailable)
		case 3:
			fmt.Fprint(w, `{"version":1,"outbounds":[{"type":"socks","tag":"node","server":"127.0.0.1","server_port":1080,"detour":"missing"}]}`)
		}
	}))
	defer server.Close()
	ctx, options := parse(t, fmt.Sprintf(`{"outbound_set":[{"type":"remote","tag":"remote","url":%q,"format":"source"}]}`, server.URL))
	options.Experimental = &option.ExperimentalOptions{CacheFile: &option.CacheFileOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache.db")}}
	result, err := expand(ctx, options)
	require.NoError(t, err)
	require.Equal(t, "remote/node", result.Outbounds[0].Tag)
	paths, err := filepath.Glob(options.Experimental.CacheFile.Path + ".outbound-set/*.json")
	require.NoError(t, err)
	require.Len(t, paths, 1)
	info, err := os.Stat(paths[0])
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Zero(t, info.Mode().Perm()&0o077, "cache contains proxy credentials")
	}
	for _, value := range []int32{1, 2} {
		mode.Store(value)
		cached, err := expand(ctx, options)
		require.NoError(t, err)
		require.Equal(t, result, cached)
	}
	mode.Store(3)
	_, err = expand(ctx, options)
	require.NoError(t, err, "startup uses the validated cache before attempting a refresh")
	stored, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	require.Equal(t, document, string(stored), "invalid dependency graph must not poison cache")
	mode.Store(2)
	// Overrides are reapplied to source data, not persisted into shared source data.
	options.OutboundSet[0].Override = map[string]json.RawMessage{"server_port": []byte("1234")}
	result, err = expand(ctx, options)
	require.NoError(t, err)
	require.EqualValues(t, 1234, result.Outbounds[0].Options.(*option.SOCKSOutboundOptions).ServerPort)
	options.OutboundSet[0].RemoteOptions.URL += "/different"
	_, err = expand(ctx, options)
	require.ErrorContains(t, err, "initial download failed")
	options.OutboundSet[0].RemoteOptions.URL = server.URL
	options.Experimental.CacheFile.CacheID = "different"
	_, err = expand(ctx, options)
	require.ErrorContains(t, err, "initial download failed")
}

func TestRemoteInitialPathAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "initial.json")
	require.NoError(t, os.WriteFile(path, []byte(document), 0o600))
	ctx, options := parse(t, fmt.Sprintf(`{"outbound_set":[{"type":"remote","tag":"remote","url":%q,"format":"source","initial_path":%q}]}`, server.URL, path))
	result, err := expand(ctx, options)
	require.NoError(t, err)
	require.Equal(t, "remote/node", result.Outbounds[0].Tag)
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = expand(ctx, options)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSourceSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.json")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat(" ", (16<<20)+1)), 0o600))
	ctx, options := parse(t, fmt.Sprintf(`{"outbound_set":[{"type":"local","tag":"large","path":%q}]}`, path))
	_, err := expand(ctx, options)
	require.ErrorContains(t, err, "exceeds 16 MiB")
}

func TestSchema(t *testing.T) {
	content, err := schema.Generate(include.Context(context.Background()), reflect.TypeFor[option.Options]())
	require.NoError(t, err)
	var generated map[string]any
	require.NoError(t, json.Unmarshal(content, &generated))
	require.Contains(t, generated["properties"], "outbound_set")
	require.Contains(t, generated["$defs"], "OutboundSet")
}
