package redirect

import (
	"context"
	"net"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/adapter/inbound"
	"github.com/fig0ut/rostra/common/listener"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing/common/control"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func RegisterRedirect(registry *inbound.Registry) {
	inbound.Register[option.RedirectInboundOptions](registry, C.TypeRedirect, NewRedirect)
}

type Redirect struct {
	inbound.Adapter
	router   adapter.Router
	logger   log.ContextLogger
	listener *listener.Listener
}

func NewRedirect(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.RedirectInboundOptions) (adapter.Inbound, error) {
	redirect := &Redirect{
		Adapter: inbound.NewAdapter(C.TypeRedirect, tag),
		router:  router,
		logger:  logger,
	}
	redirect.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: redirect,
	})
	return redirect, nil
}

func (h *Redirect) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	err := h.listener.Start()
	if err != nil {
		return err
	}
	scope.Add(h.listener.Close)
	return nil
}

func (h *Redirect) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	destination, err := control.GetOriginalDestination(conn)
	if err != nil {
		conn.Close()
		h.logger.ErrorContext(ctx, "process connection from ", conn.RemoteAddr(), ": get redirect destination: ", err)
		return
	}
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	metadata.Destination = M.SocksaddrFromNetIP(destination)
	metadata.OriginDestination = metadata.Destination
	h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}
