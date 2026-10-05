//go:build !(darwin || dragonfly || freebsd || netbsd || openbsd)

package listener

import (
	C "github.com/fig0ut/rostra/constant"
)

func UDPSocketBufferSize() int {
	return C.UDPSocketBufferSize
}
