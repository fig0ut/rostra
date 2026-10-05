//go:build with_usbip && (linux || (darwin && cgo) || windows)

package include

import (
	"github.com/fig0ut/rostra/adapter/service"
	"github.com/fig0ut/rostra/service/usbip"
)

func registerUSBIPServices(registry *service.Registry) {
	usbip.RegisterService(registry)
}
