package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/experimental/libbox"
	"github.com/fig0ut/rostra/log"

	E "github.com/sagernet/sing/common/exceptions"

	"github.com/spf13/cobra"
)

var (
	workingDirectory   string
	socketPath         string
	listenAddress      string
	runOwned           bool
	runParentProcessID uint32
)

var commandRun = &cobra.Command{
	Use:   "run",
	Short: "Run the daemon",
	Args:  cobra.NoArgs,
	Run: func(command *cobra.Command, args []string) {
		err := run()
		if err != nil {
			log.Fatal(E.Cause(err, "run daemon"))
		}
	},
}

func init() {
	commandRun.Flags().StringVarP(&workingDirectory, "working-directory", "D", "", "working directory")
	commandRun.Flags().StringVar(&socketPath, "socket", "", "listen on the specified unix domain socket path, or named pipe path on Windows")
	commandRun.Flags().StringVar(&listenAddress, "listen", "", "listen on the specified TCP address (development only)")
	commandRun.Flags().BoolVar(&runOwned, "owned", false, "run as a parent-owned child using a private Unix socket (Linux/Windows)")
	commandRun.Flags().Uint32Var(&runParentProcessID, "parent-pid", 0, "owning application process ID (requires --owned)")
	mainCommand.AddCommand(commandRun)
}

func prepareWorkingDirectory(owner *ownedParent) error {
	if workingDirectory == "" {
		return E.New("missing working directory")
	}
	absoluteWorkingDirectory, err := filepath.Abs(workingDirectory)
	if err != nil {
		return err
	}
	workingDirectory = absoluteWorkingDirectory
	if owner != nil {
		err = os.MkdirAll(workingDirectory, 0o700)
		if err == nil {
			err = secureOwnedDirectory(workingDirectory)
		}
	} else {
		err = preparePlatformWorkingDirectory()
	}
	if err != nil {
		return err
	}
	err = os.Chdir(workingDirectory)
	if err != nil {
		return err
	}
	err = libbox.Setup(&libbox.SetupOptions{
		BasePath:          workingDirectory,
		WorkingPath:       workingDirectory,
		TempPath:          workingDirectory,
		CrashReportSource: "Daemon",
		AppVersion:        C.Version,
	})
	if err != nil {
		return err
	}
	libbox.PromoteOOMDraft()
	libbox.DiscardPowerReportDraft()
	return nil
}

func run() error {
	var owner *ownedParent
	if runOwned {
		if listenAddress != "" {
			return E.New("--listen cannot be used with --owned")
		}
		if socketPath == "" || runParentProcessID == 0 {
			return E.New("--owned requires --socket and --parent-pid")
		}
		var err error
		owner, err = newOwnedParent(runParentProcessID)
		if err != nil {
			return err
		}
		defer owner.Close()
	} else {
		if runParentProcessID != 0 {
			return E.New("--parent-pid requires --owned")
		}
		handled, err := runService()
		if handled {
			return err
		}
	}
	err := prepareWorkingDirectory(owner)
	if err != nil {
		return err
	}
	d, err := newDaemon(owner)
	if err != nil {
		return err
	}
	defer d.Close()
	err = d.Start()
	if err != nil {
		return err
	}
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalChannel)
	if owner != nil {
		inputClosed := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			close(inputClosed)
		}()
		fmt.Println("READY")
		select {
		case <-owner.done:
		case <-inputClosed:
		case <-signalChannel:
		}
	} else {
		<-signalChannel
	}
	return nil
}
