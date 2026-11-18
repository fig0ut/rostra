//go:build with_utls

package tls

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/fig0ut/rostra/common/dialer"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	utls "github.com/metacubex/utls"
)

var _ ServerConfigCompat = (*RealityServerConfig)(nil)

type RealityServerConfig struct {
	ctx              context.Context
	config           *utls.RealityConfig
	recordCache      *realityRecordCache
	handshakeTimeout time.Duration
}

func NewRealityServer(ctx context.Context, logger log.ContextLogger, options option.InboundTLSOptions) (ServerConfig, error) {
	var tlsConfig utls.RealityConfig

	if options.CertificateProvider != nil {
		return nil, E.New("certificate_provider is unavailable in reality")
	}
	//nolint:staticcheck
	if options.ACME != nil && len(options.ACME.Domain) > 0 {
		return nil, E.New("acme is unavailable in reality")
	}
	tlsConfig.Time = ntp.TimeFuncFromContext(ctx)
	if options.ServerName != "" {
		tlsConfig.ServerName = options.ServerName
	}
	if len(options.ALPN) > 0 {
		tlsConfig.NextProtos = append(tlsConfig.NextProtos, options.ALPN...)
	}
	if options.MinVersion != "" {
		minVersion, err := ParseTLSVersion(options.MinVersion)
		if err != nil {
			return nil, E.Cause(err, "parse min_version")
		}
		tlsConfig.MinVersion = minVersion
	}
	if options.MaxVersion != "" {
		maxVersion, err := ParseTLSVersion(options.MaxVersion)
		if err != nil {
			return nil, E.Cause(err, "parse max_version")
		}
		tlsConfig.MaxVersion = maxVersion
	}
	if options.CipherSuites != nil {
	find:
		for _, cipherSuite := range options.CipherSuites {
			for _, tlsCipherSuite := range tls.CipherSuites() {
				if cipherSuite == tlsCipherSuite.Name {
					tlsConfig.CipherSuites = append(tlsConfig.CipherSuites, tlsCipherSuite.ID)
					continue find
				}
			}
			return nil, E.New("unknown cipher_suite: ", cipherSuite)
		}
	}
	if len(options.CurvePreferences) > 0 {
		return nil, E.New("curve preferences is unavailable in reality")
	}
	if len(options.Certificate) > 0 || options.CertificatePath != "" || len(options.ClientCertificatePublicKeySHA256) > 0 {
		return nil, E.New("certificate is unavailable in reality")
	}
	if len(options.Key) > 0 || options.KeyPath != "" {
		return nil, E.New("key is unavailable in reality")
	}

	tlsConfig.SessionTicketsDisabled = true
	tlsConfig.Log = func(format string, v ...any) {
		if logger != nil {
			logger.Trace(fmt.Sprintf(format, v...))
		}
	}
	tlsConfig.Type = N.NetworkTCP
	tlsConfig.Dest = options.Reality.Handshake.ServerOptions.Build().String()

	tlsConfig.ServerNames = map[string]bool{options.ServerName: true}
	privateKey, err := base64.RawURLEncoding.DecodeString(options.Reality.PrivateKey)
	if err != nil {
		return nil, E.Cause(err, "decode private key")
	}
	if len(privateKey) != 32 {
		return nil, E.New("invalid private key")
	}
	tlsConfig.PrivateKey = privateKey
	if options.Reality.MLDSA65Seed != "" {
		seed, err := base64.RawURLEncoding.DecodeString(options.Reality.MLDSA65Seed)
		if err != nil || len(seed) != mldsa65.SeedSize {
			return nil, E.New("invalid mldsa65_seed: expected 32 bytes in unpadded base64url")
		}
		if bytes.Equal(seed, privateKey) {
			return nil, E.New("mldsa65_seed must differ from private_key")
		}
		_, signingKey := mldsa65.NewKeyFromSeed((*[mldsa65.SeedSize]byte)(seed))
		tlsConfig.Mldsa65Key = signingKey.Bytes()
	}
	tlsConfig.MaxTimeDiff = time.Duration(options.Reality.MaxTimeDifference)
	tlsConfig.LimitFallbackUpload, err = parseRealityFallbackLimit(options.Reality.LimitFallbackUpload)
	if err != nil {
		return nil, E.Cause(err, "limit_fallback_upload")
	}
	tlsConfig.LimitFallbackDownload, err = parseRealityFallbackLimit(options.Reality.LimitFallbackDownload)
	if err != nil {
		return nil, E.Cause(err, "limit_fallback_download")
	}

	tlsConfig.ShortIds = make(map[[8]byte]bool)
	if len(options.Reality.ShortID) == 0 {
		tlsConfig.ShortIds[[8]byte{0}] = true
	} else {
		for i, shortIDString := range options.Reality.ShortID {
			var shortID [8]byte
			if len(shortIDString) > hex.EncodedLen(len(shortID)) {
				return nil, E.New("invalid short_id[", i, "]: maximum length is 16 hex characters")
			}
			_, err := hex.Decode(shortID[:], []byte(shortIDString))
			if err != nil {
				return nil, E.Cause(err, "decode short_id[", i, "]: ", shortIDString)
			}
			tlsConfig.ShortIds[shortID] = true
		}
	}

	handshakeDialer, err := dialer.New(ctx, options.Reality.Handshake.DialerOptions, options.Reality.Handshake.ServerIsDomain())
	if err != nil {
		return nil, err
	}
	tlsConfig.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return handshakeDialer.DialContext(ctx, network, M.ParseSocksaddr(addr))
	}

	if options.ECH != nil && options.ECH.Enabled {
		return nil, E.New("Reality is conflict with ECH")
	}
	var handshakeTimeout time.Duration
	if options.HandshakeTimeout > 0 {
		handshakeTimeout = options.HandshakeTimeout.Build()
	} else {
		handshakeTimeout = C.TCPTimeout
	}
	recordCache := newRealityRecordCache(ctx, &tlsConfig)
	tlsConfig.GetPostHandshakeRecordLengths = recordCache.get
	var config ServerConfig = &RealityServerConfig{
		ctx:              ctx,
		config:           &tlsConfig,
		recordCache:      recordCache,
		handshakeTimeout: handshakeTimeout,
	}
	if options.KernelTx || options.KernelRx {
		if !C.IsLinux {
			recordCache.cancel()
			return nil, E.New("kTLS is only supported on Linux")
		}
		config = &KTlSServerConfig{
			ServerConfig: config,
			logger:       logger,
			kernelTx:     options.KernelTx,
			kernelRx:     options.KernelRx,
		}
	}
	return config, nil
}

