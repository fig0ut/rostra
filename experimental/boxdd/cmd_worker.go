package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/fig0ut/rostra/daemon"
	"github.com/fig0ut/rostra/include"
	"github.com/fig0ut/rostra/log"

	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

var (
	workerSocketPath            string
	workerDaemonRelaySocketPath string
	workerParentProcessID       uint32
	workerOwned                 bool
)

type workerParent interface {
	Close() error
}

var commandWorker = &cobra.Command{
	Use:   "worker",
	Short: "Serve the non-privileged application worker for the application process",
	Args:  cobra.NoArgs,
	Run: func(command *cobra.Command, args []string) {
		err := runWorker()
		if err != nil {
			log.Fatal(E.Cause(err, "run application worker"))
		}
	},
}

func init() {
	commandWorker.Flags().StringVar(&workerSocketPath, "socket", "", "listen on the specified unix domain socket path, or named pipe path on Windows")
	commandWorker.Flags().StringVar(&workerDaemonRelaySocketPath, "daemon-relay-socket", "", "relay the authenticated Windows daemon connection on the specified named pipe path")
	commandWorker.Flags().Uint32Var(&workerParentProcessID, "parent-pid", 0, "expected application parent process ID")
	commandWorker.Flags().BoolVar(&workerOwned, "owned", false, "run as a parent-owned child using a private Unix socket (Linux/Windows)")
	mainCommand.AddCommand(commandWorker)
}

func runWorker() error {
	if workerSocketPath == "" {
		return E.New("missing --socket")
	}
	if workerParentProcessID == 0 {
		return E.New("missing --parent-pid")
	}
	var owner *ownedParent
	var parent workerParent
	var err error
	if workerOwned {
		if workerDaemonRelaySocketPath != "" {
			return E.New("--daemon-relay-socket cannot be used with --owned")
		}
		owner, err = newOwnedParent(workerParentProcessID)
		parent = owner
	} else {
		parent, err = prepareWorkerParent(workerParentProcessID)
	}
	if err != nil {
		return err
	}
	defer parent.Close()
	var listener net.Listener
	if owner != nil {
		listener, err = listenOwnedEndpoint(workerSocketPath, owner)
	} else {
		listener, err = listenWorkerEndpoint(workerSocketPath, parent)
	}
	if err != nil {
		return err
	}
	defer listener.Close()
	server := grpc.NewServer(
		grpc.ChainUnaryInterceptor(daemon.UnaryLocaleInterceptor),
		grpc.ChainStreamInterceptor(daemon.StreamLocaleInterceptor),
	)
	defer server.Stop()
	startedService := daemon.NewStartedService(daemon.ServiceOptions{Context: include.Context(context.Background())})
	defer startedService.Close()
	RegisterApplicationServiceServer(server, &applicationService{
		startedService: startedService,
	})
	relayErrorChannel := make(chan error, 1)
	var relay io.Closer
	if owner == nil {
		relay, err = startWorkerDaemonRelay(workerDaemonRelaySocketPath, parent, func(relayError error) {
			select {
			case relayErrorChannel <- relayError:
			default:
			}
			server.Stop()
		})
	}
	if err != nil {
		return err
	}
	if relay != nil {
		defer relay.Close()
	}
	inputClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(inputClosed)
	}()
	var parentDone <-chan struct{}
	if owner != nil {
		parentDone = owner.done
	}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-inputClosed:
		case <-parentDone:
		case <-stopped:
			return
		}
		if relay != nil {
			relay.Close()
		}
		server.Stop()
	}()
	fmt.Println("READY")
	err = server.Serve(listener)
	select {
	case relayError := <-relayErrorChannel:
		return relayError
	case <-parentDone:
		return nil
	default:
	}
	if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}
