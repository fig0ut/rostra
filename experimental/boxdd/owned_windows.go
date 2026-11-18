//go:build windows

package main

import (
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func newOwnedParent(pid uint32) (*ownedParent, error) {
	if pid == 0 || uint32(os.Getppid()) != pid {
		return nil, fmt.Errorf("owned process was not started by its declared parent")
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	var parentToken windows.Token
	err = windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &parentToken)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	defer parentToken.Close()
	parentUser, err := parentToken.GetTokenUser()
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	var session uint32
	if err = windows.ProcessIdToSessionId(pid, &session); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	identity := peerIdentity{UserID: parentUser.User.Sid.String(), ProcessID: pid, SessionID: session}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user.User.Sid.String() != identity.UserID {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("owning parent belongs to another user")
	}
	if parentToken.IsElevated() != windows.GetCurrentProcessToken().IsElevated() {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("owning parent and child have different elevation levels")
	}
	p := &ownedParent{pid: pid, identity: identity, done: make(chan struct{})}
	stop := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	p.stop = func() { once.Do(func() { close(stop) }); <-finished }
	go func() {
		defer close(finished)
		defer close(p.done)
		defer windows.CloseHandle(handle)
		for {
			select {
			case <-stop:
				return
			default:
			}
			result, waitErr := windows.WaitForSingleObject(handle, 100)
			if waitErr != nil || result != uint32(windows.WAIT_TIMEOUT) {
				return
			}
		}
	}()
	return p, nil
}

func ownedPeerPID(conn net.Conn) (uint32, error) {
	c, ok := conn.(syscall.Conn)
	if !ok {
		return 0, fmt.Errorf("owned endpoint is not a socket")
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	// SIO_AF_UNIX_GETPEERPID returns ULONG, which is 32 bits on Windows.
	const sioAFUnixGetPeerPID = windows.IOC_OUT | windows.IOC_VENDOR | 256
	var pid uint32
	var size uint32
	var ioctlErr error
	err = raw.Control(func(fd uintptr) {
		ioctlErr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0, (*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &size, nil, 0)
	})
	if err != nil {
		return 0, err
	}
	if ioctlErr != nil {
		return 0, ioctlErr
	}
	// Some Windows versions return a valid PID but report zero bytes written:
	// https://github.com/microsoft/WSL/issues/4676
	// Rely on a successful call and a nonzero PID, not the reported byte count.
	if pid == 0 {
		return 0, fmt.Errorf("invalid Unix socket peer process")
	}
	return pid, nil
}

func secureOwnedDirectory(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("owned directory must not be a reparse point")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
