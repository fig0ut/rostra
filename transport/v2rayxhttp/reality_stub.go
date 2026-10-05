//go:build !with_utls

package v2rayxhttp

import "github.com/fig0ut/rostra/common/tls"

func isReality(config tls.Config) bool { return false }
