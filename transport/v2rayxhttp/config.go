package v2rayxhttp

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http/httpguts"
)

const (
	modePacket = "packet-up"
	modeStream = "stream-up"
	modeSingle = "stream-one"
)

// clientConfig is immutable. Construction resolves defaults, validates fields,
// and compiles the request template; dialing never parses configuration again.
type clientConfig struct {
	request      *http.Request
	metadata     metadataConfig
	server       M.Socksaddr
	padding      paddingConfig
	paddingSize  intRange
	postSize     intRange
	postInterval intRange
	mode         string
	version      string
	grpcHeader   bool
	pool         poolConfig
	uplink       uplinkConfig
}

type intRange struct {
	min int32
	max int32
}

func (r intRange) sample() int32 {
	if r.min == r.max {
		return r.min
	}
	return r.min + rand.Int32N(r.max-r.min+1)
}

func parseRange(name string, value *option.XHTTPRange, fallback intRange, min, max int32) (intRange, error) {
	r := fallback
	if value != nil && (value.From != 0 || value.To != 0) {
		r = intRange{value.From, value.To}
	}
	if r.min < min || r.max < r.min || r.max > max {
		return intRange{}, fmt.Errorf("xhttp: %s must be an ordered range within %d-%d", name, min, max)
	}
	return r, nil
}

func newConfig(o option.V2RayXHTTPOptions, server M.Socksaddr, tc tls.Config) (clientConfig, error) {
	c := clientConfig{server: server, mode: o.Mode, version: o.HTTPVersion, grpcHeader: !o.NoGRPCHeader}

	if c.mode == "" || c.mode == "auto" {
		c.mode = modePacket
		if isReality(tc) {
			c.mode = modeSingle
			if o.DownloadSettings != nil {
				c.mode = modeStream
			}
		}
	}
	if c.mode != modePacket && c.mode != modeStream && c.mode != modeSingle {
		return c, fmt.Errorf("xhttp: unknown mode %q", c.mode)
	}
	if c.version == "" {
		c.version = "1.1"
		if tc != nil {
			c.version = "2"
			if alpn := tc.NextProtos(); len(alpn) == 1 {
				switch alpn[0] {
				case "h3":
					c.version = "3"
				case "http/1.1":
					c.version = "1.1"
				}
			}
		}
	}
	if c.version != "1.1" && c.version != "2" && c.version != "3" {
		return c, fmt.Errorf("xhttp: unknown HTTP version %q", c.version)
	}
	if c.version == "3" && tc == nil {
		return c, fmt.Errorf("xhttp: HTTP/3 requires TLS")
	}
	if isReality(tc) && c.version != "2" {
		return c, fmt.Errorf("xhttp: REALITY requires HTTP/2")
	}
	if c.mode != modePacket && c.version == "1.1" {
		return c, fmt.Errorf("xhttp: streaming uploads require HTTP/2 or HTTP/3")
	}
	if c.mode == modeSingle && o.DownloadSettings != nil {
		return c, fmt.Errorf("xhttp: download_settings cannot be used with stream-one")
	}

	var err error
	c.paddingSize, err = parseRange("x_padding_bytes", o.XPaddingBytes, intRange{100, 1000}, 1, 65536)
	if err != nil {
		return c, err
	}
	c.postSize, err = parseRange("sc_max_each_post_bytes", o.ScMaxEachPostBytes, intRange{1000000, 1000000}, 1, 16*1024*1024)
	if err != nil {
		return c, err
	}
	c.postInterval, err = parseRange("sc_min_posts_interval_ms", o.ScMinPostsIntervalMs, intRange{30, 30}, 0, 60000)
	if err != nil {
		return c, err
	}
	c.pool, err = newPoolConfig(o.Xmux)
	if err != nil {
		return c, err
	}
	path := o.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u, err := url.ParseRequestURI(path)
	if err != nil {
		return c, fmt.Errorf("xhttp: invalid path: %w", err)
	}
	if u.IsAbs() || u.Host != "" || strings.Contains(path, "#") {
		return c, fmt.Errorf("xhttp: path must be an HTTP request path")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		if u.RawPath != "" {
			u.RawPath += "/"
		}
	}
	u.Scheme = "http"
	if tc != nil {
		u.Scheme = "https"
	}
	u.Host = o.Host
	if u.Host == "" && tc != nil {
		u.Host = tc.ServerName()
	}
	if u.Host == "" {
		u.Host = server.String()
	}
	if !httpguts.ValidHostHeader(u.Host) {
		return c, fmt.Errorf("xhttp: invalid host %q", u.Host)
	}
	headers := make(http.Header, len(o.Headers)+2)
	for name, value := range o.Headers {
		if strings.EqualFold(name, "Host") || !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
			return c, fmt.Errorf("xhttp: invalid header %q; set host separately", name)
		}
		headers.Set(name, value)
	}
	// Check before metadata/padding validation calls Request.Cookie, which
	// silently returns no cookies when the parser's count limit is exceeded.
	if cookieCount(headers) > maxRequestCookies {
		return c, fmt.Errorf("xhttp: too many configured cookies")
	}

	c.request = &http.Request{Method: http.MethodGet, URL: u, Host: u.Host, Header: headers}
	c.metadata, err = newMetadataConfig(o, c.request)
	if err != nil {
		return c, err
	}

	c.padding, err = newPaddingConfig(o, c.request, c.paddingSize)
	if err != nil {
		return c, err
	}
	c.uplink, err = newUplinkConfig(o, c)
	if err != nil {
		return c, err
	}

	return c, nil
}

