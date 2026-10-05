//go:build with_ocm

package include

import (
	"github.com/fig0ut/rostra/adapter/service"
	"github.com/fig0ut/rostra/service/ocm"
)

func registerOCMService(registry *service.Registry) {
	ocm.RegisterService(registry)
}
