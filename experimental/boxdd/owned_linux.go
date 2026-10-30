//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

func newOwnedParent(pid uint32) (*ownedParent, error) {
	if pid == 0 || uint32(os.Getppid()) != pid {
		return nil, fmt.Errorf("owned process was not started by its declared parent")
	}
	start, err := linuxProcessStartTime(pid)
	if err != nil {
		return nil, err
	}
	p := &ownedParent{
		pid: pid,
		identity: peerIdentity{
			UserID:           strconv.Itoa(os.Getuid()),
			ProcessID:        pid,
			ProcessStartTime: start,
		},
		done: make(chan struct{}),
	}
	var once sync.Once
	p.stop = func() {
		once.Do(func() {
			close(p.done)
		})
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-ticker.C:
				if uint32(os.Getppid()) != pid {
					p.stop()
					return
				}
			}
		}
	}()
	return p, nil
}

func ownedPeerPID(conn net.Conn) (uint32, error) {
	identity, err := linuxPeerIdentity(conn)
	return identity.ProcessID, err
}

func secureOwnedDirectory(path string) error {
	return os.Chmod(path, 0o700)
}
