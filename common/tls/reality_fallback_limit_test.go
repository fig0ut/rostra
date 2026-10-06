//go:build with_utls

package tls

import (
	"bytes"
	"context"
	stdtls "crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/fig0ut/rostra/option"

	M "github.com/sagernet/sing/common/metadata"

	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

func TestRealityFallbackLimitConfiguration(t *testing.T) {
	for _, field := range []string{"limit_fallback_upload", "limit_fallback_download"} {
		for _, limits := range []string{
			`{"after_bytes": -1}`, `{"bytes_per_sec": -1}`, `{"burst_bytes_per_sec": -1}`,
			`{"bytes_per_sec": 1.5}`, `{"after_bytes": 18446744073709551616}`,
		} {
			var options option.InboundRealityOptions
			require.Error(t, json.Unmarshal([]byte(`{"`+field+`":`+limits+`}`), &options))
		}
		for _, limits := range []string{
			`{"after_bytes": 9223372036854775808}`,
			`{"bytes_per_sec": 9223372036854775808}`,
			`{"burst_bytes_per_sec": 9223372036854775808}`,
		} {
			var options option.InboundRealityOptions
			require.NoError(t, json.Unmarshal([]byte(`{"`+field+`":`+limits+`}`), &options))
			options.PrivateKey = realityTestPrivateKey
			_, err := NewRealityServer(t.Context(), nil, option.InboundTLSOptions{Reality: &options})
			require.ErrorContains(t, err, field)
		}
	}
	var options option.InboundRealityOptions
	require.NoError(t, json.Unmarshal([]byte(`{
		"limit_fallback_upload": {"after_bytes": 8192, "bytes_per_sec": 65536, "burst_bytes_per_sec": 131072},
		"limit_fallback_download": {"after_bytes": 9223372036854775807, "bytes_per_sec": 0, "burst_bytes_per_sec": 1}
	}`), &options))
	options.PrivateKey = realityTestPrivateKey
	config, err := NewRealityServer(t.Context(), nil, option.InboundTLSOptions{Reality: &options})
	require.NoError(t, err)
	defer config.Close()
	clone := config.Clone().(*RealityServerConfig)
	defer clone.Close()
	require.Equal(t, utls.RealityLimitFallback{AfterBytes: 8192, BytesPerSec: 65536, BurstBytesPerSec: 131072}, clone.config.LimitFallbackUpload)
	require.Equal(t, utls.RealityLimitFallback{AfterBytes: 9223372036854775807, BurstBytesPerSec: 1}, clone.config.LimitFallbackDownload)

	options.LimitFallbackUpload, options.LimitFallbackDownload = nil, nil
	config, err = NewRealityServer(t.Context(), nil, option.InboundTLSOptions{Reality: &options})
	require.NoError(t, err)
	defer config.Close()
	require.Zero(t, config.(*RealityServerConfig).config.LimitFallbackUpload)
	require.Zero(t, config.(*RealityServerConfig).config.LimitFallbackDownload)
}

func TestRealityFallbackBandwidth(t *testing.T) {
	for _, direction := range []string{"upload", "download", "authenticated", "disabled"} {
		t.Run(direction, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			targetConfig := realityTestTargetTLSConfig(t, stdtls.X25519MLKEM768)
			target := realityTestListener(t, func(conn net.Conn) {
				client := stdtls.Server(conn, targetConfig)
				defer client.Close()
				if client.Handshake() == nil {
					io.Copy(client, client)
				}
			})
			address := M.ParseSocksaddr(target.Addr().String())
			options := option.InboundRealityOptions{
				PrivateKey: realityTestPrivateKey,
				ShortID:    []string{"0123456789abcdef"},
				Handshake:  option.InboundRealityHandshakeOptions{ServerOptions: option.ServerOptions{Server: address.AddrString(), ServerPort: address.Port}},
			}
			limit := &option.RealityLimitFallbackOptions{BytesPerSec: 256 * 1024}
			switch direction {
			case "upload":
				options.LimitFallbackUpload = limit
			case "download":
				options.LimitFallbackDownload = limit
			case "authenticated":
				// This would take at least seven seconds if applied to tunnel data.
				limit.BytesPerSec = 64 * 1024
				options.LimitFallbackUpload, options.LimitFallbackDownload = limit, limit
			case "disabled":
				limit.BytesPerSec, limit.BurstBytesPerSec = 0, 1
				options.LimitFallbackUpload, options.LimitFallbackDownload = limit, limit
			}
			server, err := NewRealityServer(ctx, nil, option.InboundTLSOptions{ServerName: "example.com", Reality: &options})
			require.NoError(t, err)
			defer server.Close()
			listener := realityTestListener(t, func(conn net.Conn) {
				client, err := ServerHandshake(ctx, conn, server)
				if err == nil {
					defer client.Close()
					io.Copy(client, client)
				}
			})
			conn, err := net.Dial("tcp", listener.Addr().String())
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
			var client net.Conn
			if direction == "authenticated" {
				client, err = ClientHandshake(ctx, conn, realityTestClientConfig(t, ""))
				require.NoError(t, err)
			} else {
				certificate, err := x509.ParseCertificate(targetConfig.Certificates[0].Certificate[0])
				require.NoError(t, err)
				roots := x509.NewCertPool()
				roots.AddCert(certificate)
				ordinary := stdtls.Client(conn, &stdtls.Config{ServerName: "example.com", RootCAs: roots, MinVersion: stdtls.VersionTLS13})
				require.NoError(t, ordinary.HandshakeContext(ctx))
				client = ordinary
			}
			defer client.Close()
			payload := bytes.Repeat([]byte{0x5a}, 512*1024)
			start := time.Now()
			_, err = client.Write(payload)
			require.NoError(t, err)
			response := make([]byte, len(payload))
			_, err = io.ReadFull(client, response)
			require.NoError(t, err)
			require.Equal(t, payload, response)
			if direction == "upload" || direction == "download" {
				// One second of initial burst is allowed; 512 KiB at 256 KiB/s
				// must still incur a delay. Leave margin for read-chunk accounting.
				require.GreaterOrEqual(t, time.Since(start), 500*time.Millisecond)
			}
		})
	}
}
