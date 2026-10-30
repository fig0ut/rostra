//go:build linux || windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnedRejectsUnrelatedParent(t *testing.T) {
	if parent, err := newOwnedParent(0); err == nil {
		parent.Close()
		t.Fatal("accepted a missing parent")
	}
	if parent, err := newOwnedParent(uint32(os.Getpid())); err == nil {
		parent.Close()
		t.Fatal("accepted a process other than the parent")
	}
}

func TestOwnedPeerPID(t *testing.T) {
	dir, err := os.MkdirTemp("", "rd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control.sock")
	// Use a raw listener so lookup errors are reported before authentication
	// turns them into a closed connection.
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for name, conn := range map[string]net.Conn{"client": client, "server": server} {
		t.Run(name, func(t *testing.T) {
			pid, err := ownedPeerPID(conn)
			if err != nil {
				t.Fatalf("lookup peer PID: %v", err)
			}
			if want := uint32(os.Getpid()); pid != want {
				t.Fatalf("peer PID = %d, want %d", pid, want)
			}
		})
	}
}

func TestOwnedSocketAuthenticatesPeer(t *testing.T) {
	dir, err := os.MkdirTemp("", "rd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	done := make(chan struct{})
	defer close(done)
	path := filepath.Join(dir, "control.sock")
	parent := &ownedParent{
		pid:  uint32(os.Getpid()),
		done: done,
	}
	l, err := listenOwnedEndpoint(path, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			_, err = c.Write([]byte("ok"))
			c.Close()
		}
		accepted <- err
	}()
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2)
	if n, err := c.Read(buf); err != nil || n != 2 || string(buf) != "ok" {
		t.Fatalf("authentication failed: %d %q %v", n, buf, err)
	}
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
	if duplicate, err := listenOwnedEndpoint(path, parent); err == nil {
		duplicate.Close()
		t.Fatal("replaced an existing socket")
	}
}

func TestOwnedSocketRejectsOtherPeer(t *testing.T) {
	dir, err := os.MkdirTemp("", "rd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	done := make(chan struct{})
	defer close(done)
	path := filepath.Join(dir, "control.sock")
	l, err := listenOwnedEndpoint(path, &ownedParent{pid: uint32(os.Getppid()), done: done})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		c, err := l.Accept()
		if err == nil {
			c.Close()
			t.Error("accepted an unrelated peer")
		}
	}()
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("unrelated peer remained connected")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("unrelated peer was not rejected")
	}
	l.Close()
	<-finished
}
