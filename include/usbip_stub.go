//go:build !with_usbip || !(linux || (darwin && cgo) || windows)

package include

import (
	"context"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/adapter/service"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	E "github.com/sagernet/sing/common/exceptions"
)

func registerUSBIPServices(registry *service.Registry) {
	service.Register[option.USBIPServerServiceOptions](registry, C.TypeUSBIPServer, func(ctx context.Context, logger log.ContextLogger, tag string, options option.USBIPServerServiceOptions) (adapter.Service, error) {
		return nil, E.New(`USB/IP is not included in this build, rebuild with -tags with_usbip (supported on Linux, Windows, and macOS with CGO)`)
	})
	service.Register[option.USBIPClientServiceOptions](registry, C.TypeUSBIPClient, func(ctx context.Context, logger log.ContextLogger, tag string, options option.USBIPClientServiceOptions) (adapter.Service, error) {
		return nil, E.New(`USB/IP is not included in this build, rebuild with -tags with_usbip (supported on Linux, Windows, and macOS with CGO)`)
	})
}
