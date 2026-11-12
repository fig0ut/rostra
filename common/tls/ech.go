//go:build go1.24

package tls

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/dns"
	"github.com/fig0ut/rostra/option"

	E "github.com/sagernet/sing/common/exceptions"
	aTLS "github.com/sagernet/sing/common/tls"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"

	mDNS "github.com/miekg/dns"
	"golang.org/x/crypto/cryptobyte"
)

func parseECHClientConfig(ctx context.Context, clientConfig ECHCapableConfig, options option.OutboundTLSOptions) (Config, error) {
	var echConfig []byte
	if len(options.ECH.Config) > 0 {
		echConfig = []byte(strings.Join(options.ECH.Config, "\n"))
	} else if options.ECH.ConfigPath != "" {
		content, err := filemanager.ReadFile(ctx, options.ECH.ConfigPath)
		if err != nil {
			return nil, E.Cause(err, "read ECH config")
		}
		echConfig = content
	}
	//nolint:staticcheck
	if options.ECH.PQSignatureSchemesEnabled || options.ECH.DynamicRecordSizingDisabled {
		return nil, E.New("legacy ECH options are deprecated in sing-box 1.12.0 and removed in sing-box 1.13.0")
	}
	if len(echConfig) > 0 {
		block, rest := pem.Decode(echConfig)
		if block == nil || block.Type != "ECH CONFIGS" || len(rest) > 0 {
			return nil, E.New("invalid ECH configs pem")
		}
		clientConfig.SetECHConfigList(block.Bytes)
		return clientConfig, nil
	} else {
		return &ECHClientConfig{
			ECHCapableConfig: clientConfig,
			dnsRouter:        service.FromContext[adapter.DNSRouter](ctx),
			queryServerName:  options.ECH.QueryServerName,
		}, nil
	}
}

func parseECHServerConfig(ctx context.Context, options option.InboundTLSOptions, tlsConfig *tls.Config, echKeyPath *string) error {
	var echKey []byte
	if len(options.ECH.Key) > 0 {
		echKey = []byte(strings.Join(options.ECH.Key, "\n"))
	} else if options.ECH.KeyPath != "" {
		content, err := filemanager.ReadFile(ctx, options.ECH.KeyPath)
		if err != nil {
			return E.Cause(err, "read ECH keys")
		}
		echKey = content
		*echKeyPath = options.ECH.KeyPath
	} else {
		return E.New("missing ECH keys")
	}
	echKeys, err := parseECHKeys(echKey)
	if err != nil {
		return E.Cause(err, "parse ECH keys")
	}
	tlsConfig.EncryptedClientHelloKeys = echKeys
	//nolint:staticcheck
	if options.ECH.PQSignatureSchemesEnabled || options.ECH.DynamicRecordSizingDisabled {
		return E.New("legacy ECH options are deprecated in sing-box 1.12.0 and removed in sing-box 1.13.0")
	}
	return nil
}

func (c *STDServerConfig) setECHServerConfig(echKey []byte) error {
	echKeys, err := parseECHKeys(echKey)
	if err != nil {
		return err
	}
	c.access.Lock()
	config := c.config.Clone()
	config.EncryptedClientHelloKeys = echKeys
	c.config = config
	c.access.Unlock()
	return nil
}

func parseECHKeys(echKey []byte) ([]tls.EncryptedClientHelloKey, error) {
	block, _ := pem.Decode(echKey)
	if block == nil || block.Type != "ECH KEYS" {
		return nil, E.New("invalid ECH keys pem")
	}
	echKeys, err := UnmarshalECHKeys(block.Bytes)
	if err != nil {
		return nil, E.Cause(err, "parse ECH keys")
	}
	return echKeys, nil
}

type ECHClientConfig struct {
	ECHCapableConfig
	access          sync.Mutex
	dnsRouter       adapter.DNSRouter
	queryServerName string
	lastTTL         time.Duration
	lastUpdate      time.Time
}

func (s *ECHClientConfig) ECHConfigList() []byte {
	s.access.Lock()
	defer s.access.Unlock()
	return s.ECHCapableConfig.ECHConfigList()
}

func (s *ECHClientConfig) SetECHConfigList(echConfigList []byte) {
	s.access.Lock()
	defer s.access.Unlock()
	s.ECHCapableConfig.SetECHConfigList(echConfigList)
	s.lastUpdate = time.Now()
	if s.lastTTL <= 0 {
		s.lastTTL = time.Minute
	}
}

