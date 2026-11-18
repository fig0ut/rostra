package v2rayxhttp

import (
	"context"
	"fmt"

	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	N "github.com/sagernet/sing/common/network"
)

func newDownload(ctx context.Context, dialer N.Dialer, o option.V2RayXHTTPDownloadOptions) (clientConfig, *clientPool, error) {
	if o.Server == "" || o.ServerPort == 0 {
		return clientConfig{}, nil, fmt.Errorf("server and server_port are required")
	}
	if o.Transport == nil || (o.Transport.Type != C.V2RayTransportTypeXHTTP && o.Transport.Type != C.V2RayTransportTypeSplitHTTP) {
		return clientConfig{}, nil, fmt.Errorf("transport must be xhttp or splithttp")
	}

	options := o.Transport.XHTTPOptions
	if options.DownloadSettings != nil {
		return clientConfig{}, nil, fmt.Errorf("nested download_settings is not supported")
	}
	switch options.Mode {
	case "", "auto", modePacket, modeStream:
	case modeSingle:
		return clientConfig{}, nil, fmt.Errorf("download transport cannot use stream-one")
	default:
		return clientConfig{}, nil, fmt.Errorf("invalid mode %q", options.Mode)
	}

	var tc tls.Config
	var err error

	if o.TLS != nil {
		tc, err = tls.NewClient(ctx, logger.NOP(), o.Server, *o.TLS)
		if err != nil {
			return clientConfig{}, nil, err
		}
	}

	// This endpoint only sends GET, so HTTP/1.1 is valid even when the
	// upload endpoint uses stream-up. All other endpoint options are independent.
	options.Mode = modePacket
	config, err := newConfig(options, o.Build(), tc)
	if err != nil {
		return config, nil, err
	}

	factory, err := transportFactory(dialer, config.server, config.version, tc, config.pool.keepAlive)
	if err != nil {
		return config, nil, err
	}

	return config, newClientPool(config.pool, factory), nil
}
