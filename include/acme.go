//go:build with_acme

package include

import (
	"github.com/fig0ut/rostra/adapter/certificate"
	"github.com/fig0ut/rostra/service/acme"
)

func registerACMECertificateProvider(registry *certificate.Registry) {
	acme.RegisterCertificateProvider(registry)
}
