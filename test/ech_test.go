package main

import (
	"encoding/binary"
	"encoding/pem"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/fig0ut/rostra/common/tls"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

func TestECH(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	echConfig, echKey := common.Must2(tls.ECHKeygenDefault("not.example.org"))
	startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientPort,
					},
				},
			},
			{
				Type: C.TypeTrojan,
				Options: &option.TrojanInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users: []option.TrojanUser{
						{
							Name:     "sekai",
							Password: "password",
						},
					},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
							ECH: &option.InboundECHOptions{
								Enabled: true,
								Key:     []string{echKey},
							},
						},
					},
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeTrojan,
				Tag:  "trojan-out",
				Options: &option.TrojanOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverPort,
					},
					Password: "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							ECH: &option.OutboundECHOptions{
								Enabled: true,
								Config:  []string{echConfig},
							},
						},
					},
				},
			},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{
							Inbound: []string{"mixed-in"},
						},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,

							RouteOptions: option.RouteActionOptions{
								Outbound: "trojan-out",
							},
						},
					},
				},
			},
		},
	})
	testSuit(t, clientPort, testPort)
}

func TestECHQUIC(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	echConfig, echKey := common.Must2(tls.ECHKeygenDefault("not.example.org"))
	startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientPort,
					},
				},
			},
			{
				Type: C.TypeTUIC,
				Options: &option.TUICInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users: []option.TUICUser{{
						UUID: uuid.Nil.String(),
					}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
							ECH: &option.InboundECHOptions{
								Enabled: true,
								Key:     []string{echKey},
							},
						},
					},
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeTUIC,
				Tag:  "tuic-out",
				Options: &option.TUICOutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverPort,
					},
					UUID: uuid.Nil.String(),
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							ECH: &option.OutboundECHOptions{
								Enabled: true,
								Config:  []string{echConfig},
							},
						},
					},
				},
			},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{
							Inbound: []string{"mixed-in"},
						},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,

							RouteOptions: option.RouteActionOptions{
								Outbound: "tuic-out",
							},
						},
					},
				},
			},
		},
	})
	testSuitLargeUDP(t, clientPort, testPort)
}

