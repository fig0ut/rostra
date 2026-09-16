package v2rayxhttp

import (
	"context"
	"io"
	"net/http"
)

// The URL and headers were validated at construction. Clone only the mutable
// request state, then add this stream's ID, sequence and randomized padding.
func (c *clientConfig) newRequest(ctx context.Context, session, sequence string, body io.Reader, packet []byte) *http.Request {
	req := c.request.Clone(ctx)
	c.padding.apply(req, int(c.paddingSize.sample()))
	c.metadata.session.apply(req, session)
	c.metadata.sequence.apply(req, sequence)

	if packet != nil {
		c.uplink.apply(req, packet)
	} else if body != nil {
		req.Method = c.uplink.method
		req.Body = io.NopCloser(body)
		req.ContentLength = -1
		if c.grpcHeader {
			req.Header.Set("Content-Type", "application/grpc")
		}
	}

	return req
}
