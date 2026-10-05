//go:build generate_completions

package main

import "github.com/fig0ut/rostra/log"

func main() {
	err := generateCompletions()
	if err != nil {
		log.Fatal(err)
	}
}

func generateCompletions() error {
	err := mainCommand.GenBashCompletionFile("release/completions/rostra.bash")
	if err != nil {
		return err
	}
	err = mainCommand.GenFishCompletionFile("release/completions/rostra.fish", true)
	if err != nil {
		return err
	}
	err = mainCommand.GenZshCompletionFile("release/completions/rostra.zsh")
	if err != nil {
		return err
	}
	return nil
}
