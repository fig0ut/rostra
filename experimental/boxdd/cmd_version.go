package main

import (
	"encoding/json"

	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/daemon"

	"github.com/spf13/cobra"
)

var commandVersion = &cobra.Command{
	Use:   "version",
	Short: "Print the core and API versions as JSON",
	Args:  cobra.NoArgs,
	RunE: func(command *cobra.Command, args []string) error {
		return json.NewEncoder(command.OutOrStdout()).Encode(struct {
			Version    string `json:"version"`
			APIVersion int32  `json:"api_version"`
		}{C.Version, daemon.APIVersion})
	},
}

func init() {
	mainCommand.AddCommand(commandVersion)
}
