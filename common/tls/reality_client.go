//go:build with_utls

package tls

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing/common/debug"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	aTLS "github.com/sagernet/sing/common/tls"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	utls "github.com/metacubex/utls"
	"golang.org/x/crypto/hkdf"
)

var _ ConfigCompat = (*RealityClientConfig)(nil)

type RealityClientConfig struct {
	ctx           context.Context
	uClient       *UTLSClientConfig
	publicKey     []byte
	shortID       [8]byte
	mldsa65Verify *mldsa65.PublicKey
	fallbackPath  string
}

func NewRealityClient(ctx context.Context, logger logger.ContextLogger, serverAddress string, options option.OutboundTLSOptions) (Config, error) {
	return newRealityClient(ctx, logger, serverAddress, options, false)
}

func newRealityClient(ctx context.Context, logger logger.ContextLogger, serverAddress string, options option.OutboundTLSOptions, allowEmptyServerName bool) (Config, error) {
	if options.UTLS == nil || !options.UTLS.Enabled {
		return nil, E.New("uTLS is required by reality client")
	}
	if options.Spoof != "" || options.SpoofMethod != "" {
		return nil, E.New("spoof is unsupported in reality")
	}

	uClient, err := newUTLSClient(ctx, logger, serverAddress, options, allowEmptyServerName)
	if err != nil {
		return nil, err
	}

	publicKey, err := base64.RawURLEncoding.DecodeString(options.Reality.PublicKey)
	if err != nil {
		return nil, E.Cause(err, "decode public_key")
	}
	if len(publicKey) != 32 {
		return nil, E.New("invalid public_key")
	}
	var shortID [8]byte
	if len(options.Reality.ShortID) > hex.EncodedLen(len(shortID)) {
		return nil, E.New("invalid short_id: maximum length is 16 hex characters")
	}
	_, err = hex.Decode(shortID[:], []byte(options.Reality.ShortID))
	if err != nil {
		return nil, E.Cause(err, "decode short_id")
	}

	var mldsaVerify *mldsa65.PublicKey
	if options.Reality.MLDSA65Verify != "" {
		verify, err := base64.RawURLEncoding.DecodeString(options.Reality.MLDSA65Verify)
		if err != nil || len(verify) != mldsa65.PublicKeySize {
			return nil, E.New("invalid mldsa65_verify: expected 1952 bytes in unpadded base64url")
		}
		key, err := mldsa65.Scheme().UnmarshalBinaryPublicKey(verify)
		if err != nil {
			return nil, E.Cause(err, "parse mldsa65_verify")
		}
		mldsaVerify = key.(*mldsa65.PublicKey)
	}
	fallbackPath, err := parseRealityFallbackPath(options.Reality.FallbackPath)
	if err != nil {
		return nil, err
	}
	var config Config = &RealityClientConfig{
		ctx:           ctx,
		uClient:       uClient.(*UTLSClientConfig),
		publicKey:     publicKey,
		shortID:       shortID,
		mldsa65Verify: mldsaVerify,
		fallbackPath:  fallbackPath,
	}
	if options.KernelRx || options.KernelTx {
		if !C.IsLinux {
			return nil, E.New("kTLS is only supported on Linux")
		}
		config = &KTLSClientConfig{
			Config:   config,
			logger:   logger,
			kernelTx: options.KernelTx,
			kernelRx: options.KernelRx,
		}
	}
	return config, nil
}

func (e *RealityClientConfig) ServerName() string {
	return e.uClient.ServerName()
}

func (e *RealityClientConfig) SetServerName(serverName string) {
	e.uClient.SetServerName(serverName)
}

func (e *RealityClientConfig) NextProtos() []string {
	return e.uClient.NextProtos()
}

func (e *RealityClientConfig) SetNextProtos(nextProto []string) {
	e.uClient.SetNextProtos(nextProto)
}

func (e *RealityClientConfig) HandshakeTimeout() time.Duration {
	return e.uClient.HandshakeTimeout()
}

