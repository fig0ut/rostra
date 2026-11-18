//go:build with_quic

package v2rayquic

import "github.com/fig0ut/rostra/transport/v2ray"

func init() {
	v2ray.RegisterQUICConstructor(NewServer, NewClient)
}
