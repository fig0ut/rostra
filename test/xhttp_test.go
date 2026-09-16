package main

import (
	"net/netip"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badjson"
	"github.com/sagernet/sing/common/json/badoption"
)

const xhttpTestUser = "b6d4f362-2b3a-42b8-9d98-5e063f87f440"

func TestXHTTPDownloadDomain(t *testing.T) {
	for _, protocol := range []string{C.TypeVLESS, C.TypeVMess, C.TypeTrojan} {
		for _, resolver := range []string{"explicit", "default"} {
			t.Run(protocol+"/"+resolver, func(t *testing.T) {
				server, client := xhttpTestTransports("packet-up", "")
				client.XHTTPOptions.DownloadSettings = &option.V2RayXHTTPDownloadOptions{
					ServerOptions: option.ServerOptions{Server: "download.test", ServerPort: serverPort},
					Transport:     &server,
				}
				o := xhttpTestOptions(t, server, client)
				hosts := new(badjson.TypedMap[string, badoption.Listable[netip.Addr]])
				hosts.Put("download.test", []netip.Addr{netip.MustParseAddr("127.0.0.1")})
				o.DNS = &option.DNSOptions{RawDNSOptions: option.RawDNSOptions{
					Servers: []option.DNSServerOptions{
						{Type: C.DNSTypeHosts, Tag: "empty", Options: &option.HostsDNSServerOptions{}},
						{Type: C.DNSTypeHosts, Tag: "download", Options: &option.HostsDNSServerOptions{Predefined: hosts}},
					},
					Final: "empty", // Only the selected resolver knows download.test.
				}}
				inbound := o.Inbounds[1].Options.(*option.VLESSInboundOptions)
				outbound := o.Outbounds[1].Options.(*option.VLESSOutboundOptions)
				if resolver == "explicit" {
					outbound.DomainResolver = &option.DomainResolveOptions{Server: "download"}
				} else {
					o.Route.DefaultDomainResolver = &option.DomainResolveOptions{Server: "download"}
				}
				switch protocol {
				case C.TypeVMess:
					o.Inbounds[1].Options = &option.VMessInboundOptions{
						ListenOptions: inbound.ListenOptions, Transport: inbound.Transport,
						Users: []option.VMessUser{{UUID: xhttpTestUser}},
					}
					o.Outbounds[1].Options = &option.VMessOutboundOptions{
						ServerOptions: outbound.ServerOptions, DialerOptions: outbound.DialerOptions,
						Transport: outbound.Transport, UUID: xhttpTestUser, Security: "auto",
					}
				case C.TypeTrojan:
					o.Inbounds[1].Options = &option.TrojanInboundOptions{
						ListenOptions: inbound.ListenOptions, Transport: inbound.Transport,
						Users: []option.TrojanUser{{Password: "xhttp-test"}},
					}
					o.Outbounds[1].Options = &option.TrojanOutboundOptions{
						ServerOptions: outbound.ServerOptions, DialerOptions: outbound.DialerOptions,
						Transport: outbound.Transport, Password: "xhttp-test",
					}
				}
				o.Inbounds[1].Type, o.Outbounds[1].Type = protocol, protocol
				startInstance(t, o)
				testTCP(t, clientPort, testPort)
			})
		}
	}
}

func TestXHTTPSelf(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		mode    string
		version string
		mutate  func(*option.V2RayXHTTPOptions)
	}{
		{name: "packet-up/plain", mode: "packet-up"},
		{name: "stream-up/h2", mode: "stream-up", version: "2"},
		{name: "stream-one/h2", mode: "stream-one", version: "2"},
		{
			name: "metadata-padding/h2", mode: "packet-up", version: "2",
			mutate: func(options *option.V2RayXHTTPOptions) {
				options.XPaddingObfsMode = true
				options.XPaddingMethod = "tokenish"
				options.XPaddingPlacement = "cookie"
				options.XPaddingKey = "pad"
				options.XPaddingBytes = &option.XHTTPRange{From: 127, To: 127}
				options.SessionIDPlacement = "query"
				options.SessionIDKey = "sid"
				options.SessionIDTable = "Base62"
				options.SessionIDLength = &option.XHTTPRange{From: 16, To: 24}
				options.SeqPlacement = "header"
				options.SeqKey = "X-Part"
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			serverTransport, clientTransport := xhttpTestTransports(testCase.mode, testCase.version)
			if testCase.mutate != nil {
				testCase.mutate(&serverTransport.XHTTPOptions)
				testCase.mutate(&clientTransport.XHTTPOptions)
			}
			testXHTTPSelf(t, serverTransport, clientTransport)
		})
	}
	if C.WithQUIC {
		t.Run("stream-one/h3", func(t *testing.T) {
			serverTransport, clientTransport := xhttpTestTransports("stream-one", "3")
			testXHTTPSelf(t, serverTransport, clientTransport)
		})
	}
}

