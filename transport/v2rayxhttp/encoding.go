package v2rayxhttp

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/option"

	"golang.org/x/net/http/httpguts"
)

type uplinkConfig struct {
	method, placement, key string
	chunkSize              intRange
	maxPacketSize          int
}

// Stay within net/http's default limit, including when the local process has
// raised it: peers such as Xray may still use the default.
const maxRequestCookies = 3000

func cookieCount(headers http.Header) int {
	var count int
	for _, line := range headers.Values("Cookie") {
		// Match net/http's pre-parse count, including empty/invalid parts.
		count += strings.Count(line, ";") + 1
	}
	return count
}

func newUplinkConfig(o option.V2RayXHTTPOptions, c clientConfig) (uplinkConfig, error) {
	u := uplinkConfig{method: strings.ToUpper(o.UplinkHTTPMethod), placement: o.UplinkDataPlacement, key: o.UplinkDataKey}
	if u.method == "" {
		u.method = http.MethodPost
	}
	if !httpguts.ValidHeaderFieldName(u.method) || u.method == http.MethodHead || u.method == http.MethodConnect || u.method == http.MethodOptions {
		return u, fmt.Errorf("xhttp: unsupported uplink_http_method %q", u.method)
	}
	if u.method == http.MethodGet && c.mode != modePacket {
		return u, fmt.Errorf("xhttp: GET uploads require packet-up")
	}
	if u.placement == "" {
		u.placement = "auto"
	}
	fallback := c.postSize
	switch u.placement {
	case "auto", "body":
	case "header", "cookie":
		if c.mode != modePacket {
			return u, fmt.Errorf("xhttp: encoded uploads require packet-up")
		}
		fallback = intRange{3000, 4000}
		if u.placement == "cookie" {
			fallback = intRange{2048, 3072}
		}
	default:
		return u, fmt.Errorf("xhttp: invalid uplink_data_placement %q", u.placement)
	}
	if u.key == "" {
		u.key = "X-Data"
		if u.placement == "cookie" {
			u.key = "x_data"
		}
	}
	if !httpguts.ValidHeaderFieldName(u.key) {
		return u, fmt.Errorf("xhttp: invalid uplink_data_key %q", u.key)
	}
	// Xray clamps explicit chunk sizes to at least 64 encoded characters.
	chunk := o.UplinkChunkSize
	if chunk != nil && (chunk.From != 0 || chunk.To != 0) {
		if chunk.From < 0 || chunk.To < chunk.From {
			return u, fmt.Errorf("xhttp: invalid uplink_chunk_size")
		}
		chunk = &option.XHTTPRange{From: max(64, chunk.From), To: max(64, chunk.To)}
	}
	var err error
	u.chunkSize, err = parseRange("uplink_chunk_size", chunk, fallback, 1, 32*1024*1024)
	if err != nil {
		return u, err
	}
	u.maxPacketSize = int(c.postSize.max)
	if u.placement == "cookie" {
		reserved := cookieCount(c.request.Header)
		for _, placement := range []string{c.metadata.session.placement, c.metadata.sequence.placement, c.padding.placement} {
			if placement == "cookie" {
				reserved++
			}
		}
		if reserved >= maxRequestCookies {
			return u, fmt.Errorf("xhttp: configured cookies, metadata and padding leave no room for upload chunks")
		}
		// Use the smallest possible chunk so every random draw is safe. Cap
		// only outgoing packets; the server's configured acceptance limit
		// remains unchanged. Clamp before converting to int for 32-bit builds.
		encodedLimit := min(int64(base64.RawURLEncoding.EncodedLen(u.maxPacketSize)), int64(maxRequestCookies-reserved)*int64(u.chunkSize.min))
		u.maxPacketSize = base64.RawURLEncoding.DecodedLen(int(encodedLimit))
	}
	// Reject names that could overwrite metadata or be mistaken for payload.
	conflicts := func(placement, key string) bool {
		if u.placement == "body" || u.placement != "auto" && u.placement != placement {
			return false
		}
		_, ok := u.chunkIndex(placement, key)
		return ok
	}
	for _, f := range []metadataField{c.metadata.session, c.metadata.sequence} {
		if conflicts(f.placement, f.key) {
			return u, fmt.Errorf("xhttp: upload chunks conflict with metadata key %q", f.key)
		}
	}
	paddingPlacement, paddingKey := c.padding.placement, c.padding.header
	if paddingPlacement == "queryInHeader" {
		paddingPlacement = "header"
	}
	if paddingPlacement == "cookie" {
		paddingKey = o.XPaddingKey
		if paddingKey == "" {
			paddingKey = "x_padding"
		}
	}
	if conflicts(paddingPlacement, paddingKey) {
		return u, fmt.Errorf("xhttp: upload chunks conflict with padding")
	}
	if o.XPaddingObfsMode {
		// The server also accepts cookie/header padding as fallbacks. A
		// payload in those fields would shadow the configured query padding.
		key := o.XPaddingKey
		if key == "" {
			key = "x_padding"
		}
		if conflicts("cookie", key) || conflicts("header", c.padding.header) {
			return u, fmt.Errorf("xhttp: upload chunks shadow padding extraction")
		}
	}
	for name := range o.Headers {
		if conflicts("header", name) {
			return u, fmt.Errorf("xhttp: configured header %q conflicts with upload chunks", name)
		}
	}
	for _, cookie := range c.request.Cookies() {
		if conflicts("cookie", cookie.Name) {
			return u, fmt.Errorf("xhttp: configured cookie %q conflicts with upload chunks", cookie.Name)
		}
	}
	return u, nil
}

