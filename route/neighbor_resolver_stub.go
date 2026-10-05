//go:build !linux && !darwin

package route

import (
	"os"

	"github.com/fig0ut/rostra/adapter"

	"github.com/sagernet/sing/common/logger"
)

func newNeighborResolver(_ logger.ContextLogger, _ []string) (adapter.NeighborResolver, error) {
	return nil, os.ErrInvalid
}
