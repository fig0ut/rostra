//go:build with_utls

package tls

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	stdtls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	utls "github.com/metacubex/utls"
	"github.com/stretchr/testify/require"
)

const (
	realityTestPrivateKey = "UuMBgl7MXTPx9inmQp2UC7Jcnwc6XYbwDNebonM-FCc"
	realityTestPublicKey  = "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0"
)

func realityTestMLDSAKeys() (string, string) {
	seed := [mldsa65.SeedSize]byte{1}
	publicKey, _ := mldsa65.NewKeyFromSeed(&seed)
	return base64.RawURLEncoding.EncodeToString(seed[:]), base64.RawURLEncoding.EncodeToString(publicKey.Bytes())
}

func TestRealityMLDSA65Configuration(t *testing.T) {
	for _, seed := range []string{"!", base64.RawURLEncoding.EncodeToString(make([]byte, 31)), base64.RawURLEncoding.EncodeToString(make([]byte, 33)), realityTestPrivateKey} {
		_, err := NewRealityServer(context.Background(), nil, option.InboundTLSOptions{
			Reality: &option.InboundRealityOptions{PrivateKey: realityTestPrivateKey, MLDSA65Seed: seed},
		})
		require.Error(t, err)
	}
	for _, verify := range []string{"!", base64.RawURLEncoding.EncodeToString(make([]byte, mldsa65.PublicKeySize-1)), base64.RawURLEncoding.EncodeToString(make([]byte, mldsa65.PublicKeySize+1))} {
		_, err := NewRealityClient(context.Background(), logger.NOP(), "example.com", option.OutboundTLSOptions{
			UTLS:    &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
			Reality: &option.OutboundRealityOptions{PublicKey: realityTestPublicKey, MLDSA65Verify: verify},
		})
		require.Error(t, err)
	}
	_, verify := realityTestMLDSAKeys()
	config := realityTestClientConfig(t, verify).(*RealityClientConfig)
	clone := config.Clone().(*RealityClientConfig)
	require.Equal(t, config.mldsa65Verify.Bytes(), clone.mldsa65Verify.Bytes())
}

func TestRealityMLDSA65Verifier(t *testing.T) {
	verify, signingKey := mldsa65.NewKeyFromSeed(&[mldsa65.SeedSize]byte{1})
	wrongKey, _ := mldsa65.NewKeyFromSeed(&[mldsa65.SeedSize]byte{2})
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	authKey := bytes.Repeat([]byte{3}, 32)
	clientHello, serverHello := []byte("client hello"), []byte("server hello")
	h := hmac.New(sha512.New, authKey)
	h.Write(pub)
	legacySignature := h.Sum(nil)
	h.Write(clientHello)
	h.Write(serverHello)
	signature := make([]byte, mldsa65.SignatureSize)
	require.NoError(t, mldsa65.SignTo(signingKey, h.Sum(nil), nil, false, signature))
	for _, test := range []struct {
		name        string
		verify      *mldsa65.PublicKey
		signature   []byte
		clientHello []byte
		serverHello []byte
		wantError   bool
	}{
		{"valid", verify, signature, clientHello, serverHello, false},
		{"legacy-client", nil, signature, clientHello, serverHello, false},
		{"legacy-both", nil, nil, clientHello, serverHello, false},
		{"missing-signature", verify, nil, clientHello, serverHello, true},
		{"wrong-key", wrongKey, signature, clientHello, serverHello, true},
		{"tampered-signature", verify, append([]byte{signature[0] ^ 1}, signature[1:]...), clientHello, serverHello, true},
		{"tampered-client-hello", verify, signature, []byte("tampered"), serverHello, true},
		{"tampered-server-hello", verify, signature, clientHello, []byte("tampered"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			template := &x509.Certificate{SerialNumber: big.NewInt(0)}
			if test.signature != nil {
				template.ExtraExtensions = []pkix.Extension{{Id: []int{0, 0}, Value: test.signature}}
			}
			der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
			require.NoError(t, err)
			copy(der[len(der)-sha512.Size:], legacySignature)
			verifier := &realityVerifier{authKey: authKey, mldsa65Verify: test.verify, conn: &utls.UConn{
				HandshakeState: utls.PubClientHandshakeState{
					Hello:       &utls.PubClientHelloMsg{Raw: test.clientHello},
					ServerHello: &utls.PubServerHelloMsg{Raw: test.serverHello},
				},
			}}
			err = verifier.VerifyPeerCertificate([][]byte{der}, nil)
			if test.wantError {
				require.Error(t, err)
				require.False(t, verifier.verified)
			} else {
				require.NoError(t, err)
				require.True(t, verifier.verified)
			}
		})
	}
}

