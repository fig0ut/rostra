package badsocks

import (
	std_bufio "bufio"
	"context"
	"net"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"
)

// NewBufferedHandler preserves application bytes read ahead while parsing a
// SOCKS handshake. The pinned sing parser passes its raw connection to the
// handler, leaving any unread bytes in the separate buffered reader.
func NewBufferedHandler(reader *std_bufio.Reader, handler socks.HandlerEx) socks.HandlerEx {
	return &bufferedHandler{HandlerEx: handler, reader: reader}
}

type bufferedHandler struct {
	socks.HandlerEx
	reader *std_bufio.Reader
}

func (h *bufferedHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	if length := h.reader.Buffered(); length > 0 {
		buffer := buf.NewSize(length)
		// Drain only already-buffered bytes, without reading from the socket.
		if _, err := buffer.ReadFullFrom(h.reader, length); err != nil {
			buffer.Release()
			N.CloseOnHandshakeFailure(conn, onClose, err)
			return
		}
		conn = bufio.NewCachedConn(conn, buffer)
	}
	h.HandlerEx.NewConnectionEx(ctx, conn, source, destination, onClose)
}
