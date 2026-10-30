package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"google.golang.org/grpc/credentials"
)

// ownedParent authenticates the application that started run/worker --owned.
// Installed-service authentication remains separate from this per-user mode.
type ownedParent struct {
	pid      uint32
	identity peerIdentity
	done     chan struct{}
	stop     func()
}

func (p *ownedParent) Close() error {
	p.stop()
	return nil
}

type ownedListener struct {
	net.Listener
	parent *ownedParent
}

func listenOwnedEndpoint(path string, parent *ownedParent) (net.Listener, error) {
	if !filepath.IsAbs(path) || len([]byte(path)) >= 108 {
		return nil, fmt.Errorf("owned socket must be an absolute path shorter than 108 bytes")
	}
	if err := secureOwnedDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// Never remove another process's endpoint. Sawfish creates a fresh directory.
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, fmt.Errorf("owned endpoint already exists or cannot be inspected")
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	listener := &ownedListener{Listener: l, parent: parent}
	go func() { <-parent.done; _ = listener.Close() }()
	return listener, nil
}

func (l *ownedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		pid, err := ownedPeerPID(c)
		select {
		case <-l.parent.done:
			_ = c.Close()
			return nil, net.ErrClosed
		default:
		}
		if err == nil && pid == l.parent.pid {
			return c, nil
		}
		_ = c.Close()
	}
}

type ownedCredentials struct {
	daemon *Daemon
	parent *ownedParent
}
type ownedConnection struct {
	net.Conn
	daemon   *Daemon
	identity peerIdentity
	once     sync.Once
}

func (c *ownedConnection) peerConnectionIdentity() peerIdentity {
	return c.identity
}

func (c *ownedConnection) Close() error {
	var err error
	c.once.Do(func() {
		c.daemon.unregisterPeerConnection(c)
		err = c.Conn.Close()
	})
	return err
}

func (c *ownedCredentials) ClientHandshake(context.Context, string, net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, fmt.Errorf("server credentials only")
}

func (c *ownedCredentials) ServerHandshake(raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	pid, err := ownedPeerPID(raw)
	if err != nil || pid != c.parent.pid {
		return nil, nil, fmt.Errorf("owned client is not the owning application")
	}
	select {
	case <-c.parent.done:
		return nil, nil, net.ErrClosed
	default:
	}
	conn := &ownedConnection{
		Conn:     raw,
		daemon:   c.daemon,
		identity: c.parent.identity,
	}
	c.daemon.registerPeerConnection(conn)
	return conn, &peerAuthInfo{
		CommonAuthInfo: credentials.CommonAuthInfo{
			SecurityLevel: credentials.PrivacyAndIntegrity,
		},
		identity: conn.identity,
	}, nil
}

func (*ownedCredentials) Info() credentials.ProtocolInfo {
	return credentials.ProtocolInfo{SecurityProtocol: "owned-parent"}
}

func (c *ownedCredentials) Clone() credentials.TransportCredentials {
	return &ownedCredentials{
		daemon: c.daemon,
		parent: c.parent,
	}
}

func (*ownedCredentials) OverrideServerName(string) error {
	return nil
}
