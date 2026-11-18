package main

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestOwnedDoesNotRunServiceInstaller(t *testing.T) {
	// Owned mode has no privileged platform or service installer. Calling this
	// legacy RPC must return a capability error, not panic on a nil platform.
	d := &Daemon{}
	_, err := d.installUpdate(peerIdentity{}, `C:\installer.exe`)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("InstallUpdate returned %v", err)
	}
}
