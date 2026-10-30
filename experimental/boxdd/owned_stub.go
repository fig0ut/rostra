//go:build !linux && !windows

package main

import (
	"fmt"
	"net"
)

func newOwnedParent(uint32) (*ownedParent, error) {
	return nil, fmt.Errorf("owned mode is supported on Linux and Windows")
}

func ownedPeerPID(net.Conn) (uint32, error) {
	return 0, fmt.Errorf("owned endpoint authentication is unavailable")
}

func secureOwnedDirectory(string) error {
	return fmt.Errorf("owned endpoint authentication is unavailable")
}
