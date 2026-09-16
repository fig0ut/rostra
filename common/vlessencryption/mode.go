package vlessencryption

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"

	"lukechampine.com/blake3"
)

type wireMode uint8

const (
	modeNative wireMode = iota
	modeXorPublic
	modeRandom
)

func parseWireMode(mode string) (wireMode, error) {
	switch mode {
	case "native":
		return modeNative, nil
	case "xorpub":
		return modeXorPublic, nil
	case "random":
		return modeRandom, nil
	default:
		return 0, fmt.Errorf("VLESS encryption: mode must be native, xorpub or random")
	}
}

// The public exchange and record headers use independent AES-CTR streams.
// Header masking belongs in the record codec: this avoids a second connection
// wrapper, partial-header state, and mutation of the caller's write buffer.
func newMask(secret, iv []byte) cipher.Stream {
	var key [32]byte
	blake3.DeriveKey(key[:], "VLESS", secret)
	block, _ := aes.NewCipher(key[:])
	return cipher.NewCTR(block, iv)
}
