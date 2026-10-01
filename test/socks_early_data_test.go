package main

import (
	"bytes"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/varbin"
	"github.com/sagernet/sing/protocol/socks/socks4"
	"github.com/sagernet/sing/protocol/socks/socks5"

	"github.com/stretchr/testify/require"
)

func TestSOCKSEarlyData(t *testing.T) {
	for _, inboundType := range []string{C.TypeSOCKS, C.TypeMixed} {
		for _, version := range []string{"4", "5", "5-auth"} {
			t.Run(inboundType+"/"+version, func(t *testing.T) {
				listen := option.ListenOptions{Listen: common.Ptr(badoption.Addr(netip.MustParseAddr("127.0.0.1"))), ListenPort: clientPort}
				var users []auth.User
				if version == "5-auth" {
					users = []auth.User{{Username: "user", Password: "password"}}
				}
				inbound := option.Inbound{Type: inboundType}
				if inboundType == C.TypeSOCKS {
					inbound.Options = &option.SocksInboundOptions{ListenOptions: listen, Users: users}
				} else {
					inbound.Options = &option.HTTPMixedInboundOptions{ListenOptions: listen, Users: users}
				}
				startInstance(t, option.Options{Inbounds: []option.Inbound{inbound}, Outbounds: []option.Outbound{{Type: C.TypeDirect}}})
				echo, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				defer echo.Close()
				echoDone := make(chan struct{})
				go func() {
					defer close(echoDone)
					conn, err := echo.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					_, _ = io.Copy(conn, conn)
				}()
				t.Cleanup(func() { echo.Close(); <-echoDone })
				conn, err := net.Dial("tcp", M.ParseSocksaddrHostPort("127.0.0.1", clientPort).String())
				require.NoError(t, err)
				defer conn.Close()
				require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
				destination := M.ParseSocksaddr(echo.Addr().String())
				var request bytes.Buffer
				if version == "4" {
					require.NoError(t, socks4.WriteRequest(&request, socks4.Request{Command: socks4.CommandConnect, Destination: destination}))
				} else {
					method := byte(socks5.AuthTypeNotRequired)
					if version == "5-auth" {
						method = socks5.AuthTypeUsernamePassword
					}
					_, err = conn.Write([]byte{5, 1, method})
					require.NoError(t, err)
					reply := make([]byte, 2)
					_, err = io.ReadFull(conn, reply)
					require.NoError(t, err)
					require.Equal(t, []byte{5, method}, reply)
					if version == "5-auth" {
						_, err = conn.Write([]byte{1, 4, 'u', 's', 'e', 'r', 8, 'p', 'a', 's', 's', 'w', 'o', 'r', 'd'})
						require.NoError(t, err)
						_, err = io.ReadFull(conn, reply)
						require.NoError(t, err)
						require.Equal(t, []byte{1, 0}, reply)
					}
					require.NoError(t, socks5.WriteRequest(&request, socks5.Request{Command: socks5.CommandConnect, Destination: destination}))
				}
				payload := bytes.Repeat([]byte("early application data\x00"), 512)
				request.Write(payload)
				// One write puts CONNECT and application bytes into the handshake
				// reader together, including bytes beyond its 4 KiB buffer.
				_, err = conn.Write(request.Bytes())
				require.NoError(t, err)
				if version == "4" {
					response, err := socks4.ReadResponse(varbin.StubReader(conn))
					require.NoError(t, err)
					require.EqualValues(t, socks4.ReplyCodeGranted, response.ReplyCode)
				} else {
					response, err := socks5.ReadResponse(varbin.StubReader(conn))
					require.NoError(t, err)
					require.EqualValues(t, socks5.ReplyCodeSuccess, response.ReplyCode)
				}
				content := make([]byte, len(payload))
				_, err = io.ReadFull(conn, content)
				require.NoError(t, err)
				require.Equal(t, payload, content)
				// Later bytes must follow the preserved early bytes exactly once.
				_, err = conn.Write([]byte("later"))
				require.NoError(t, err)
				content = make([]byte, 5)
				_, err = io.ReadFull(conn, content)
				require.NoError(t, err)
				require.Equal(t, "later", string(content))
			})
		}
	}
}