func realityTestListener(t *testing.T, handler func(net.Conn)) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				handler(conn)
			})
		}
	})
	t.Cleanup(func() {
		listener.Close()
		cancel()
		wg.Wait()
	})
	return listener
}

func realityTestTargetTLSConfig(t *testing.T, curve stdtls.CurveID) *stdtls.Config {
	t.Helper()
	certificate := newPinnedTestCertificate(t, "example.com").Certificate
	template := *certificate.Leaf
	// The target flight must accommodate the additional 3309-byte signature.
	template.ExtraExtensions = []pkix.Extension{{Id: []int{1, 2, 3, 4}, Value: make([]byte, 4096)}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, certificate.Leaf.PublicKey, certificate.PrivateKey)
	require.NoError(t, err)
	certificate.Certificate = [][]byte{der}
	return &stdtls.Config{
		Certificates:     []stdtls.Certificate{certificate},
		MinVersion:       stdtls.VersionTLS13,
		CurvePreferences: []stdtls.CurveID{curve},
		NextProtos:       []string{"h2", "http/1.1"},
	}
}

func realityTestTarget(t *testing.T, curve stdtls.CurveID) net.Listener {
	t.Helper()
	config := realityTestTargetTLSConfig(t, curve)
	return realityTestListener(t, func(conn net.Conn) {
		tlsConn := stdtls.Server(conn, config)
		defer tlsConn.Close()
		tlsConn.Handshake()
	})
}

func realityTestServerConfig(t *testing.T, target net.Listener, seed string) ServerConfig {
	t.Helper()
	return realityTestServerConfigContext(t, context.Background(), target, seed)
}

func realityTestServerConfigContext(t *testing.T, ctx context.Context, target net.Listener, seed string) ServerConfig {
	t.Helper()
	address := M.ParseSocksaddr(target.Addr().String())
	config, err := NewRealityServer(ctx, nil, option.InboundTLSOptions{
		ServerName: "example.com",
		Reality: &option.InboundRealityOptions{
			PrivateKey:  realityTestPrivateKey,
			MLDSA65Seed: seed,
			ShortID:     []string{"0123456789abcdef"},
			Handshake:   option.InboundRealityHandshakeOptions{ServerOptions: option.ServerOptions{Server: address.AddrString(), ServerPort: address.Port}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { config.Close() })
	return config
}

func realityTestClientConfig(t *testing.T, verify string) Config {
	t.Helper()
	config, err := NewRealityClient(context.Background(), logger.NOP(), "example.com", option.OutboundTLSOptions{
		ServerName: "example.com",
		UTLS:       &option.OutboundUTLSOptions{Enabled: true, Fingerprint: "chrome"},
		Reality:    &option.OutboundRealityOptions{PublicKey: realityTestPublicKey, ShortID: "0123456789abcdef", MLDSA65Verify: verify},
	})
	require.NoError(t, err)
	return config
}

func TestRealityMLDSA65Handshake(t *testing.T) {
	seed, verify := realityTestMLDSAKeys()
	for _, curve := range []stdtls.CurveID{stdtls.X25519, stdtls.X25519MLKEM768} {
		t.Run(curve.String(), func(t *testing.T) {
			target := realityTestTarget(t, curve)
			for _, test := range []struct {
				name, seed, verify string
				wantError          bool
			}{
				{"enabled", seed, verify, false},
				{"legacy-client", seed, "", false},
				{"legacy-both", "", "", false},
				{"missing-signature", "", verify, true},
			} {
				t.Run(test.name, func(t *testing.T) {
					server := realityTestServerConfig(t, target, test.seed)
					listener := realityTestListener(t, func(conn net.Conn) {
						tlsConn, err := ServerHandshake(context.Background(), conn, server)
						if err == nil {
							defer tlsConn.Close()
							io.Copy(tlsConn, tlsConn)
						}
					})
					conn, err := net.Dial("tcp", listener.Addr().String())
					require.NoError(t, err)
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(5 * time.Second))
					tlsConn, err := ClientHandshake(context.Background(), conn, realityTestClientConfig(t, test.verify))
					if test.wantError {
						require.Error(t, err)
						return
					}
					require.NoError(t, err)
					defer tlsConn.Close()
					realityConn, ok := common.Cast[*realityClientConnWrapper](tlsConn)
					require.True(t, ok, "TLS connection must wrap a REALITY client")
					group := realityConn.HandshakeState.ServerHello.ServerShare.Group
					require.Equal(t, uint16(curve), uint16(group))
					_, err = tlsConn.Write([]byte("hello"))
					require.NoError(t, err)
					response := make([]byte, 5)
					_, err = io.ReadFull(tlsConn, response)
					require.NoError(t, err)
					require.Equal(t, "hello", string(response))
				})
			}
		})
	}
}
