package option

import "github.com/sagernet/sing/common/json/badoption"

type SelectorOutboundOptions struct {
	OutboundSet               badoption.Listable[string] `json:"outbound_set,omitempty" reference:"outbound_set"`
	Outbounds                 []string                   `json:"outbounds,omitempty" reference:"outbound"`
	Default                   string                     `json:"default,omitempty" reference:"outbound"`
	InterruptExistConnections bool                       `json:"interrupt_exist_connections,omitempty"`
}

type URLTestOutboundOptions struct {
	OutboundSet               badoption.Listable[string] `json:"outbound_set,omitempty" reference:"outbound_set"`
	Outbounds                 []string                   `json:"outbounds,omitempty" reference:"outbound"`
	URL                       string                     `json:"url,omitempty"`
	Interval                  badoption.Duration         `json:"interval,omitempty"`
	Tolerance                 uint16                     `json:"tolerance,omitempty"`
	IdleTimeout               badoption.Duration         `json:"idle_timeout,omitempty"`
	InterruptExistConnections bool                       `json:"interrupt_exist_connections,omitempty"`
}

// LoadBalanceOutboundOptions shares membership and probe fields with URLTest.
type LoadBalanceOutboundOptions struct {
	OutboundSet badoption.Listable[string] `json:"outbound_set,omitempty" reference:"outbound_set"`
	Outbounds   []string                   `json:"outbounds,omitempty" reference:"outbound"`
	Strategy    string                     `json:"strategy,omitempty" enum:"consistent_hash,round_robin"`
	HashKey     string                     `json:"hash_key,omitempty" enum:"source_ip_domain,source_ip_host,destination_domain"`
	URL         string                     `json:"url,omitempty"`
	Interval    badoption.Duration         `json:"interval,omitempty"`
	IdleTimeout badoption.Duration         `json:"idle_timeout,omitempty"`
}
