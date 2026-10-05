package sniff_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/common/sniff"
	C "github.com/fig0ut/rostra/constant"

	"github.com/stretchr/testify/require"
)

func TestSniffRDP(t *testing.T) {
	t.Parallel()

	pkt, err := hex.DecodeString("030000130ee00000000000010008000b000000010008000b000000")
	require.NoError(t, err)
	var metadata adapter.InboundContext
	err = sniff.RDP(context.TODO(), &metadata, bytes.NewReader(pkt))
	require.NoError(t, err)
	require.Equal(t, C.ProtocolRDP, metadata.Protocol)
}