type poolConfig struct {
	connections intRange
	concurrency intRange
	reuse       intRange
	requests    intRange
	lifetime    intRange
	keepAlive   time.Duration
}

func newPoolConfig(o *option.V2RayXHTTPXMuxOptions) (poolConfig, error) {
	c := poolConfig{connections: intRange{3, 3}, requests: intRange{600, 900}, lifetime: intRange{1800, 3000}, keepAlive: 30 * time.Second}
	if o == nil {
		return c, nil
	}

	// A configured XMUX object starts with unlimited counters. Apply defaults
	// only to the empty object, matching Xray's configuration semantics.
	c = poolConfig{keepAlive: 30 * time.Second}

	var err error
	for _, field := range []struct {
		name   string
		option *option.XHTTPRange
		target *intRange
		limit  int32
	}{
		{"xmux.max_connections", o.MaxConnections, &c.connections, 1024},
		{"xmux.max_concurrency", o.MaxConcurrency, &c.concurrency, 1 << 30},
		{"xmux.c_max_reuse_times", o.CMaxReuseTimes, &c.reuse, 1 << 30},
		{"xmux.h_max_request_times", o.HMaxRequestTimes, &c.requests, 1 << 30},
		{"xmux.h_max_reusable_secs", o.HMaxReusableSecs, &c.lifetime, 1 << 30},
	} {
		*field.target, err = parseRange(field.name, field.option, intRange{}, 0, field.limit)
		if err != nil {
			return c, err
		}
	}

	if c.connections.max > 0 && c.concurrency.max > 0 {
		return c, fmt.Errorf("xhttp: xmux max_connections and max_concurrency are mutually exclusive")
	}
	if o.HKeepAlivePeriod < -1 || o.HKeepAlivePeriod > 86400 {
		return c, fmt.Errorf("xhttp: h_keep_alive_period must be -1 through 86400")
	}
	if o.HKeepAlivePeriod == -1 {
		c.keepAlive = 0
	} else if o.HKeepAlivePeriod > 0 {
		c.keepAlive = time.Duration(o.HKeepAlivePeriod) * time.Second
	}
	if c.connections.max == 0 && c.concurrency.max == 0 && c.reuse.max == 0 && c.requests.max == 0 && c.lifetime.max == 0 && o.HKeepAlivePeriod == 0 {
		return newPoolConfig(nil)
	}

	return c, nil
}
