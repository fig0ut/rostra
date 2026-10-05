package api

import (
	"context"
	"net"
	"net/http"

	"github.com/fig0ut/rostra/adapter"
	boxService "github.com/fig0ut/rostra/adapter/service"
	"github.com/fig0ut/rostra/common/listener"
	"github.com/fig0ut/rostra/common/tls"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/daemon"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	aTLS "github.com/sagernet/sing/common/tls"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.APIServiceOptions](registry, C.TypeAPI, NewService)
}

type Service struct {
	boxService.Adapter
	logger    log.ContextLogger
	options   option.APIServiceOptions
	listener  *listener.Listener
	tlsConfig tls.ServerConfig
	dashboard *dashboard
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.APIServiceOptions) (adapter.Service, error) {
	s := &Service{
		Adapter: boxService.NewAdapter(C.TypeAPI, tag),
		logger:  logger,
		options: options,
		listener: listener.New(listener.Options{
			Context: ctx,
			Logger:  logger,
			Network: []string{N.NetworkTCP},
			Listen:  options.ListenOptions,
		}),
	}
	if options.TLS != nil {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
		s.tlsConfig = tlsConfig
	}
	if options.Dashboard != nil && options.Dashboard.Enabled {
		s.dashboard = newDashboard(ctx, logger, *options.Dashboard)
	}
	return s, nil
}

func (s *Service) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStarted {
		return nil
	}
	ctx := scope.Context()
	startedService := daemon.NewAttachedService(ctx)
	scope.Add(func() error {
		startedService.Close()
		return nil
	})
	grpcServer := daemon.NewServer(startedService, s.options.Secret)
	scope.Add(func() error {
		grpcServer.Stop()
		return nil
	})
	if s.dashboard != nil {
		err := s.dashboard.start(ctx)
		if err != nil {
			return E.Cause(err, "start dashboard")
		}
		scope.Add(s.dashboard.close)
	}
	httpServer := &http.Server{
		//nolint:staticcheck
		Handler: h2c.NewHandler(newHTTPHandler(s.logger, grpcServer, s.options, s.dashboard), new(http2.Server)),
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	if s.tlsConfig != nil {
		err := s.tlsConfig.Start()
		if err != nil {
			return E.Cause(err, "create TLS config")
		}
		scope.Add(s.tlsConfig.Close)
		if !common.Contains(s.tlsConfig.NextProtos(), http2.NextProtoTLS) {
			s.tlsConfig.SetNextProtos(append([]string{http2.NextProtoTLS}, s.tlsConfig.NextProtos()...))
		}
		if !common.Contains(s.tlsConfig.NextProtos(), "http/1.1") {
			s.tlsConfig.SetNextProtos(append(s.tlsConfig.NextProtos(), "http/1.1"))
		}
	}
	tcpListener, err := s.listener.ListenTCP()
	if err != nil {
		return err
	}
	scope.Add(s.listener.Close)
	if s.tlsConfig != nil {
		tcpListener = aTLS.NewListener(tcpListener, s.tlsConfig)
	}
	scope.Add(httpServer.Close)
	go func() {
		serveErr := httpServer.Serve(tcpListener)
		if serveErr != nil && ctx.Err() == nil {
			s.logger.Error("serve error: ", serveErr)
		}
	}()
	return nil
}
