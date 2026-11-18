//go:build with_usbip && (linux || (darwin && cgo) || windows)

package usbip

import (
	boxService "github.com/fig0ut/rostra/adapter/service"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing-usbip"
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.USBIPServerServiceOptions](registry, C.TypeUSBIPServer, NewServerService)
	boxService.Register[option.USBIPClientServiceOptions](registry, C.TypeUSBIPClient, NewClientService)
}

func toDeviceMatches(matches []option.USBIPDeviceMatch) []usbip.DeviceMatch {
	if len(matches) == 0 {
		return nil
	}
	deviceMatches := make([]usbip.DeviceMatch, 0, len(matches))
	for _, match := range matches {
		deviceMatches = append(deviceMatches, usbip.DeviceMatch{
			BusID:     match.BusID,
			VendorID:  match.VendorID,
			ProductID: match.ProductID,
			Serial:    match.Serial,
		})
	}
	return deviceMatches
}