func TestECHVLESSXHTTP(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	echConfig, echKey := common.Must2(tls.ECHKeygenDefault("not.example.org"))
	block, rest := pem.Decode([]byte(echConfig))
	require.NotNil(t, block)
	require.Empty(t, rest)

	// RFC 9460 HTTPS RDATA: SvcPriority=1, TargetName=".", SvcParamKey=5 (ech), length, ECHConfigList.
	httpsRData := make([]byte, 2+1+2+2+len(block.Bytes))
	binary.BigEndian.PutUint16(httpsRData[0:2], 1)
	httpsRData[2] = 0
	binary.BigEndian.PutUint16(httpsRData[3:5], 5)
	binary.BigEndian.PutUint16(httpsRData[5:7], uint16(len(block.Bytes)))
	copy(httpsRData[7:], block.Bytes)

	dnsConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	var httpsQueries atomic.Int32
	dnsDone := make(chan struct{})
	go func() {
		defer close(dnsDone)
		buf := make([]byte, 4096)
		for {
			n, addr, readErr := dnsConn.ReadFrom(buf)
			if readErr != nil {
				return
			}
			var query dnsmessage.Message
			if query.Unpack(buf[:n]) != nil {
				continue
			}
			reply := dnsmessage.Message{
				Header: dnsmessage.Header{
					ID:                 query.ID,
					Response:           true,
					RecursionDesired:   query.RecursionDesired,
					RecursionAvailable: true,
				},
				Questions: query.Questions,
			}
			for _, q := range query.Questions {
				if q.Type == dnsmessage.Type(65) && q.Name.String() == "example.org." {
					httpsQueries.Add(1)
					reply.Answers = append(reply.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{
							Name:  q.Name,
							Type:  dnsmessage.Type(65),
							Class: dnsmessage.ClassINET,
							TTL:   300,
						},
						Body: &dnsmessage.UnknownResource{
							Type: dnsmessage.Type(65),
							Data: httpsRData,
						},
					})
				}
			}
			if packed, packErr := reply.Pack(); packErr == nil {
				_, _ = dnsConn.WriteTo(packed, addr)
			}
		}
	}()
	t.Cleanup(func() {
		_ = dnsConn.Close()
		<-dnsDone
	})
	_, dnsPortStr, err := net.SplitHostPort(dnsConn.LocalAddr().String())
	require.NoError(t, err)
	dnsPort, err := strconv.Atoi(dnsPortStr)
	require.NoError(t, err)

	versions := []string{"2"}
	if C.WithQUIC {
		versions = append(versions, "3")
	}
	for _, version := range versions {
		t.Run("h"+version, func(t *testing.T) {
			httpsQueries.Store(0)
			alpn := map[string]string{"2": "h2", "3": "h3"}[version]
			startInstance(t, option.Options{
				DNS: &option.DNSOptions{
					RawDNSOptions: option.RawDNSOptions{
						Servers: []option.DNSServerOptions{
							{
								Type: C.DNSTypeUDP,
								Tag:  "ech-dns",
								Options: &option.RemoteDNSServerOptions{
									DNSServerAddressOptions: option.DNSServerAddressOptions{
										Server:     "127.0.0.1",
										ServerPort: uint16(dnsPort),
									},
								},
							},
						},
						Final: "ech-dns",
					},
				},
				Inbounds: []option.Inbound{
					{
						Type: C.TypeMixed,
						Tag:  "mixed-in",
						Options: &option.HTTPMixedInboundOptions{
							ListenOptions: option.ListenOptions{
								Listen:     common.Ptr(badoption.Addr(netip.MustParseAddr("127.0.0.1"))),
								ListenPort: clientPort,
							},
						},
					},
					{
						Type: C.TypeVLESS,
						Options: &option.VLESSInboundOptions{
							ListenOptions: option.ListenOptions{
								Listen:     common.Ptr(badoption.Addr(netip.MustParseAddr("127.0.0.1"))),
								ListenPort: serverPort,
							},
							Users: []option.VLESSUser{
								{
									UUID: uuid.Nil.String(),
								},
							},
							InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
								TLS: &option.InboundTLSOptions{
									Enabled:         true,
									ServerName:      "example.org",
									CertificatePath: certPem,
									KeyPath:         keyPem,
									ALPN:            []string{alpn},
									ECH: &option.InboundECHOptions{
										Enabled: true,
										Key:     []string{echKey},
									},
								},
							},
							Transport: &option.V2RayTransportOptions{
								Type: C.V2RayTransportTypeXHTTP,
								XHTTPOptions: option.V2RayXHTTPOptions{
									Path:        "/xhttp",
									HTTPVersion: version,
								},
							},
						},
					},
				},
				Outbounds: []option.Outbound{
					{
						Type: C.TypeDirect,
					},
					{
						Type: C.TypeVLESS,
						Tag:  "vless-out",
						Options: &option.VLESSOutboundOptions{
							ServerOptions: option.ServerOptions{
								Server:     "127.0.0.1",
								ServerPort: serverPort,
							},
							UUID: uuid.Nil.String(),
							OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
								TLS: &option.OutboundTLSOptions{
									Enabled:         true,
									ServerName:      "example.org",
									CertificatePath: certPem,
									ALPN:            []string{alpn},
									ECH: &option.OutboundECHOptions{
										Enabled: true,
									},
								},
							},
							Transport: &option.V2RayTransportOptions{
								Type: C.V2RayTransportTypeXHTTP,
								XHTTPOptions: option.V2RayXHTTPOptions{
									Path:        "/xhttp",
									Mode:        "auto",
									HTTPVersion: version,
								},
							},
						},
					},
				},
				Route: &option.RouteOptions{
					Rules: []option.Rule{
						{
							Type: C.RuleTypeDefault,
							DefaultOptions: option.DefaultRule{
								RawDefaultRule: option.RawDefaultRule{
									Inbound: []string{"mixed-in"},
								},
								RuleAction: option.RuleAction{
									Action: C.RuleActionTypeRoute,
									RouteOptions: option.RouteActionOptions{
										Outbound: "vless-out",
									},
								},
							},
						},
					},
				},
			})
			testSuit(t, clientPort, testPort)
			require.GreaterOrEqual(t, httpsQueries.Load(), int32(1))
		})
	}
}

func TestECHHysteria2(t *testing.T) {
	_, certPem, keyPem := createSelfSignedCertificate(t, "example.org")
	echConfig, echKey := common.Must2(tls.ECHKeygenDefault("not.example.org"))
	startInstance(t, option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: clientPort,
					},
				},
			},
			{
				Type: C.TypeHysteria2,
				Options: &option.Hysteria2InboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(badoption.Addr(netip.IPv4Unspecified())),
						ListenPort: serverPort,
					},
					Users: []option.Hysteria2User{{
						Password: "password",
					}},
					InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{
						TLS: &option.InboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							KeyPath:         keyPem,
							ECH: &option.InboundECHOptions{
								Enabled: true,
								Key:     []string{echKey},
							},
						},
					},
				},
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type: C.TypeHysteria2,
				Tag:  "hy2-out",
				Options: &option.Hysteria2OutboundOptions{
					ServerOptions: option.ServerOptions{
						Server:     "127.0.0.1",
						ServerPort: serverPort,
					},
					Password: "password",
					OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
						TLS: &option.OutboundTLSOptions{
							Enabled:         true,
							ServerName:      "example.org",
							CertificatePath: certPem,
							ECH: &option.OutboundECHOptions{
								Enabled: true,
								Config:  []string{echConfig},
							},
						},
					},
				},
			},
		},
		Route: &option.RouteOptions{
			Rules: []option.Rule{
				{
					Type: C.RuleTypeDefault,
					DefaultOptions: option.DefaultRule{
						RawDefaultRule: option.RawDefaultRule{
							Inbound: []string{"mixed-in"},
						},
						RuleAction: option.RuleAction{
							Action: C.RuleActionTypeRoute,

							RouteOptions: option.RouteActionOptions{
								Outbound: "hy2-out",
							},
						},
					},
				},
			},
		},
	})
	testSuitLargeUDP(t, clientPort, testPort)
}
