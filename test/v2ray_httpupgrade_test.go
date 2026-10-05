package main

import (
	"testing"

	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/option"
)

func TestV2RayHTTPUpgrade(t *testing.T) {
	t.Run("self", func(t *testing.T) {
		testV2RayTransportSelf(t, &option.V2RayTransportOptions{
			Type: C.V2RayTransportTypeHTTPUpgrade,
		})
	})
}
