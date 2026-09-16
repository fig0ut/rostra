package v2rayxhttp

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"

	"golang.org/x/net/http2/hpack"
)

type serverConfig struct {
	path, host, mode, version string
	metadata                  metadataConfig
	uplink                    uplinkConfig
	maxHeaderBytes            int
	padding                   paddingConfig
	paddingKey                string
	paddingSize               intRange
	noSSE                     bool
	obfs, tokenish            bool
	postSize, bufferedPosts   int
	headers                   http.Header
}

func newServerConfig(o option.V2RayXHTTPOptions) (serverConfig, error) {
	if o.DownloadSettings != nil {
		return serverConfig{}, fmt.Errorf("xhttp: download_settings is client-only")
	}
	for name := range o.Headers {
		switch http.CanonicalHeaderKey(name) {
		case "Content-Length", "Transfer-Encoding", "Connection", "Trailer", "Te", "Upgrade", "Keep-Alive", "Proxy-Connection":
			return serverConfig{}, fmt.Errorf("xhttp: response header %q conflicts with HTTP framing", name)
		}
	}
	c := serverConfig{host: o.Host, mode: o.Mode, version: o.HTTPVersion, obfs: o.XPaddingObfsMode, tokenish: o.XPaddingMethod == "tokenish", noSSE: o.NoSSEHeader}
	switch c.mode {
	case "", "auto", modePacket, modeStream, modeSingle:
	default:
		return c, fmt.Errorf("xhttp: invalid server mode %q", c.mode)
	}
	switch c.version {
	case "", "1.1", "2", "3":
	default:
		return c, fmt.Errorf("xhttp: invalid server HTTP version %q", c.version)
	}
	if c.version == "1.1" && (c.mode == modeStream || c.mode == modeSingle) {
		return c, fmt.Errorf("xhttp: streaming uploads require HTTP/2 or HTTP/3")
	}
	// Reuse the pure configuration compiler for path, names, ranges and
	// collision validation. The server does not dial or create a client pool.
	normalized := o
	normalized.Mode, normalized.HTTPVersion = modePacket, "1.1"
	template, err := newConfig(normalized, M.ParseSocksaddr("127.0.0.1:80"), nil)
	if err != nil {
		return c, err
	}
	c.uplink = template.uplink
	if c.mode == modeStream || c.mode == modeSingle {
		if c.uplink.placement == "header" || c.uplink.placement == "cookie" || c.uplink.method == http.MethodGet {
			return c, fmt.Errorf("xhttp: encoded or GET uploads require packet-up")
		}
	}
	c.maxHeaderBytes = o.ServerMaxHeaderBytes
	if c.maxHeaderBytes == 0 {
		c.maxHeaderBytes = 256 * 1024
	}
	if c.maxHeaderBytes < 1024 || c.maxHeaderBytes > 32*1024*1024 {
		return c, fmt.Errorf("xhttp: server_max_header_bytes must be within 1024-33554432")
	}
	c.path, c.metadata = template.request.URL.Path, template.metadata
	c.padding, c.paddingSize = template.padding, template.paddingSize
	c.postSize, c.headers = int(template.postSize.max), template.request.Header.Clone()
	c.bufferedPosts = o.ScMaxBufferedPosts
	if c.bufferedPosts == 0 {
		c.bufferedPosts = 30
	}
	if c.bufferedPosts < 1 || c.bufferedPosts > 1024 {
		return c, fmt.Errorf("xhttp: sc_max_buffered_posts must be within 1-1024")
	}
	c.paddingKey = o.XPaddingKey
	if c.paddingKey == "" {
		c.paddingKey = "x_padding"
	}
	if !c.obfs {
		c.paddingKey = "x_padding"
	}
	return c, nil
}

func (c *serverConfig) metadataFrom(r *http.Request) (string, string, error) {
	var parts []string
	if c.metadata.session.placement == "path" || c.metadata.sequence.placement == "path" {
		if !strings.HasPrefix(r.URL.Path, c.path) {
			return "", "", fmt.Errorf("wrong path")
		}
		suffix := strings.TrimPrefix(r.URL.Path, c.path)
		if suffix != "" {
			parts = strings.Split(suffix, "/")
		}
	} else if r.URL.Path != c.path && r.URL.Path != strings.TrimSuffix(c.path, "/") {
		// Xray omits the trailing slash when neither metadata field is in the path.
		return "", "", fmt.Errorf("wrong path")
	}

	read := func(field metadataField) string {
		switch field.placement {
		case "path":
			if len(parts) == 0 {
				return ""
			}
			value := parts[0]
			parts = parts[1:]
			return value
		case "query":
			return r.URL.Query().Get(field.key)
		case "header":
			return r.Header.Get(field.key)
		case "cookie":
			if cookie, err := r.Cookie(field.key); err == nil {
				return cookie.Value
			}
		}
		return ""
	}

	session, sequence := read(c.metadata.session), read(c.metadata.sequence)

	if len(parts) > 0 || len(session) > 4096 || len(sequence) > 20 {
		return "", "", fmt.Errorf("invalid metadata")
	}

	if sequence != "" {
		if _, err := strconv.ParseUint(sequence, 10, 64); err != nil {
			return "", "", fmt.Errorf("invalid sequence")
		}
	}

	return session, sequence, nil
}

func (c *serverConfig) validPadding(r *http.Request) bool {
	queryValue := func(value string) string {
		u, err := url.Parse(value)
		if err != nil {
			return ""
		}
		return u.Query().Get(c.paddingKey)
	}

	var value string

	if !c.obfs {
		if referer := r.Header.Get("Referer"); referer != "" {
			value = queryValue(referer)
		} else {
			value = r.URL.Query().Get(c.paddingKey)
		}
	} else {
		// Match Xray's extraction precedence, including its legacy-compatible
		// query fallback. Configuration compilation rejects ambiguous field names.
		if cookie, err := r.Cookie(c.paddingKey); err == nil {
			value = cookie.Value
		}
		if value == "" {
			if header := r.Header.Get(c.padding.header); header != "" {
				if c.padding.placement == "header" {
					value = header
				} else {
					value = queryValue(header)
				}
			} else {
				value = r.URL.Query().Get(c.paddingKey)
			}
		}
	}

	if value == "" {
		return false
	}

	size, tolerance := len(value), 0
	if c.tokenish {
		size, tolerance = int(hpack.HuffmanEncodeLength(value)), 2
	}

	return size >= int(c.paddingSize.min)-tolerance && size <= int(c.paddingSize.max)+tolerance
}

func (c *serverConfig) responseHeaders(w http.ResponseWriter) {
	for key, values := range c.headers {
		w.Header()[key] = append([]string(nil), values...)
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	size := int(c.paddingSize.sample())
	value := c.padding.value(size)

	if !c.obfs {
		w.Header().Set("X-Padding", value)
		return
	}
	switch c.padding.placement {
	case "header":
		w.Header().Set(c.padding.header, value)
	case "queryInHeader":
		w.Header().Set(c.padding.header, "?"+url.QueryEscape(c.paddingKey)+"="+value)
	case "cookie":
		http.SetCookie(w, &http.Cookie{Name: c.paddingKey, Value: value, Path: "/"})
	}
}