func (u uplinkConfig) apply(r *http.Request, packet []byte) {
	r.Method = u.method
	if r.Method == http.MethodGet {
		r.Header.Set("Cache-Control", "no-store")
	}
	if u.placement != "header" && u.placement != "cookie" {
		r.Body = io.NopCloser(bytes.NewReader(packet))
		r.ContentLength = int64(len(packet))
		// Omit GetBody so an ambiguous failure cannot replay the body.
		return
	}
	encoded := base64.RawURLEncoding.EncodeToString(packet)
	var cookies strings.Builder
	if u.placement == "cookie" {
		cookies.WriteString(strings.Join(r.Header.Values("Cookie"), "; "))
	}
	for i := 0; len(encoded) > 0; i++ {
		n := min(len(encoded), int(u.chunkSize.sample()))
		if u.placement == "header" {
			r.Header.Set(u.key+"-"+strconv.Itoa(i), encoded[:n])
		} else {
			if cookies.Len() > 0 {
				cookies.WriteString("; ")
			}
			cookies.WriteString(u.key)
			cookies.WriteByte('_')
			cookies.WriteString(strconv.Itoa(i))
			cookies.WriteByte('=')
			cookies.WriteString(encoded[:n])
		}
		encoded = encoded[n:]
	}
	if u.placement == "cookie" {
		r.Header.Set("Cookie", cookies.String())
	}
	// GET payloads must not be served from a cache shared with another session.
	r.Header.Set("Cache-Control", "no-store")
}

func (u uplinkConfig) chunkIndex(placement, name string) (int, bool) {
	prefix := u.key + "_"
	if placement == "header" {
		prefix, name = strings.ToLower(u.key)+"-", strings.ToLower(name)
	} else if placement != "cookie" {
		return 0, false
	}
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	suffix := strings.TrimPrefix(name, prefix)
	if suffix == "" {
		return 0, false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || strconv.Itoa(n) != suffix {
		return -1, true
	}
	return n, true
}

var errPacketTooLarge = errors.New("xhttp: packet exceeds sc_max_each_post_bytes")

// Read encoded sources in Xray's order: headers, cookies, body. Both the
// encoded input and combined decoded payload are bounded before queue admission.
func (u uplinkConfig) readPacket(r *http.Request, limit int) ([]byte, error) {
	if (u.placement == "auto" || u.placement == "cookie") && cookieCount(r.Header) > maxRequestCookies {
		return nil, fmt.Errorf("xhttp: too many upload cookies")
	}
	var packet []byte
	for _, placement := range []string{"header", "cookie"} {
		if u.placement != "auto" && u.placement != placement {
			continue
		}
		chunks := make(map[int]string)
		size := 0
		add := func(name, value string) error {
			index, ok := u.chunkIndex(placement, name)
			if !ok {
				return nil
			}
			if index < 0 || value == "" || strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("xhttp: invalid upload chunk")
			}
			if _, exists := chunks[index]; exists {
				return fmt.Errorf("xhttp: duplicate upload chunk")
			}
			size += len(value)
			if size > base64.RawURLEncoding.EncodedLen(limit) {
				return errPacketTooLarge
			}
			chunks[index] = value
			if len(chunks) > (base64.RawURLEncoding.EncodedLen(limit)+63)/64 {
				return errPacketTooLarge
			}
			return nil
		}
		if placement == "header" {
			for name, values := range r.Header {
				for _, value := range values {
					if err := add(name, value); err != nil {
						return nil, err
					}
				}
			}
		} else {
			for _, cookie := range r.Cookies() {
				if err := add(cookie.Name, cookie.Value); err != nil {
					return nil, err
				}
			}
		}
		if len(chunks) == 0 {
			continue
		}
		var encoded strings.Builder
		encoded.Grow(size)
		for i := 0; i < len(chunks); i++ {
			chunk, ok := chunks[i]
			if !ok {
				return nil, fmt.Errorf("xhttp: missing upload chunk %d", i)
			}
			if i < len(chunks)-1 && len(chunk) < 64 {
				return nil, fmt.Errorf("xhttp: non-final upload chunks must contain at least 64 characters")
			}
			encoded.WriteString(chunk)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded.String())
		if err != nil {
			return nil, err
		}
		if len(decoded) > limit-len(packet) {
			return nil, errPacketTooLarge
		}
		if packet == nil {
			packet = decoded
		} else {
			packet = append(packet, decoded...)
		}
	}
	if u.placement == "" || u.placement == "auto" || u.placement == "body" {
		if r.ContentLength > int64(limit-len(packet)) {
			return nil, errPacketTooLarge
		}
		if r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit-len(packet))+1))
			if err != nil {
				return nil, err
			}
			if len(body) > limit-len(packet) {
				return nil, errPacketTooLarge
			}
			if packet == nil {
				packet = body
			} else {
				packet = append(packet, body...)
			}
		}
	} else if r.Body != nil {
		// Explicit header/cookie placement ignores the body, as Xray does.
		// Consume it within the same size/time bounds so HTTP/1's implicit
		// body draining cannot hold the connection indefinitely after return.
		n, err := io.Copy(io.Discard, io.LimitReader(r.Body, int64(limit)+1))
		if err != nil {
			return nil, err
		}
		if n > int64(limit) {
			return nil, errPacketTooLarge
		}
	}
	return packet, nil
}
