package option

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/schema"
	"github.com/sagernet/sing/common/json"
)

// XHTTPRange represents an integer or an inclusive "min-max" range.
type XHTTPRange struct {
	From int32
	To   int32
}

func (r XHTTPRange) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return &schema.Node{OneOf: []*schema.Node{schema.UnsignedNode(31), {Type: "string", Pattern: `^[0-9]+-[0-9]+$`}}}, nil
}

func (r XHTTPRange) MarshalJSON() ([]byte, error) {
	if r.From == r.To {
		return json.Marshal(r.From)
	}
	return json.Marshal(fmt.Sprintf("%d-%d", r.From, r.To))
}

func (r *XHTTPRange) UnmarshalJSON(data []byte) error {
	var number int32
	if err := json.Unmarshal(data, &number); err == nil {
		if number < 0 {
			return fmt.Errorf("XHTTP range cannot be negative")
		}
		*r = XHTTPRange{number, number}
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	lower, upper, found := strings.Cut(text, "-")
	if !found {
		return fmt.Errorf("invalid XHTTP range %q", text)
	}
	min, err := strconv.ParseInt(lower, 10, 32)
	if err != nil {
		return err
	}
	max, err := strconv.ParseInt(upper, 10, 32)
	if err != nil {
		return err
	}
	if min < 0 || max < min {
		return fmt.Errorf("invalid XHTTP range %q", text)
	}
	*r = XHTTPRange{int32(min), int32(max)}
	return nil
}

type V2RayXHTTPOptions struct {
	Host                 string                     `json:"host,omitempty"`
	Path                 string                     `json:"path,omitempty"`
	Mode                 string                     `json:"mode,omitempty" enum:"auto,packet-up,stream-up,stream-one"`
	HTTPVersion          string                     `json:"http_version,omitempty" enum:"1.1,2,3"`
	Headers              map[string]string          `json:"headers,omitempty"`
	XPaddingBytes        *XHTTPRange                `json:"x_padding_bytes,omitempty"`
	XPaddingObfsMode     bool                       `json:"x_padding_obfs_mode,omitempty"`
	XPaddingMethod       string                     `json:"x_padding_method,omitempty" enum:"repeat-x,tokenish"`
	XPaddingPlacement    string                     `json:"x_padding_placement,omitempty" enum:"queryInHeader,header,cookie,query"`
	XPaddingKey          string                     `json:"x_padding_key,omitempty"`
	XPaddingHeader       string                     `json:"x_padding_header,omitempty"`
	SessionIDPlacement   string                     `json:"session_id_placement,omitempty" enum:"path,query,header,cookie"`
	SessionIDKey         string                     `json:"session_id_key,omitempty"`
	SessionIDTable       string                     `json:"session_id_table,omitempty"`
	SessionIDLength      *XHTTPRange                `json:"session_id_length,omitempty"`
	SeqPlacement         string                     `json:"seq_placement,omitempty" enum:"path,query,header,cookie"`
	SeqKey               string                     `json:"seq_key,omitempty"`
	UplinkHTTPMethod     string                     `json:"uplink_http_method,omitempty"`
	UplinkDataPlacement  string                     `json:"uplink_data_placement,omitempty" enum:"auto,body,header,cookie"`
	UplinkDataKey        string                     `json:"uplink_data_key,omitempty"`
	UplinkChunkSize      *XHTTPRange                `json:"uplink_chunk_size,omitempty"`
	ServerMaxHeaderBytes int                        `json:"server_max_header_bytes,omitempty"`
	DownloadSettings     *V2RayXHTTPDownloadOptions `json:"download_settings,omitempty"`
	ScMaxBufferedPosts   int                        `json:"sc_max_buffered_posts,omitempty"`
	NoSSEHeader          bool                       `json:"no_sse_header,omitempty"`
	NoGRPCHeader         bool                       `json:"no_grpc_header,omitempty"`
	ScMaxEachPostBytes   *XHTTPRange                `json:"sc_max_each_post_bytes,omitempty"`
	ScMinPostsIntervalMs *XHTTPRange                `json:"sc_min_posts_interval_ms,omitempty"`
	Xmux                 *V2RayXHTTPXMuxOptions     `json:"xmux,omitempty"`
}

// Download settings describe a separate HTTP endpoint, sharing the outbound dialer.
type V2RayXHTTPDownloadOptions struct {
	ServerOptions
	OutboundTLSOptionsContainer
	Transport *V2RayTransportOptions `json:"transport,omitempty"`
}

// XMUX controls HTTP connection pooling, independently of protocol multiplexing.
type V2RayXHTTPXMuxOptions struct {
	MaxConcurrency   *XHTTPRange `json:"max_concurrency,omitempty"`
	MaxConnections   *XHTTPRange `json:"max_connections,omitempty"`
	CMaxReuseTimes   *XHTTPRange `json:"c_max_reuse_times,omitempty"`
	HMaxRequestTimes *XHTTPRange `json:"h_max_request_times,omitempty"`
	HMaxReusableSecs *XHTTPRange `json:"h_max_reusable_secs,omitempty"`
	HKeepAlivePeriod int64       `json:"h_keep_alive_period,omitempty"`
}
