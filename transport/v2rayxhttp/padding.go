package v2rayxhttp

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"

	"github.com/sagernet/sing-box/option"

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/http2/hpack"
)

// All prefixes and defaults are compiled once. Requests only generate a value
// and attach it to their own URL or header map.
type paddingConfig struct {
	placement string
	header    string
	prefix    string
	repeated  string
	tokenish  bool
}

func newPaddingConfig(o option.V2RayXHTTPOptions, request *http.Request, size intRange) (paddingConfig, error) {
	p := paddingConfig{placement: o.XPaddingPlacement, header: o.XPaddingHeader}
	if p.placement == "" {
		p.placement = "queryInHeader"
	}
	if p.header == "" {
		p.header = "X-Padding"
	}
	key := o.XPaddingKey
	if key == "" {
		key = "x_padding"
	}
	switch o.XPaddingMethod {
	case "", "repeat-x":
	case "tokenish":
		p.tokenish = true
	default:
		return p, fmt.Errorf("xhttp: unknown x_padding_method %q", o.XPaddingMethod)
	}
	switch p.placement {
	case "queryInHeader", "header", "cookie", "query":
	default:
		return p, fmt.Errorf("xhttp: unknown x_padding_placement %q", p.placement)
	}
	if !httpguts.ValidHeaderFieldName(p.header) {
		return p, fmt.Errorf("xhttp: invalid x_padding_header %q", p.header)
	}
	if p.placement == "cookie" && !httpguts.ValidHeaderFieldName(key) {
		return p, fmt.Errorf("xhttp: invalid padding cookie name %q", key)
	}
	if !o.XPaddingObfsMode {
		p.placement, p.header, key, p.tokenish = "queryInHeader", "Referer", "x_padding", false
	}
	p.header = http.CanonicalHeaderKey(p.header)
	if p.placement == "header" || p.placement == "queryInHeader" {
		switch p.header {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Trailer", "Te", "Upgrade", "Cookie", "Content-Type":
			return p, fmt.Errorf("xhttp: x_padding_header %q conflicts with HTTP transport headers", p.header)
		}
	}
	if o.XPaddingObfsMode {
		if _, err := request.Cookie(key); err == nil {
			return p, fmt.Errorf("xhttp: configured cookie %q conflicts with padding", key)
		}
		if p.placement == "query" && request.Header.Get(p.header) != "" {
			return p, fmt.Errorf("xhttp: configured header %q shadows query padding", p.header)
		}
	}
	switch p.placement {
	case "queryInHeader":
		reference := *request.URL
		reference.RawQuery, reference.ForceQuery = "", false
		p.prefix = reference.String() + "?" + url.QueryEscape(key) + "="
	case "cookie":
		p.prefix = key + "="
		if cookies := request.Header.Get("Cookie"); cookies != "" {
			p.prefix = cookies + "; " + p.prefix
		}
	case "query":
		query, err := url.ParseQuery(request.URL.RawQuery)
		if err != nil {
			return p, fmt.Errorf("xhttp: invalid padding URL query: %w", err)
		}
		query.Del(key)
		p.prefix = query.Encode()
		if p.prefix != "" {
			p.prefix += "&"
		}
		p.prefix += url.QueryEscape(key) + "="
	}
	if !p.tokenish {
		p.repeated = strings.Repeat("X", int(size.max))
	}
	return p, nil
}

func (p *paddingConfig) apply(request *http.Request, size int) {
	value := p.value(size)
	switch p.placement {
	case "header", "queryInHeader":
		request.Header.Set(p.header, p.prefix+value)
	case "cookie":
		request.Header.Set("Cookie", p.prefix+value)
	case "query":
		request.URL.RawQuery = p.prefix + value
	}
}

const paddingAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var paddingCodeBits = func() [len(paddingAlphabet)]int {
	var widths [len(paddingAlphabet)]int
	for i, character := range paddingAlphabet {
		// Eight copies eliminate byte rounding, giving the symbol's bit width.
		widths[i] = int(hpack.HuffmanEncodeLength(strings.Repeat(string(character), 8)))
	}
	return widths
}()

func tokenishPadding(size int) string {
	var value strings.Builder
	value.Grow((size*8 + 4) / 5)
	// Base62 codes occupy 5–8 bits. Until this threshold is crossed, at
	// least eight bits remain, so any next symbol fits. The final encoded
	// length is exactly size, without rescanning or trimming the string.
	// Padding carries no secrets; the concurrency-safe PRNG is sufficient.
	for bits := 0; bits <= (size-1)*8; {
		i := rand.IntN(len(paddingAlphabet))
		value.WriteByte(paddingAlphabet[i])
		bits += paddingCodeBits[i]
	}
	return value.String()
}

func (p *paddingConfig) value(size int) string {
	if p.tokenish {
		return tokenishPadding(size)
	}
	return p.repeated[:size]
}
