//go:build !linux

package main

import "github.com/fig0ut/rostra/option"

func runInUserNamespaceIfNeeded(options option.Options, optionsList []*OptionsEntry) error {
	return nil
}
