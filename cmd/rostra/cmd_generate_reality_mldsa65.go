package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/spf13/cobra"
)

func init() {
	command := &cobra.Command{
		Use:   "reality-mldsa65-keypair",
		Short: "Generate an ML-DSA-65 seed and verification key for REALITY",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var seed [mldsa65.SeedSize]byte
			input, _ := cmd.Flags().GetString("seed")
			if input != "" {
				decoded, err := base64.RawURLEncoding.DecodeString(input)
				if err != nil || len(decoded) != len(seed) {
					return fmt.Errorf("invalid seed: expected 32 bytes in unpadded base64url")
				}
				copy(seed[:], decoded)
			} else if _, err := rand.Read(seed[:]); err != nil {
				return err
			}
			publicKey, _ := mldsa65.NewKeyFromSeed(&seed)
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Seed: %s\nVerify: %s\n",
				base64.RawURLEncoding.EncodeToString(seed[:]),
				base64.RawURLEncoding.EncodeToString(publicKey.Bytes()))
			return err
		},
	}
	command.Flags().String("seed", "", "Derive the verification key from an existing base64url seed")
	commandGenerate.AddCommand(command)
}
