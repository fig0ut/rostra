package outboundset

import (
	"strconv"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// bootstrapOptions selects only the independently available outbound and DNS
// dependencies needed to download a source, excluding inbounds and services.
func bootstrapOptions(options option.Options, client option.HTTPClientOptions) (option.Options, error) {
	bootstrap := option.Options{
		Log:                  &option.LogOptions{Disabled: true},
		DNS:                  options.DNS,
		Certificate:          options.Certificate,
		CertificateProviders: options.CertificateProviders,
		NetworkNamespaces:    options.NetworkNamespaces,
	}
	if options.Route != nil {
		route := *options.Route
		route.Rules, route.RuleSet = nil, nil
		route.Final, route.DefaultHTTPClient = "", ""
		bootstrap.Route = &route
	}
	outbounds := make(map[string]option.Outbound)
	endpoints := make(map[string]option.Endpoint)
	for i, value := range options.Outbounds {
		if value.Tag == "" {
			value.Tag = strconv.Itoa(i)
		}
		outbounds[value.Tag] = value
	}
	for i, value := range options.Endpoints {
		if value.Tag == "" {
			value.Tag = strconv.Itoa(i)
		}
		endpoints[value.Tag] = value
	}
	state := make(map[string]uint8)
	var add func(string) error
	add = func(tag string) error {
		if state[tag] == 2 {
			return nil
		}
		if state[tag] == 1 {
			return E.New("circular bootstrap outbound dependency: ", tag)
		}
		state[tag] = 1
		if value, found := outbounds[tag]; found {
			switch group := value.Options.(type) {
			case *option.SelectorOutboundOptions:
				if len(group.OutboundSet) > 0 {
					return E.New("download depends on outbound-set group: ", tag, "; provide cache or initial_path")
				}
			case *option.URLTestOutboundOptions:
				if len(group.OutboundSet) > 0 {
					return E.New("download depends on outbound-set group: ", tag, "; provide cache or initial_path")
				}
			}
			for _, dependency := range referencedOutbounds(value.Options) {
				if err := add(dependency); err != nil {
					return err
				}
			}
			bootstrap.Outbounds = append(bootstrap.Outbounds, value)
		} else if value, found := endpoints[tag]; found {
			for _, dependency := range referencedOutbounds(value.Options) {
				if err := add(dependency); err != nil {
					return err
				}
			}
			bootstrap.Endpoints = append(bootstrap.Endpoints, value)
		} else {
			return E.New("bootstrap outbound unavailable: ", tag, "; provide cache or initial_path")
		}
		state[tag] = 2
		return nil
	}
	references := referencedOutbounds(options.DNS)
	if client.Detour != "" {
		references = append(references, client.Detour)
	}
	for _, reference := range references {
		if err := add(reference); err != nil {
			return option.Options{}, err
		}
	}
	return bootstrap, nil
}
