//go:build with_ccm && (!darwin || cgo)

package include

import (
	"github.com/fig0ut/rostra/adapter/service"
	"github.com/fig0ut/rostra/service/ccm"
)

func registerCCMService(registry *service.Registry) {
	ccm.RegisterService(registry)
}