func (e *RealityClientConfig) SetHandshakeTimeout(timeout time.Duration) {
	e.uClient.SetHandshakeTimeout(timeout)
}

func (e *RealityClientConfig) STDConfig() (*STDConfig, error) {
	return nil, E.New("unsupported usage for reality")
}

func (e *RealityClientConfig) Client(conn net.Conn) (Conn, error) {
	return ClientHandshake(context.Background(), conn, e)
}

func (e *RealityClientConfig) ClientHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
	verifier := &realityVerifier{
		serverName:    e.uClient.ServerName(),
		roots:         e.uClient.config.RootCAs,
		time:          e.uClient.config.Time,
		mldsa65Verify: e.mldsa65Verify,
	}
	uConfig := e.uClient.config.Clone()
	uConfig.InsecureSkipVerify = true
	uConfig.SessionTicketsDisabled = true
	uConfig.VerifyPeerCertificate = verifier.VerifyPeerCertificate
	uConn := utls.UClient(conn, uConfig, e.uClient.id)
	verifier.conn = uConn
	err := uConn.BuildHandshakeState()
	if err != nil {
		return nil, err
	}
	// Preserve the fingerprint's hybrid key share. Current REALITY servers
	// require X25519MLKEM768 even when authentication uses standalone X25519.

	if nextProtos := e.uClient.NextProtos(); len(nextProtos) > 0 {
		for _, extension := range uConn.Extensions {
			if alpnExtension, isALPN := extension.(*utls.ALPNExtension); isALPN {
				alpnExtension.AlpnProtocols = nextProtos
				break
			}
		}
	}

	// Serialize the configured extensions before authenticating the hello.
	if err = uConn.BuildHandshakeState(); err != nil {
		return nil, err
	}
	hello := uConn.HandshakeState.Hello
	hello.SessionId = make([]byte, 32)
	copy(hello.Raw[39:], hello.SessionId)

	var nowTime time.Time
	if uConfig.Time != nil {
		nowTime = uConfig.Time()
	} else {
		nowTime = time.Now()
	}

	hello.SessionId[0] = 1
	hello.SessionId[1] = 8
	hello.SessionId[2] = 1
	binary.BigEndian.PutUint32(hello.SessionId[4:], uint32(nowTime.Unix()))
	copy(hello.SessionId[8:], e.shortID[:])
	if debug.Enabled {
		fmt.Printf("REALITY hello.sessionId[:16]: %v\n", hello.SessionId[:16])
	}
	publicKey, err := ecdh.X25519().NewPublicKey(e.publicKey)
	if err != nil {
		return nil, err
	}
	keyShareKeys := uConn.HandshakeState.State13.KeyShareKeys
	if keyShareKeys == nil {
		return nil, E.New("nil KeyShareKeys")
	}
	ecdheKey := keyShareKeys.Ecdhe
	if ecdheKey == nil {
		ecdheKey = keyShareKeys.MlkemEcdhe
	}
	if ecdheKey == nil {
		return nil, E.New("nil ecdheKey")
	}
	authKey, err := ecdheKey.ECDH(publicKey)
	if err != nil {
		return nil, err
	}
	if authKey == nil {
		return nil, E.New("nil auth_key")
	}
	verifier.authKey = authKey
	_, err = hkdf.New(sha256.New, authKey, hello.Random[:20], []byte("REALITY")).Read(authKey)
	if err != nil {
		return nil, err
	}
	aesBlock, _ := aes.NewCipher(authKey)
	aesGcmCipher, _ := cipher.NewGCM(aesBlock)
	aesGcmCipher.Seal(hello.SessionId[:0], hello.Random[20:], hello.SessionId[:16], hello.Raw)
	copy(hello.Raw[39:], hello.SessionId)
	if debug.Enabled {
		fmt.Printf("REALITY hello.sessionId: %v\n", hello.SessionId)
	}

	err = uConn.HandshakeContext(ctx)
	if err != nil {
		return nil, err
	}

	if debug.Enabled {
		fmt.Printf("REALITY Conn.Verified: %v\n", verifier.verified)
	}

	if !verifier.verified {
		// Finish fallback while we still own the connection; dialers close it on error.
		realityClientFallback(ctx, uConn, e.uClient.ServerName(), e.fallbackPath, e.uClient.id)
		return nil, E.New("reality verification failed")
	}

	return &realityClientConnWrapper{uConn}, nil
}

