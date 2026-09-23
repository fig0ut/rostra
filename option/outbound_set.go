package option

import (
	"context"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/schema"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/json/badjson"
	"github.com/sagernet/sing/common/json/badoption"
)

type _OutboundSet struct {
	Type          string                     `json:"type,omitempty" enum:"inline,local,remote"`
	Tag           badoption.Listable[string] `json:"tag"`
	Format        string                     `json:"format,omitempty" enum:"source"`
	Override      map[string]json.RawMessage `json:"override,omitempty"`
	InlineOptions InlineOutboundSet          `json:"-"`
	LocalOptions  LocalOutboundSet           `json:"-"`
	RemoteOptions RemoteOutboundSet          `json:"-"`
}

type OutboundSet _OutboundSet

// Keep definitions untyped until overrides have been applied, then decode them
// through the outbound registry with the same validation as ordinary outbounds.
type InlineOutboundSet struct {
	Outbounds []json.RawMessage `json:"outbounds"`
}

type LocalOutboundSet struct {
	Path string `json:"path"`
}

type RemoteOutboundSet struct {
	URL            string             `json:"url"`
	InitialPath    string             `json:"initial_path,omitempty"`
	HTTPClient     *HTTPClientOptions `json:"http_client,omitempty"`
	UpdateInterval badoption.Duration `json:"update_interval,omitempty"`
}

type PlainOutboundSet struct {
	Version uint8 `json:"version" enum:"1"`
	InlineOutboundSet
}

func (o OutboundSet) MarshalJSON() ([]byte, error) {
	var value any
	switch o.Type {
	case "", C.OutboundSetTypeInline:
		o.Type = ""
		value = o.InlineOptions
	case C.OutboundSetTypeLocal:
		value = o.LocalOptions
	case C.OutboundSetTypeRemote:
		value = o.RemoteOptions
	default:
		return nil, E.New("unknown outbound-set type: ", o.Type)
	}
	return badjson.MarshallObjects(_OutboundSet(o), value)
}

func (o *OutboundSet) UnmarshalJSONContext(ctx context.Context, content []byte) error {
	err := json.UnmarshalContext(ctx, content, (*_OutboundSet)(o))
	if err != nil {
		return err
	}
	var value any
	switch o.Type {
	case "", C.OutboundSetTypeInline:
		o.Type = C.OutboundSetTypeInline
		value = &o.InlineOptions
	case C.OutboundSetTypeLocal:
		value = &o.LocalOptions
	case C.OutboundSetTypeRemote:
		value = &o.RemoteOptions
	default:
		return E.New("unknown outbound-set type: ", o.Type)
	}
	err = badjson.UnmarshallExcludedContext(ctx, content, (*_OutboundSet)(o), value)
	if err != nil {
		return err
	}
	return o.Validate()
}

func (o OutboundSet) Validate() error {
	if len(o.Tag) == 0 {
		return E.New("missing outbound-set tag")
	}
	for _, tag := range o.Tag {
		if tag == "" || strings.Contains(tag, "/") {
			return E.New("outbound-set tag must be nonempty and must not contain '/'")
		}
	}
	for _, key := range []string{"type", "tag"} {
		if _, exists := o.Override[key]; exists {
			return E.New("outbound-set override must not change ", key)
		}
	}
	if o.Format != "" && o.Format != C.OutboundSetFormatSource {
		return E.New("unknown outbound-set format: ", o.Format)
	}
	var source, sourcePath string
	switch o.Type {
	case "", C.OutboundSetTypeInline:
		if len(o.Tag) != 1 {
			return E.New("inline outbound-set does not support multiple tags")
		}
		if o.Format != "" {
			return E.New("inline outbound-set does not use format")
		}
		if len(o.InlineOptions.Outbounds) == 0 {
			return E.New("empty inline outbound-set")
		}
	case C.OutboundSetTypeLocal:
		source = o.LocalOptions.Path
		sourcePath = source
		if source == "" {
			return E.New("missing outbound-set path")
		}
	case C.OutboundSetTypeRemote:
		if o.RemoteOptions.UpdateInterval < 0 {
			return E.New("outbound-set update_interval must be positive")
		}
		source = o.RemoteOptions.URL
		parsedURL, err := url.Parse(strings.ReplaceAll(source, C.OutboundSetTagPlaceholder, "tag"))
		if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return E.New("outbound-set url must be an HTTP or HTTPS URL")
		}
		sourcePath = parsedURL.Path
		if len(o.Tag) > 1 && o.RemoteOptions.InitialPath != "" && !strings.Contains(o.RemoteOptions.InitialPath, C.OutboundSetTagPlaceholder) {
			return E.New("missing {tag} placeholder in initial_path")
		}
	default:
		return E.New("unknown outbound-set type: ", o.Type)
	}
	if source != "" {
		if len(o.Tag) > 1 && !strings.Contains(source, C.OutboundSetTagPlaceholder) {
			return E.New("missing {tag} placeholder in outbound-set source")
		}
		if o.Format == "" && filepath.Ext(sourcePath) != ".json" {
			return E.New("missing outbound-set format: specify source for paths or URLs without a .json extension")
		}
	}
	return nil
}

func (o OutboundSet) DescribeSchema(builder schema.Builder) (*schema.Node, error) {
	return builder.Define("OutboundSet", func() (*schema.Node, error) {
		return schema.DiscriminatedUnion(builder, "type", false, []schema.UnionVariant{
			{Value: C.OutboundSetTypeInline, TypeOptional: true},
			{Value: C.OutboundSetTypeLocal, StructType: reflect.TypeFor[LocalOutboundSet]()},
			{Value: C.OutboundSetTypeRemote, StructType: reflect.TypeFor[RemoteOutboundSet]()},
		}, func(node *schema.Node) error {
			node.Properties.Put("tag", schema.ListableOf(schema.StringNode()))
			typeNode, _ := node.Properties.Get("type")
			if typeNode.Const == nil {
				member := schema.LooseObject()
				member.Properties.Put("type", schema.StringNode())
				member.Properties.Put("tag", schema.StringNode())
				member.Required = []string{"type", "tag"}
				node.Properties.Put("outbounds", &schema.Node{Type: "array", Items: member})
				node.Required = append(node.Required, "outbounds")
			} else {
				node.Properties.Put("format", schema.StringEnum(C.OutboundSetFormatSource))
				if typeNode.Const == C.OutboundSetTypeLocal {
					node.Required = append(node.Required, "path")
				} else {
					node.Required = append(node.Required, "url")
				}
			}
			node.Properties.Put("override", schema.LooseObject())
			node.Required = append(node.Required, "tag")
			return nil
		})
	})
}
