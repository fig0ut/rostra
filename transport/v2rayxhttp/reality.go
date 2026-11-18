//go:build with_utls

package v2rayxhttp

import "github.com/sagernet/sing-box/common/tls"

func isReality(config tls.Config) bool { _, ok := config.(*tls.RealityClientConfig); return ok }
