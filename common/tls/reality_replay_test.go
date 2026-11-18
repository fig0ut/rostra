//go:build with_utls

package tls

import (
	"context"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing/common/json/badoption"
)

func TestRealityReplayConfigLifetime(t *testing.T) {
	for _, window := range []time.Duration{0, 5 * time.Minute, -time.Second} {
		t.Run(window.String(), func(t *testing.T) {
			config, err := NewRealityServer(context.Background(), nil, option.InboundTLSOptions{
				ServerName: "example.com",
				Reality: &option.InboundRealityOptions{
					PrivateKey: "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc",
					Handshake: option.InboundRealityHandshakeOptions{
						ServerOptions: option.ServerOptions{
							Server:     "127.0.0.1",
							ServerPort: 443,
						},
					},
					MaxTimeDifference: badoption.Duration(window),
				},
			})
			if window < 0 {
				if err == nil {
					config.Close()
					t.Fatal("negative window accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer config.Close()
			server := config.(*RealityServerConfig)
			want := window
			if want == 0 {
				want = time.Hour
			}
			if server.config.MaxTimeDiff != want {
				t.Fatalf("window = %v, want %v", server.config.MaxTimeDiff, want)
			}
			clone := server.Clone().(*RealityServerConfig)
			if clone.replayCache != server.replayCache {
				t.Fatal("clone has independent replay state")
			}
			clone.Close()
			if server.config.FallbackContext.Err() != nil {
				t.Fatal("closing clone canceled parent fallback")
			}
			other := server.Clone().(*RealityServerConfig)
			defer other.Close()
			server.Close()
			if other.config.FallbackContext.Err() == nil {
				t.Fatal("closing parent did not cancel child fallback")
			}
		})
	}
}

func TestRealityReplayCacheCapacity(t *testing.T) {
	cache := newRealityReplayCache()
	now := time.Now()
	var hello [8]byte
	for i := range realityReplayCapacity {
		binary.BigEndian.PutUint64(hello[:], uint64(i))
		if cache.seen(hello[:], now, now.Add(time.Hour)) {
			t.Fatal("fresh hello rejected before capacity was reached")
		}
	}
	clear(hello[:])
	if !cache.seen(hello[:], now, now.Add(time.Hour)) {
		t.Fatal("oldest unexpired hello accepted again at capacity")
	}
	binary.BigEndian.PutUint64(hello[:], realityReplayCapacity)
	if !cache.seen(hello[:], now, now.Add(time.Hour)) {
		t.Fatal("new hello accepted without space for replay protection")
	}
	if len(cache.entries) != realityReplayCapacity {
		t.Fatal("capacity handling displaced an unexpired hello")
	}
}

func TestRealityReplayCacheConcurrent(t *testing.T) {
	cache := newRealityReplayCache()
	now := time.Now()
	const attempts = 32
	var wg sync.WaitGroup
	results := make(chan bool, attempts)
	for range attempts {
		wg.Go(func() {
			results <- cache.seen([]byte("one authenticated hello"), now, now.Add(time.Hour))
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for replay := range results {
		if !replay {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent copies, want 1", accepted)
	}
}

func TestRealityReplayCacheExpiry(t *testing.T) {
	cache := newRealityReplayCache()
	now := time.Unix(1700000000, 0)
	// A future-dated hello is accepted now and for another two windows.
	future := []byte("future timestamp")
	until := now.Add(2 * time.Hour)
	if cache.seen(future, now, until) {
		t.Fatal("fresh hello rejected")
	}
	// Expirations need not arrive in timestamp order.
	if cache.seen([]byte("past timestamp"), now, now.Add(time.Minute)) {
		t.Fatal("fresh older hello rejected")
	}
	if !cache.seen(future, now.Add(90*time.Minute), until) {
		t.Fatal("future-dated hello expired while still acceptable")
	}
	if len(cache.entries) != 1 {
		t.Fatal("expired older entry was not collected")
	}
	if !cache.seen(future, until, until) {
		t.Fatal("hello accepted again at the inclusive timestamp boundary")
	}
	if !cache.seen(future, until.Add(time.Nanosecond), until) {
		t.Fatal("already-expired hello accepted")
	}
	if len(cache.entries) != 0 {
		t.Fatal("expired entry was not removed")
	}
}
