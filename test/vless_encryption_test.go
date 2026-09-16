package main

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"encoding/base64"
	"net/netip"
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"

	"github.com/stretchr/testify/require"
)

const vlessEncryptionTestUser = "b6d4f362-2b3a-42b8-9d98-5e063f87f440"

func TestVLESSEncryptionSelf(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		keyType   string
		mode      string
		clientRTT string
	}{
		{name: "x25519/native/1rtt", keyType: "x25519", mode: "native", clientRTT: "1rtt"},
		{name: "x25519/xorpub/1rtt", keyType: "x25519", mode: "xorpub", clientRTT: "1rtt"},
		{name: "mlkem768/random/0rtt", keyType: "mlkem768", mode: "random", clientRTT: "0rtt"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			encryption, decryption := vlessEncryptionTestPair(t, testCase.keyType, testCase.mode, testCase.clientRTT)
			testVLESSEncryptionSelf(t, encryption, decryption)
		})
	}
}

func testVLESSEncryptionSelf(t *testing.T, encryption, decryption string) {
	t.Helper()
	listenAddress := badoption.Addr(netip.IPv4Unspecified())
	startInstance(t, option.Options{
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
				Type: C.TypeVLESS,
				Options: &option.VLESSInboundOptions{
					ListenOptions: option.ListenOptions{
						Listen:     common.Ptr(listenAddress),
						ListenPort: serverPort,
					},
					Users: []option.VLESSUser{
						{
							UUID: vlessEncryptionTestUser,
						},
					},
					Decryption: decryption,
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
					UUID:       vlessEncryptionTestUser,
					Encryption: encryption,
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
}

func vlessEncryptionTestPair(t *testing.T, keyType, mode, clientRTT string) (string, string) {
	t.Helper()
	var public, private []byte
	switch keyType {
	case "x25519":
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		require.NoError(t, err)
		public, private = key.PublicKey().Bytes(), key.Bytes()
	case "mlkem768":
		key, err := mlkem.GenerateKey768()
		require.NoError(t, err)
		public, private = key.EncapsulationKey().Bytes(), key.Bytes()
	default:
		t.Fatalf("unknown key type %q", keyType)
	}
	prefix := "mlkem768x25519plus." + mode + "."
	return prefix + clientRTT + "." + base64.RawURLEncoding.EncodeToString(public), prefix + "60-60s." + base64.RawURLEncoding.EncodeToString(private)
}
