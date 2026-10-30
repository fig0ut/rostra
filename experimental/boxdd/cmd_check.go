package main

import (
	"fmt"
	"io"

	"github.com/fig0ut/rostra/daemon"
	"github.com/fig0ut/rostra/include"

	"github.com/spf13/cobra"
)

var commandCheck = &cobra.Command{
	Use:   "check",
	Short: "Check configuration from standard input without starting networking",
	Args:  cobra.NoArgs,
	RunE: func(command *cobra.Command, args []string) error {
		const maxConfigSize = 4 * 1024 * 1024
		content, err := io.ReadAll(io.LimitReader(command.InOrStdin(), maxConfigSize+1))
		if err != nil {
			return err
		}
		if len(content) > maxConfigSize {
			return fmt.Errorf("configuration exceeds 4 MiB")
		}
		service := daemon.NewStartedService(daemon.ServiceOptions{Context: include.Context(command.Context())})
		defer service.Close()
		return service.CheckConfig(command.Context(), string(content))
	},
}

func init() {
	mainCommand.AddCommand(commandCheck)
}
