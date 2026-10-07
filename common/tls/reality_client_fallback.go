//go:build with_utls

package tls

import (
	"context"
	"crypto/tls"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"

	C "github.com/fig0ut/rostra/constant"

	E "github.com/sagernet/sing/common/exceptions"

	utls "github.com/metacubex/utls"
	"golang.org/x/net/http2"
)

const realityFallbackMaxBody = 1 << 20

func parseRealityFallbackPath(path string) (string, error) {
	if path == "" {
		return "/", nil
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\#") || strings.IndexFunc(path, func(r rune) bool { return r < ' ' || r == 0x7f || unicode.IsSpace(r) }) >= 0 {
		return "", E.New("invalid fallback_path: expected an absolute path with an optional query, without a host or fragment")
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "", E.New("invalid fallback_path: malformed request URI")
	}
	return u.RequestURI(), nil
}

func realityClientFallback(ctx context.Context, uConn *utls.UConn, serverName, path string, fingerprint utls.ClientHelloID) {
	defer uConn.Close()
	ctx, cancel := context.WithTimeout(ctx, C.TCPTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if err := uConn.SetDeadline(deadline); err != nil {
		return
	}
	stop := context.AfterFunc(ctx, func() { uConn.Close() })
	defer stop()
	var claimed atomic.Bool
	dial := func(context.Context, string, string) (net.Conn, error) {
		if !claimed.CompareAndSwap(false, true) {
			return nil, net.ErrClosed
		}
		return uConn, nil
	}
	var transport http.RoundTripper
	switch uConn.ConnectionState().NegotiatedProtocol {
	case "h2":
		transport = &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return dial(ctx, network, addr)
			},
			DisableCompression: true,
		}
	case "", "http/1.1":
		transport = &http.Transport{DialTLSContext: dial, DisableCompression: true}
	default:
		return
	}
	client := &http.Client{Transport: transport}
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	requestURL, err := url.ParseRequestURI(path)
	if err != nil {
		return
	}
	requestURL.Scheme = "https"
	requestURL.Host = serverName
	if ip := net.ParseIP(serverName); ip != nil && strings.Contains(serverName, ":") {
		requestURL.Host = "[" + serverName + "]"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return
	}
	realityFallbackHeaders(request.Header, fingerprint)
	request.AddCookie(&http.Cookie{Name: "padding", Value: strings.Repeat("0", rand.IntN(32)+30)})
	response, err := client.Do(request)
	if err != nil {
		return
	}
	// The response is camouflage, never application data. Consume compressed
	// bytes without decoding, then close after a bounded amount of body data.
	// The HTTP transport may have buffered additional bytes in the meantime.
	_, _ = io.CopyN(io.Discard, response.Body, realityFallbackMaxBody)
	response.Body.Close()
}

// Use the selected uTLS profile's version, rather than guessing the browser's
// current release. These are navigation headers, not an HTTP fingerprint clone:
// net/http and http2 still determine header ordering and HTTP/2 settings.
func realityFallbackHeaders(header http.Header, fingerprint utls.ClientHelloID) {
	version := fingerprint.Version
	var userAgent string
	accept := "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	language := "en-US,en;q=0.9"
	switch fingerprint.Client {
	case utls.HelloFirefox_Auto.Client:
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:" + version + ".0) Gecko/20100101 Firefox/" + version + ".0"
		language = "en-US,en;q=0.5"
	case utls.HelloSafari_Auto.Client:
		userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/" + version + " Safari/605.1.15"
	case utls.HelloIOS_Auto.Client:
		if !strings.Contains(version, ".") {
			version += ".0"
		}
		userAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS " + strings.ReplaceAll(version, ".", "_") + " like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/" + version + " Mobile/15E148 Safari/604.1"
	default:
		// Randomized and other profiles have no matching browser UA. Use the
		// default Chromium profile for their HTTP navigation headers.
		if fingerprint.Client != utls.HelloChrome_Auto.Client && fingerprint.Client != utls.HelloEdge_Auto.Client {
			version = utls.HelloChrome_Auto.Version
		}
		version, _, _ = strings.Cut(version, "_")
		if _, err := strconv.Atoi(version); err != nil {
			version = utls.HelloChrome_Auto.Version
		}
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + version + ".0.0.0 Safari/537.36"
		if fingerprint.Client == utls.HelloEdge_Auto.Client {
			userAgent += " Edg/" + version + ".0.0.0"
		}
		accept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"
		header.Set("Cache-Control", "max-age=0")
	}
	header.Set("User-Agent", userAgent)
	header.Set("Accept", accept)
	header.Set("Accept-Language", language)
	header.Set("Accept-Encoding", "gzip, deflate, br")
	header.Set("Upgrade-Insecure-Requests", "1")
	header.Set("Sec-Fetch-Dest", "document")
	header.Set("Sec-Fetch-Mode", "navigate")
	header.Set("Sec-Fetch-Site", "none")
	if fingerprint.Client != utls.HelloSafari_Auto.Client && fingerprint.Client != utls.HelloIOS_Auto.Client {
		header.Set("Sec-Fetch-User", "?1")
	}
}
