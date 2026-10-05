//go:build with_tailscale && !windows && !linux

package tailssh

import "github.com/fig0ut/rostra/adapter"

func checkSwitchUserPrivilege(_ *adapter.PlatformUser) error {
	return nil
}