func (e *RealityClientConfig) Clone() Config {
	return &RealityClientConfig{
		ctx:           e.ctx,
		uClient:       e.uClient.Clone().(*UTLSClientConfig),
		publicKey:     e.publicKey,
		shortID:       e.shortID,
		mldsa65Verify: e.mldsa65Verify,
		fallbackPath:  e.fallbackPath,
	}
}

type realityVerifier struct {
	serverName    string
	roots         *x509.CertPool
	time          func() time.Time
	authKey       []byte
	verified      bool
	mldsa65Verify *mldsa65.PublicKey
	conn          *utls.UConn
}

func (c *realityVerifier) VerifyPeerCertificate(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return E.New("reality: missing peer certificate")
	}
	certs := make([]*x509.Certificate, len(rawCerts))
	for i, raw := range rawCerts {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return err
		}
		certs[i] = cert
	}
	if pub, ok := certs[0].PublicKey.(ed25519.PublicKey); ok {
		h := hmac.New(sha512.New, c.authKey)
		h.Write(pub)
		if hmac.Equal(h.Sum(nil), certs[0].Signature) {
			if c.mldsa65Verify != nil {
				if c.conn == nil || c.conn.HandshakeState.Hello == nil || c.conn.HandshakeState.ServerHello == nil || len(certs[0].Extensions) == 0 {
					return E.New("reality: missing ML-DSA-65 signature or hello transcript")
				}
				h.Write(c.conn.HandshakeState.Hello.Raw)
				h.Write(c.conn.HandshakeState.ServerHello.Raw)
				if !mldsa65.Verify(c.mldsa65Verify, h.Sum(nil), nil, certs[0].Extensions[0].Value) {
					return E.New("reality: invalid ML-DSA-65 signature")
				}
			}
			c.verified = true
			return nil
		}
	}
	opts := x509.VerifyOptions{
		DNSName:       c.serverName,
		Roots:         c.roots,
		Intermediates: x509.NewCertPool(),
	}
	if c.time != nil {
		opts.CurrentTime = c.time()
	}
	for _, cert := range certs[1:] {
		opts.Intermediates.AddCert(cert)
	}
	if _, err := certs[0].Verify(opts); err != nil {
		return err
	}
	return nil
}

type realityClientConnWrapper struct {
	*utls.UConn
}

func (c *realityClientConnWrapper) ConnectionState() tls.ConnectionState {
	state := c.Conn.ConnectionState()
	//nolint:staticcheck
	return tls.ConnectionState{
		Version:                     state.Version,
		HandshakeComplete:           state.HandshakeComplete,
		DidResume:                   state.DidResume,
		CipherSuite:                 state.CipherSuite,
		NegotiatedProtocol:          state.NegotiatedProtocol,
		NegotiatedProtocolIsMutual:  state.NegotiatedProtocolIsMutual,
		ServerName:                  state.ServerName,
		PeerCertificates:            state.PeerCertificates,
		VerifiedChains:              state.VerifiedChains,
		SignedCertificateTimestamps: state.SignedCertificateTimestamps,
		OCSPResponse:                state.OCSPResponse,
		TLSUnique:                   state.TLSUnique,
	}
}

func (c *realityClientConnWrapper) Upstream() any {
	return c.UConn
}

// Due to low implementation quality, the reality server intercepted half close and caused memory leaks.
// We fixed it by calling Close() directly.
func (c *realityClientConnWrapper) CloseWrite() error {
	return c.Close()
}

func (c *realityClientConnWrapper) ReaderReplaceable() bool {
	return true
}

func (c *realityClientConnWrapper) WriterReplaceable() bool {
	return false
}
