package option

import (
	"testing"

	C "github.com/sagernet/sing-box/constant"

	"github.com/stretchr/testify/require"
)

func TestOutboundSetFormatInference(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		remote bool
		valid  bool
	}{
		{name: "relative", source: "outbounds.json", valid: true},
		{name: "unix", source: "/tmp/outbounds.json", valid: true},
		{name: "windows", source: `C:\Users\runner\outbounds.json`, valid: true},
		{name: "windows forward slashes", source: "C:/Users/runner/outbounds.json", valid: true},
		{name: "windows UNC", source: `\\server\share\outbounds.json`, valid: true},
		{name: "literal URL characters", source: "/tmp/outbounds#?%.json", valid: true},
		{name: "local missing extension", source: "outbounds"},
		{name: "local query is literal", source: "outbounds.json?download=1"},
		{name: "remote", source: "https://example.com/outbounds.json", remote: true, valid: true},
		{name: "remote query and fragment", source: "https://example.com/outbounds.json?download=1#fragment", remote: true, valid: true},
		{name: "remote escaped extension", source: "https://example.com/outbounds%2Ejson", remote: true, valid: true},
		{name: "remote query is not extension", source: "https://example.com/download?file=outbounds.json", remote: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			set := OutboundSet{Type: C.OutboundSetTypeLocal, Tag: []string{"test"}, LocalOptions: LocalOutboundSet{Path: test.source}}
			if test.remote {
				set.Type = C.OutboundSetTypeRemote
				set.RemoteOptions.URL = test.source
			}
			if test.valid {
				require.NoError(t, set.Validate())
			} else {
				require.ErrorContains(t, set.Validate(), "missing outbound-set format")
				set.Format = C.OutboundSetFormatSource
				require.NoError(t, set.Validate())
			}
		})
	}
}
