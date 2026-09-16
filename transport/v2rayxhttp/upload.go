package v2rayxhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	maxConcurrentPosts = 8
	// Allow the server's 30-second queue wait plus network overhead. This
	// bounds acknowledgement time, not transmission of the upload payload.
	packetAcknowledgementTimeout = 60 * time.Second
)

// uploadBuffer coalesces writes while the sender waits for its next POST slot.
type uploadBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	data   []byte
	limit  int
	closed bool
}

func newUploadBuffer(limit int) *uploadBuffer {
	b := &uploadBuffer{limit: limit}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *uploadBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := 0
	for len(p) > 0 {
		for len(b.data) == b.limit && !b.closed {
			b.cond.Wait()
		}
		if b.closed {
			return n, io.ErrClosedPipe
		}

		count := min(len(p), b.limit-len(b.data))
		b.data = append(b.data, p[:count]...)
		p = p[count:]
		n += count

		b.cond.Broadcast()
	}

	return n, nil
}

func (b *uploadBuffer) take() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for len(b.data) == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.closed {
		return nil, io.ErrClosedPipe
	}

	data := b.data
	b.data = nil
	b.cond.Broadcast()

	return data, nil
}

func (b *uploadBuffer) Close() error {
	b.mu.Lock()

	b.closed = true
	b.data = nil
	b.cond.Broadcast()

	b.mu.Unlock()
	return nil
}

func (c *Client) uploadPackets(ctx context.Context, conn *streamConn, lease *poolLease, session string) {
	maxSize := min(int(c.config.postSize.sample()), c.config.uplink.maxPacketSize)
	buffer := newUploadBuffer(maxSize)

	defer buffer.Close()

	go func() {
		_, err := io.Copy(buffer, conn.wire)
		buffer.Close()
		if ctx.Err() == nil {
			conn.finish(err)
		}
	}()

	stop := context.AfterFunc(ctx, func() { buffer.Close() })
	defer stop()

	slots := make(chan struct{}, maxConcurrentPosts)
	var last time.Time
	var seq uint64

	for {
		interval := time.Duration(c.config.postInterval.sample()) * time.Millisecond
		if delay := time.Until(last.Add(interval)); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}

		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}

		data, err := buffer.take()
		if err != nil {
			return
		}

		req := c.config.newRequest(ctx, session, strconv.FormatUint(seq, 10), nil, data)
		seq++
		last = time.Now()

		go func() {
			defer func() { <-slots }()
			if err := postPacket(lease, req); err != nil {
				conn.finish(err)
			}
		}()
	}
}

func postPacket(lease *poolLease, request *http.Request) error {
	ctx, cancel := context.WithCancelCause(request.Context())
	var timerMu sync.Mutex
	var timer *time.Timer
	startTimer := func() {
		timerMu.Lock()
		defer timerMu.Unlock()
		if timer == nil && ctx.Err() == nil {
			timer = time.AfterFunc(packetAcknowledgementTimeout, func() {
				cancel(fmt.Errorf("xhttp: packet acknowledgement timeout: %w", os.ErrDeadlineExceeded))
			})
		}
	}
	defer func() {
		// Cancel before stopping the timer so a late WroteRequest callback
		// cannot arm a timer after this request has completed.
		cancel(nil)
		timerMu.Lock()
		if timer != nil {
			timer.Stop()
		}
		timerMu.Unlock()
	}()
	request = request.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				startTimer()
			}
		},
	}))
	requestError := func(err error) error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return err
	}
	response, err := lease.RoundTrip(request)
	if err != nil {
		return requestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("xhttp: upload HTTP status: %s", response.Status)
	}
	// Also cover early responses and transports without WroteRequest traces.
	// Receiving headers must not reset an already-running acknowledgement timer.
	startTimer()
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 65536))
	return requestError(err)
}
