//go:build !generate_completions

package main

import "github.com/fig0ut/rostra/log"

func main() {
	if err := mainCommand.Execute(); err != nil {
		log.Fatal(err)
	}
}
