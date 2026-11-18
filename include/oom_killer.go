package include

import (
	"github.com/fig0ut/rostra/adapter/service"
	"github.com/fig0ut/rostra/service/oomkiller"
)

func registerOOMKillerService(registry *service.Registry) {
	oomkiller.RegisterService(registry)
}