func parseRealityFallbackLimit(options *option.RealityLimitFallbackOptions) (utls.RealityLimitFallback, error) {
	if options == nil {
		return utls.RealityLimitFallback{}, nil
	}
	// uTLS converts these values to signed counters and bucket capacities.
	if options.AfterBytes > math.MaxInt64 || options.BytesPerSec > math.MaxInt64 || options.BurstBytesPerSec > math.MaxInt64 {
		return utls.RealityLimitFallback{}, E.New("values must not exceed ", uint64(math.MaxInt64))
	}
	return utls.RealityLimitFallback{
		AfterBytes:       options.AfterBytes,
		BytesPerSec:      options.BytesPerSec,
		BurstBytesPerSec: options.BurstBytesPerSec,
	}, nil
}

func (c *RealityServerConfig) ServerName() string {
	return c.config.ServerName
}

func (c *RealityServerConfig) SetServerName(serverName string) {
	c.config.ServerName = serverName
	c.config.ServerNames = map[string]bool{serverName: true}
}

func (c *RealityServerConfig) NextProtos() []string {
	return c.config.NextProtos
}

func (c *RealityServerConfig) SetNextProtos(nextProto []string) {
	c.config.NextProtos = nextProto
}

func (c *RealityServerConfig) HandshakeTimeout() time.Duration {
	return c.handshakeTimeout
}

func (c *RealityServerConfig) SetHandshakeTimeout(timeout time.Duration) {
	c.handshakeTimeout = timeout
}

func (c *RealityServerConfig) STDConfig() (*tls.Config, error) {
	return nil, E.New("unsupported usage for reality")
}

func (c *RealityServerConfig) Client(conn net.Conn) (Conn, error) {
	return ClientHandshake(context.Background(), conn, c)
}

func (c *RealityServerConfig) Start() error {
	for serverName := range c.config.ServerNames {
		c.recordCache.warm(serverName)
	}
	return nil
}

func (c *RealityServerConfig) Close() error {
	c.recordCache.cancel()
	return nil
}

func (c *RealityServerConfig) Server(conn net.Conn) (Conn, error) {
	return ServerHandshake(context.Background(), conn, c)
}

func (c *RealityServerConfig) ServerHandshake(ctx context.Context, conn net.Conn) (Conn, error) {
	// uTLS uses the context for dialing, but its subsequent reads and copies
	// are blocking. Cover both sockets, including a stalled camouflage target.
	stopClient := realityCloseOnCancel(ctx, conn)
	stopTarget := func() {}
	config := c.config.Clone()
	dial := config.DialContext
	config.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		target, err := dial(ctx, network, address)
		if err == nil {
			stopTarget = realityCloseOnCancel(ctx, target)
		}
		return target, err
	}
	tlsConn, err := utls.RealityServer(ctx, conn, config)
	// Stop and join callbacks before handing a successful connection to callers.
	stopClient()
	stopTarget()
	if ctx.Err() != nil {
		conn.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return &realityConnWrapper{Conn: tlsConn}, nil
}

func realityCloseOnCancel(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(done) })
	return func() {
		if !stop() {
			<-done
		}
	}
}

func (c *RealityServerConfig) Clone() Config {
	config := c.config.Clone()
	recordCache := newRealityRecordCache(c.ctx, config)
	config.GetPostHandshakeRecordLengths = recordCache.get
	return &RealityServerConfig{
		ctx:              c.ctx,
		config:           config,
		recordCache:      recordCache,
		handshakeTimeout: c.handshakeTimeout,
	}
}

var _ Conn = (*realityConnWrapper)(nil)

type realityConnWrapper struct {
	*utls.Conn
}

func (c *realityConnWrapper) ConnectionState() ConnectionState {
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

func (c *realityConnWrapper) Upstream() any {
	return c.Conn
}

// Due to low implementation quality, the reality server intercepted half close and caused memory leaks.
// We fixed it by calling Close() directly.
func (c *realityConnWrapper) CloseWrite() error {
	return c.Close()
}

func (c *realityConnWrapper) ReaderReplaceable() bool {
	return true
}

func (c *realityConnWrapper) WriterReplaceable() bool {
	return false
}
