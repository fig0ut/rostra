package settings

import (
	"context"

	"github.com/fig0ut/rostra/adapter"
)

type WIFIMonitor interface {
	ReadWIFIState(ctx context.Context) adapter.WIFIState
	Start() error
	Close() error
}