func testXHTTPSelf(t *testing.T, serverTransport, clientTransport option.V2RayTransportOptions) {
	t.Helper()
	startInstance(t, xhttpTestOptions(t, serverTransport, clientTransport))
	testSuit(t, clientPort, testPort)
}

func xhttpTestOptions(t *testing.T, serverTransport, clientTransport option.V2RayTransportOptions) option.Options {
	t.Helper()
	_, certificate, key := createSelfSignedCertificate(t, "localhost")
	listenAddress := badoption.Addr(netip.MustParseAddr("127.0.0.1"))
	inboundOptions := &option.VLESSInboundOptions{
		ListenOptions: option.ListenOptions{
			Listen:     common.Ptr(listenAddress),
			ListenPort: serverPort,
		},
		Users: []option.VLESSUser{
			{
				UUID: xhttpTestUser,
			},
		},
		Transport: &serverTransport,
	}
	outboundOptions := &option.VLESSOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     "127.0.0.1",
			ServerPort: serverPort,
		},
		UUID:      xhttpTestUser,
		Transport: &clientTransport,
	}
	if serverTransport.XHTTPOptions.HTTPVersion != "" {
		alpn := map[string]string{"1.1": "http/1.1", "2": "h2", "3": "h3"}[serverTransport.XHTTPOptions.HTTPVersion]
		inboundOptions.TLS = &option.InboundTLSOptions{
			Enabled:         true,
			CertificatePath: certificate,
			KeyPath:         key,
			ALPN:            []string{alpn},
		}
		outboundOptions.TLS = &option.OutboundTLSOptions{
			Enabled:         true,
			ServerName:      "localhost",
			CertificatePath: certificate,
			ALPN:            []string{alpn},
		}
	}
	return option.Options{
		Inbounds: []option.Inbound{
			{
				Type: C.TypeMixed,
				Tag:  "mixed-in",
				Options: &option.HTTPMixedInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(listenAddress),
						ListenPort: clientPort,
					},
				},
			},
			{
				Type:    C.TypeVLESS,
				Options: inboundOptions,
			},
		},
		Outbounds: []option.Outbound{
			{
				Type: C.TypeDirect,
			},
			{
				Type:    C.TypeVLESS,
				Tag:     "vless-out",
				Options: outboundOptions,
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
	}
}

func xhttpTestTransports(mode, version string) (option.V2RayTransportOptions, option.V2RayTransportOptions) {
	server := option.V2RayTransportOptions{
		Type: C.V2RayTransportTypeXHTTP,
		XHTTPOptions: option.V2RayXHTTPOptions{
			Path:        "/xhttp",
			Mode:        "auto",
			HTTPVersion: version,
		},
	}
	client := server
	client.XHTTPOptions.Mode = mode
	return server, client
}

// Exercise our acknowledgement-based uploader at 1 ms, including HTTP/1.1.
func TestXHTTPEncodingSelf(t *testing.T) {
	for _, version := range []string{"", "2", "3"} {
		if version == "3" && !C.WithQUIC {
			continue
		}
		for _, placement := range []string{"header", "cookie"} {
			t.Run("h"+version+"/"+placement, func(t *testing.T) {
				server, client := xhttpTestTransports("packet-up", version)
				for _, transport := range []*option.V2RayTransportOptions{&server, &client} {
					o := &transport.XHTTPOptions
					o.UplinkDataKey = "payload"
					o.ScMaxEachPostBytes = &option.XHTTPRange{From: 32768, To: 32768}
					o.ScMinPostsIntervalMs = &option.XHTTPRange{From: 1, To: 1}
				}
				client.XHTTPOptions.UplinkHTTPMethod = "GET"
				client.XHTTPOptions.UplinkDataPlacement = placement
				client.XHTTPOptions.UplinkChunkSize = &option.XHTTPRange{From: 127, To: 255}
				startInstance(t, xhttpTestOptions(t, server, client))
				testTCP(t, clientPort, testPort)
			})
		}
	}
}