func (s *ECHClientConfig) ClientHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
	tlsConn, err := s.fetchAndHandshake(ctx, conn)
	if err != nil {
		return nil, err
	}
	err = tlsConn.HandshakeContext(ctx)
	if err != nil {
		return nil, err
	}
	return tlsConn, nil
}

func (s *ECHClientConfig) fetchAndHandshake(ctx context.Context, conn net.Conn) (aTLS.Conn, error) {
	s.access.Lock()
	defer s.access.Unlock()
	err := s.fetchECHConfigListLocked(ctx)
	if err != nil {
		return nil, err
	}
	return s.Client(conn)
}

func (s *ECHClientConfig) prepareSTDConfig(ctx context.Context) (*tls.Config, error) {
	s.access.Lock()
	defer s.access.Unlock()
	err := s.fetchECHConfigListLocked(ctx)
	if err != nil {
		return nil, err
	}
	stdConfig, err := s.ECHCapableConfig.STDConfig()
	if err != nil {
		return nil, err
	}
	return stdConfig.Clone(), nil
}

func (s *ECHClientConfig) fetchECHConfigListLocked(ctx context.Context) error {
	if len(s.ECHCapableConfig.ECHConfigList()) > 0 && s.lastTTL > 0 && time.Since(s.lastUpdate) <= s.lastTTL {
		return nil
	}
	if s.dnsRouter == nil {
		return E.New("DNS router is required to fetch ECH config list")
	}
	queryServerName := s.queryServerName
	if queryServerName == "" {
		queryServerName = s.ServerName()
	}
	message := &mDNS.Msg{
		MsgHdr: mDNS.MsgHdr{
			RecursionDesired: true,
		},
		Question: []mDNS.Question{
			{
				Name:   mDNS.Fqdn(queryServerName),
				Qtype:  mDNS.TypeHTTPS,
				Qclass: mDNS.ClassINET,
			},
		},
	}
	response, err := s.dnsRouter.Exchange(ctx, message, adapter.DNSQueryOptions{})
	if err != nil {
		return E.Cause(err, "fetch ECH config list")
	}
	if response.Rcode != mDNS.RcodeSuccess {
		return E.Cause(dns.RcodeError(response.Rcode), "fetch ECH config list")
	}
	var (
		echConfigList []byte
		ttl           uint32
	)
match:
	for _, rr := range response.Answer {
		switch resource := rr.(type) {
		case *mDNS.HTTPS:
			for _, value := range resource.Value {
				if value.Key().String() == "ech" {
					echConfigList, err = base64.StdEncoding.DecodeString(value.String())
					if err != nil {
						return E.Cause(err, "decode ECH config")
					}
					ttl = rr.Header().Ttl
					break match
				}
			}
		}
	}
	if len(echConfigList) == 0 {
		return E.New("no ECH config found in DNS records")
	}
	s.lastTTL = time.Duration(ttl) * time.Second
	s.lastUpdate = time.Now()
	s.ECHCapableConfig.SetECHConfigList(echConfigList)
	return nil
}

func (s *ECHClientConfig) Clone() Config {
	s.access.Lock()
	defer s.access.Unlock()
	return &ECHClientConfig{
		ECHCapableConfig: s.ECHCapableConfig.Clone().(ECHCapableConfig),
		dnsRouter:        s.dnsRouter,
		queryServerName:  s.queryServerName,
		lastTTL:          s.lastTTL,
		lastUpdate:       s.lastUpdate,
	}
}

func UnmarshalECHKeys(raw []byte) ([]tls.EncryptedClientHelloKey, error) {
	var keys []tls.EncryptedClientHelloKey
	rawString := cryptobyte.String(raw)
	for !rawString.Empty() {
		var key tls.EncryptedClientHelloKey
		if !rawString.ReadUint16LengthPrefixed((*cryptobyte.String)(&key.PrivateKey)) {
			return nil, E.New("error parsing private key")
		}
		if !rawString.ReadUint16LengthPrefixed((*cryptobyte.String)(&key.Config)) {
			return nil, E.New("error parsing config")
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, E.New("empty ECH keys")
	}
	return keys, nil
}
