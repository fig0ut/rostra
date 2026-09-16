package v2rayxhttp

import (
	"crypto/rand"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/sagernet/sing-box/option"

	"github.com/gofrs/uuid/v5"
	"golang.org/x/net/http/httpguts"
)

type (
	metadataField  struct{ placement, key, queryPrefix string }
	metadataConfig struct {
		session, sequence metadataField
		alphabet          string
		length            intRange
	}
)

func newMetadataField(name, placement, key, fallback string) (metadataField, error) {
	if placement == "" {
		placement = "path"
	}
	if key == "" {
		key = fallback
	}

	f := metadataField{placement: placement, key: key}

	switch placement {
	case "path":
	case "query":
		f.queryPrefix = url.QueryEscape(key) + "="
	case "cookie", "header":
		if !httpguts.ValidHeaderFieldName(key) {
			return f, fmt.Errorf("xhttp: invalid %s key %q", name, key)
		}
		if placement == "header" {
			f.key = http.CanonicalHeaderKey(key)
			switch f.key {
			case "Host", "Content-Length", "Content-Type", "Transfer-Encoding", "Connection", "Trailer", "Te", "Upgrade", "Cookie":
				return f, fmt.Errorf("xhttp: %s key conflicts with HTTP transport headers", name)
			}
		}
	default:
		return f, fmt.Errorf("xhttp: invalid %s placement %q", name, placement)
	}
	return f, nil
}

func newMetadataConfig(o option.V2RayXHTTPOptions, request *http.Request) (metadataConfig, error) {
	var c metadataConfig
	var err error

	sessionKey, seqKey := "x_session", "x_seq"

	if o.SessionIDPlacement == "header" {
		sessionKey = "X-Session"
	}
	if o.SeqPlacement == "header" {
		seqKey = "X-Seq"
	}
	c.session, err = newMetadataField("session ID", o.SessionIDPlacement, o.SessionIDKey, sessionKey)
	if err != nil {
		return c, err
	}
	c.sequence, err = newMetadataField("sequence", o.SeqPlacement, o.SeqKey, seqKey)
	if err != nil {
		return c, err
	}
	if c.session.placement != "path" && c.session.placement == c.sequence.placement && c.session.key == c.sequence.key {
		return c, fmt.Errorf("xhttp: session and sequence keys collide")
	}

	paddingPlacement, paddingKey, paddingHeader := o.XPaddingPlacement, o.XPaddingKey, o.XPaddingHeader
	if paddingPlacement == "" {
		paddingPlacement = "queryInHeader"
	}
	if paddingKey == "" {
		paddingKey = "x_padding"
	}
	if paddingHeader == "" {
		paddingHeader = "X-Padding"
	}
	if !o.XPaddingObfsMode {
		paddingPlacement, paddingKey, paddingHeader = "queryInHeader", "x_padding", "Referer"
	}

	for _, field := range []metadataField{c.session, c.sequence} {
		switch field.placement {
		case "header":
			if strings.EqualFold(field.key, paddingHeader) && (paddingPlacement == "header" || paddingPlacement == "queryInHeader" || paddingPlacement == "query") {
				return c, fmt.Errorf("xhttp: metadata header conflicts with padding")
			}
			request.Header.Del(field.key)
		case "cookie":
			if field.key == paddingKey && o.XPaddingObfsMode {
				return c, fmt.Errorf("xhttp: metadata cookie conflicts with padding")
			}
			if _, err := request.Cookie(field.key); err == nil {
				return c, fmt.Errorf("xhttp: configured cookie conflicts with metadata key %q", field.key)
			}
		case "query":
			if field.key == paddingKey && paddingPlacement == "query" {
				return c, fmt.Errorf("xhttp: metadata query conflicts with padding")
			}
			query, err := url.ParseQuery(request.URL.RawQuery)
			if err != nil {
				return c, fmt.Errorf("xhttp: invalid metadata URL query: %w", err)
			}
			query.Del(field.key)
			request.URL.RawQuery = query.Encode()
		}
	}

	c.alphabet = o.SessionIDTable
	upper, lower, digits := "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz", "0123456789"
	switch c.alphabet {
	case "ALPHABET":
		c.alphabet = upper
	case "Alphabet":
		c.alphabet = upper + lower
	case "alphabet":
		c.alphabet = lower
	case "BASE36":
		c.alphabet = digits + upper
	case "base36":
		c.alphabet = digits + lower
	case "Base62":
		c.alphabet = digits + upper + lower
	case "HEX":
		c.alphabet = digits + "ABCDEF"
	case "hex":
		c.alphabet = digits + "abcdef"
	case "number":
		c.alphabet = digits
	}
	if c.alphabet == "" {
		if o.SessionIDLength != nil {
			return c, fmt.Errorf("xhttp: session_id_length requires session_id_table")
		}
		return c, nil
	}

	c.length, err = parseRange("session_id_length", o.SessionIDLength, intRange{}, 1, 4096)
	if err != nil {
		return c, err
	}

	seen := make(map[byte]bool)
	for i := range c.alphabet {
		ch := c.alphabet[i]
		// Unreserved ASCII is safe in every supported placement, without path
		// separators, cookie quoting, or lossy header whitespace normalization.
		if !strings.ContainsRune(upper+lower+digits+"-._~", rune(ch)) || seen[ch] {
			return c, fmt.Errorf("xhttp: session_id_table must contain unique URL-unreserved ASCII characters")
		}
		seen[ch] = true
	}

	if float64(c.length.min)*math.Log2(float64(len(c.alphabet))) < 31 {
		return c, fmt.Errorf("xhttp: shortest session ID must provide at least 31 bits of randomness")
	}

	return c, nil
}

func (f metadataField) apply(request *http.Request, value string) {
	if value == "" {
		return
	}

	switch f.placement {
	case "path":
		separator := ""
		if !strings.HasSuffix(request.URL.Path, "/") {
			separator = "/"
		}

		request.URL.Path += separator + value
		if request.URL.RawPath != "" {
			request.URL.RawPath += separator + value
		}
	case "query":
		if request.URL.RawQuery != "" {
			request.URL.RawQuery += "&"
		}
		request.URL.RawQuery += f.queryPrefix + url.QueryEscape(value)
	case "header":
		request.Header.Set(f.key, value)
	case "cookie":
		request.AddCookie(&http.Cookie{Name: f.key, Value: value})
	}
}

func (c *metadataConfig) newSessionID() (string, error) {
	if c.alphabet == "" {
		id, err := uuid.NewV4()
		return id.String(), err
	}

	value := make([]byte, int(c.length.sample()))
	var entropy [256]byte
	limit := 256 - 256%len(c.alphabet)
	for offset := 0; offset < len(value); {
		if _, err := rand.Read(entropy[:]); err != nil {
			return "", err
		}

		for _, b := range entropy {
			if int(b) >= limit {
				continue
			}

			value[offset] = c.alphabet[int(b)%len(c.alphabet)]

			offset++
			if offset == len(value) {
				break
			}
		}
	}
	return string(value), nil
}
