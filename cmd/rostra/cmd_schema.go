package main

import (
	"context"
	"os"
	"reflect"

	"github.com/fig0ut/rostra/include"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"
	"github.com/fig0ut/rostra/schema"

	"github.com/spf13/cobra"
)

var commandSchemaFlagOutput string

var commandSchema = &cobra.Command{
	Use:   "schema",
	Short: "Generate configuration JSON schema",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		err := generateSchema()
		if err != nil {
			log.Fatal(err)
		}
	},
}

func init() {
	commandSchema.Flags().StringVarP(&commandSchemaFlagOutput, "output", "o", "", "write schema to file instead of stdout")
	mainCommand.AddCommand(commandSchema)
}

func generateSchema() error {
	content, err := schema.Generate(include.Context(context.Background()), reflect.TypeFor[option.Options]())
	if err != nil {
		return err
	}
	if commandSchemaFlagOutput != "" {
		return os.WriteFile(commandSchemaFlagOutput, content, 0o644)
	}
	_, err = os.Stdout.Write(content)
	return err
}
