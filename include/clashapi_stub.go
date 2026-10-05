//go:build !with_clash_api

package include

import (
	"context"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/experimental"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	E "github.com/sagernet/sing/common/exceptions"
)

func init() {
	experimental.RegisterClashServerConstructor(func(ctx context.Context, logFactory log.ObservableFactory, options option.ClashAPIOptions) (adapter.LifecycleService, error) {
		return nil, E.New(`clash api is not included in this build, rebuild with -tags with_clash_api`)
	})
}
