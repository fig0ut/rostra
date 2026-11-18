//go:build with_naive_outbound

package include

import (
	"github.com/fig0ut/rostra/adapter/outbound"
	"github.com/fig0ut/rostra/protocol/naive"
)

func registerNaiveOutbound(registry *outbound.Registry) {
	naive.RegisterOutbound(registry)
}
