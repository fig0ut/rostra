//go:build !with_acme

package include

import (
	"context"

	"github.com/fig0ut/rostra/adapter"
	"github.com/fig0ut/rostra/adapter/certificate"
	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/log"
	"github.com/fig0ut/rostra/option"

	E "github.com/sagernet/sing/common/exceptions"
)

func registerACMECertificateProvider(registry *certificate.Registry) {
	certificate.Register[option.ACMECertificateProviderOptions](registry, C.TypeACME, func(ctx context.Context, logger log.ContextLogger, tag string, options option.ACMECertificateProviderOptions) (adapter.CertificateProviderService, error) {
		return nil, E.New(`ACME is not included in this build, rebuild with -tags with_acme`)
	})
}
