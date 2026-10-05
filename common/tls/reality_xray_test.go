//go:build with_utls

package tls

import (
	"context"
	stdtls "crypto/tls"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const realityXrayTestUUID = "00000000-0000-0000-0000-000000000001"

func realityXrayPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func startRealityXray(t *testing.T, binary string, port int, config any) {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(config)
	require.NoError(t, err)
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	logPath := filepath.Join(dir, "xray.log")
	logFile, err := os.Create(logPath)
	require.NoError(t, err)
	command := exec.Command(binary, "run", "-config", path)
	command.Stdout, command.Stderr = logFile, logFile
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		command.Process.Kill()
		command.Wait()
		logFile.Close()
		if t.Failed() {
			logs, _ := os.ReadFile(logPath)
			t.Log(string(logs))
		}
	})
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 20*time.Millisecond)
}

func TestRealityMLDSA65Xray(t *testing.T) {
	xrayBinary := os.Getenv("XRAY_BINARY")
	if xrayBinary == "" {
		t.Skip("set XRAY_BINARY to test against a local Xray-core build")
	}
	seed, verify := realityTestMLDSAKeys()
	for _, curve := range []stdtls.CurveID{stdtls.X25519, stdtls.X25519MLKEM768} {
		t.Run(curve.String(), func(t *testing.T) {
			target := realityTestTarget(t, curve)
			t.Run("rostra-to-xray", func(t *testing.T) {
				echo := realityTestListener(t, func(conn net.Conn) { io.Copy(conn, conn) })
				port := realityXrayPort(t)
				startRealityXray(t, xrayBinary, port, map[string]any{
					"log": map[string]any{"loglevel": "debug"},
					"inbounds": []any{map[string]any{
						"listen": "127.0.0.1", "port": port, "protocol": "vless",
						"settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": realityXrayTestUUID}}},
						"streamSettings": map[string]any{
							"network": "tcp", "security": "reality",
							"realitySettings": map[string]any{"target": target.Addr().String(), "serverNames": []string{"example.com"}, "privateKey": realityTestPrivateKey, "shortIds": []string{"0123456789abcdef"}, "mldsa65Seed": seed},
						},
					}},
					"outbounds": []any{map[string]any{
						"protocol": "freedom",
						// Current Xray blocks loopback destinations unless explicitly allowed.
						"settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "ip": []string{"127.0.0.0/8"}}}},
					}},
				})
				conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				require.NoError(t, err)
				defer conn.Close()
				// Xray can wait a five-second polling interval for its target probe cache.
				conn.SetDeadline(time.Now().Add(15 * time.Second))
				tlsConn, err := ClientHandshake(context.Background(), conn, realityTestClientConfig(t, verify))
				require.NoError(t, err)
				defer tlsConn.Close()
				// VLESS TCP request with a fixed test UUID and an IPv4 destination.
				request := make([]byte, 26)
				request[16], request[18], request[21] = 1, 1, 1
				binaryPort := uint16(echo.Addr().(*net.TCPAddr).Port)
				binary.BigEndian.PutUint16(request[19:21], binaryPort)
				copy(request[22:], net.IPv4(127, 0, 0, 1).To4())
				_, err = tlsConn.Write(append(request, []byte("hello")...))
				require.NoError(t, err)
				response := make([]byte, 7)
				_, err = io.ReadFull(tlsConn, response)
				require.NoError(t, err)
				require.Equal(t, append([]byte{0, 0}, []byte("hello")...), response)
			})
			t.Run("xray-to-rostra", func(t *testing.T) {
				ctx, recordTarget := realityRecordTarget(t, curve)
				server := realityTestServerConfigContext(t, ctx, recordTarget, seed)
				listener := realityTestListener(t, func(conn net.Conn) {
					tlsConn, err := ServerHandshake(context.Background(), conn, server)
					if err != nil {
						return
					}
					defer tlsConn.Close()
					// Consume Xray's VLESS request, then echo through the REALITY tunnel.
					header := make([]byte, 18)
					if _, err = io.ReadFull(tlsConn, header); err != nil {
						return
					}
					if _, err = io.CopyN(io.Discard, tlsConn, int64(header[17])); err != nil {
						return
					}
					destination := make([]byte, 8)
					if _, err = io.ReadFull(tlsConn, destination); err != nil {
						return
					}
					if destination[0] != 1 || destination[3] != 1 {
						return
					}
					if _, err = tlsConn.Write([]byte{0, 0}); err != nil {
						return
					}
					io.Copy(tlsConn, tlsConn)
				})
				port := realityXrayPort(t)
				startRealityXray(t, xrayBinary, port, map[string]any{
					"log":      map[string]any{"loglevel": "debug"},
					"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}},
					"outbounds": []any{map[string]any{
						"protocol":       "vless",
						"settings":       map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": listener.Addr().(*net.TCPAddr).Port, "users": []any{map[string]any{"id": realityXrayTestUUID, "encryption": "none"}}}}},
						"streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": "example.com", "fingerprint": "chrome", "publicKey": realityTestPublicKey, "shortId": "0123456789abcdef", "mldsa65Verify": verify}},
					}},
				})
				conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				require.NoError(t, err)
				defer conn.Close()
				// Xray can wait a five-second polling interval for its target probe cache.
				conn.SetDeadline(time.Now().Add(15 * time.Second))
				_, err = conn.Write([]byte{5, 1, 0})
				require.NoError(t, err)
				greeting := make([]byte, 2)
				_, err = io.ReadFull(conn, greeting)
				require.NoError(t, err)
				require.Equal(t, []byte{5, 0}, greeting)
				_, err = conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80})
				require.NoError(t, err)
				response := make([]byte, 10)
				_, err = io.ReadFull(conn, response)
				require.NoError(t, err)
				require.Equal(t, []byte{5, 0, 0, 1}, response[:4])
				_, err = conn.Write([]byte("hello"))
				require.NoError(t, err)
				payload := make([]byte, 5)
				_, err = io.ReadFull(conn, payload)
				require.NoError(t, err)
				require.Equal(t, "hello", string(payload))
				require.Equal(t, []int{53, 117}, server.(*RealityServerConfig).recordCache.get(ctx, "example.com", []string{"h2", "http/1.1"}))
			})
		})
	}
}
