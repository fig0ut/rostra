package sniff

import (
	std_bufio "bufio"
	"context"
	"errors"
	"io"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/common/badhttp"
	C "github.com/fig0ut/rostra/constant"

	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

func HTTPHost(_ context.Context, metadata *adapter.InboundContext, reader io.Reader) error {
	request, err := badhttp.ReadRequest(std_bufio.NewReader(reader))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return E.Cause1(ErrNeedMoreData, err)
		} else {
			return err
		}
	}
	metadata.Protocol = C.ProtocolHTTP
	metadata.Domain = M.ParseSocksaddr(request.Host).Fqdn
	return nil
}
