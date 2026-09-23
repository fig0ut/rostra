package outboundset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/logger"
)

type Expansion struct {
	Options  option.Options
	loader   *loader
	cache    []cacheEntry
	original option.Options
	loaded   map[string][]option.Outbound
	refresh  map[string]bool
}

// SaveCache commits downloaded sources only after the caller has successfully
// constructed every outbound. Failed construction must not poison the cache.
func (e *Expansion) SaveCache() {
	for _, entry := range e.cache {
		if err := e.loader.save(entry); err != nil {
			e.loader.log.Warn(E.Cause(err, "save outbound-set cache"))
		}
	}
	e.cache = nil
}

// Prepare loads and validates the initial outbound-set snapshot.
func Prepare(ctx context.Context, log logger.ContextLogger, options option.Options) (*Expansion, error) {
	return PrepareWithDownloader(ctx, log, options, nil)
}

func PrepareWithDownloader(ctx context.Context, log logger.ContextLogger, options option.Options, download DownloadFunc) (*Expansion, error) {
	sets := make(map[string]option.OutboundSet)
	for i, set := range options.OutboundSet {
		if err := set.Validate(); err != nil {
			return nil, E.Cause(err, "outbound_set[", i, "]")
		}
		for _, tag := range set.Tag {
			if _, exists := sets[tag]; exists {
				return nil, E.New("duplicate outbound-set tag: ", tag)
			}
			sets[tag] = set
		}
	}
	// Check references before any file or network access.
	for _, outbound := range options.Outbounds {
		var references []string
		switch group := outbound.Options.(type) {
		case *option.SelectorOutboundOptions:
			references = group.OutboundSet
		case *option.URLTestOutboundOptions:
			references = group.OutboundSet
		}
		for _, tag := range references {
			if _, exists := sets[tag]; !exists {
				return nil, E.New("outbound[", outbound.Tag, "]: outbound-set not found: ", tag)
			}
		}
	}
	if len(sets) == 0 {
		return &Expansion{Options: options}, nil
	}
	loader := newLoader(ctx, log, options.Experimental)
	defer loader.close()
	loader.options = options
	loader.download = download
	loaded := make(map[string][]option.Outbound)
	loader.loaded = loaded
	loader.cacheOnly = true
	refresh := make(map[string]bool)
	var pending []cacheEntry
	var missing []string
	for _, set := range options.OutboundSet {
		for _, tag := range set.Tag {
			outbounds, cache, err := loader.load(set, tag)
			if errors.Is(err, errUncached) {
				missing = append(missing, tag)
				continue
			}
			if err != nil {
				return nil, E.Cause(err, "outbound-set[", tag, "]")
			}
			loaded[tag] = outbounds
			refresh[tag] = cache.refresh
			if cache.path != "" {
				pending = append(pending, cache)
			}
		}
	}
	loader.cacheOnly = false
	for len(missing) > 0 {
		var remaining []string
		var firstError error
		for _, tag := range missing {
			outbounds, cache, err := loader.load(sets[tag], tag)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if firstError == nil {
					firstError = E.Cause(err, "outbound-set[", tag, "]")
				}
				remaining = append(remaining, tag)
				continue
			}
			loaded[tag] = outbounds
			refresh[tag] = cache.refresh
			if cache.path != "" {
				pending = append(pending, cache)
			}
		}
		if len(remaining) == len(missing) {
			return nil, firstError
		}
		missing = remaining
	}
	result, err := expandLoaded(options, loaded, true)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &Expansion{Options: result, loader: loader, cache: pending, original: options, loaded: loaded, refresh: refresh}, nil
}

func expandLoaded(options option.Options, loaded map[string][]option.Outbound, strictDefault bool) (option.Options, error) {
	result := options
	result.Outbounds = slices.Clone(options.Outbounds)
	members := make(map[string][]string)
	for _, set := range options.OutboundSet {
		for _, tag := range set.Tag {
			for _, outbound := range loaded[tag] {
				members[tag] = append(members[tag], outbound.Tag)
			}
			result.Outbounds = append(result.Outbounds, loaded[tag]...)
		}
	}
	expandTags := func(tags, setTags []string) []string {
		result := slices.Clone(tags)
		seen := make(map[string]bool, len(tags))
		for _, tag := range tags {
			seen[tag] = true
		}
		for _, setTag := range setTags {
			for _, tag := range members[setTag] {
				if !seen[tag] {
					result = append(result, tag)
					seen[tag] = true
				}
			}
		}
		return result
	}
	for i, outbound := range result.Outbounds {
		switch group := outbound.Options.(type) {
		case *option.SelectorOutboundOptions:
			cloned := *group
			cloned.Outbounds = expandTags(group.Outbounds, group.OutboundSet)
			cloned.OutboundSet = nil
			if len(cloned.Outbounds) == 0 {
				return option.Options{}, E.New("selector[", outbound.Tag, "]: missing outbounds")
			}
			if strictDefault && cloned.Default != "" && !slices.Contains(cloned.Outbounds, cloned.Default) {
				return option.Options{}, E.New("selector[", outbound.Tag, "]: default outbound is not a member: ", cloned.Default)
			}
			result.Outbounds[i].Options = &cloned
		case *option.URLTestOutboundOptions:
			cloned := *group
			cloned.Outbounds = expandTags(group.Outbounds, group.OutboundSet)
			cloned.OutboundSet = nil
			if len(cloned.Outbounds) == 0 {
				return option.Options{}, E.New("urltest[", outbound.Tag, "]: missing outbounds")
			}
			result.Outbounds[i].Options = &cloned
		}
	}
	if err := validateDependencies(result); err != nil {
		return option.Options{}, err
	}
	result.OutboundSet = nil
	return result, nil
}

func decodeMembers(ctx context.Context, set option.OutboundSet, tag string, definitions []json.RawMessage) ([]option.Outbound, error) {
	if len(definitions) == 0 {
		return nil, E.New("empty outbound-set")
	}
	objects := make([]map[string]json.RawMessage, len(definitions))
	names := make([]string, len(definitions))
	seen := make(map[string]bool)
	for i, definition := range definitions {
		err := json.Unmarshal(definition, &objects[i])
		if err != nil || objects[i] == nil {
			return nil, E.New("outbounds[", i, "] must be an object")
		}
		if err = json.Unmarshal(objects[i]["tag"], &names[i]); err != nil || names[i] == "" {
			return nil, E.New("outbounds[", i, "]: missing or invalid tag")
		}
		if seen[names[i]] {
			return nil, E.New("duplicate member tag: ", names[i])
		}
		seen[names[i]] = true
	}
	outbounds := make([]option.Outbound, len(definitions))
	for i, object := range objects {
		// Source-local detours stay inside this instance of the set. Overrides
		// are applied afterwards and use literal global outbound tags.
		var detour string
		if json.Unmarshal(object["detour"], &detour) == nil && seen[detour] {
			object["detour"], _ = json.Marshal(tag + "/" + detour)
		}
		mergeOverride(object, set.Override)
		object["tag"], _ = json.Marshal(tag + "/" + names[i])
		content, err := json.Marshal(object)
		if err != nil {
			return nil, err
		}
		err = json.UnmarshalContext(ctx, content, &outbounds[i])
		if err != nil {
			return nil, E.Cause(err, "member[", names[i], "]")
		}
		switch outbounds[i].Type {
		case C.TypeSelector, C.TypeURLTest:
			return nil, E.New("member[", names[i], "]: outbound sets contain leaf outbounds, not groups")
		}
	}
	return outbounds, nil
}

// JSON merge-patch semantics: objects merge, arrays/scalars replace, null
// removes a field. Raw messages preserve explicit false, zero and empty strings.
func mergeOverride(target, override map[string]json.RawMessage) {
	for key, value := range override {
		trimmed := bytes.TrimSpace(value)
		if bytes.Equal(trimmed, []byte("null")) {
			delete(target, key)
			continue
		}
		if len(trimmed) > 0 && trimmed[0] == '{' {
			var sourceObject, targetObject map[string]json.RawMessage
			if json.Unmarshal(value, &sourceObject) == nil {
				_ = json.Unmarshal(target[key], &targetObject)
				if targetObject == nil {
					targetObject = make(map[string]json.RawMessage)
				}
				mergeOverride(targetObject, sourceObject)
				target[key], _ = json.Marshal(targetObject)
				continue
			}
		}
		target[key] = value
	}
}

func validateDependencies(options option.Options) error {
	nodes := make(map[string][]string)
	var order []string
	add := func(tag string, index int, value any) error {
		if tag == "" {
			tag = strconv.Itoa(index)
		}
		if _, exists := nodes[tag]; exists {
			return E.New("duplicate outbound/endpoint tag: ", tag)
		}
		var dependencies []string
		if wrapper, ok := value.(option.DialerOptionsWrapper); ok {
			if detour := wrapper.TakeDialerOptions().Detour; detour != "" {
				dependencies = append(dependencies, detour)
			}
		}
		switch group := value.(type) {
		case *option.SelectorOutboundOptions:
			dependencies = append(dependencies, group.Outbounds...)
		case *option.URLTestOutboundOptions:
			dependencies = append(dependencies, group.Outbounds...)
		}
		nodes[tag] = dependencies
		order = append(order, tag)
		return nil
	}
	for i, outbound := range options.Outbounds {
		if err := add(outbound.Tag, i, outbound.Options); err != nil {
			return err
		}
	}
	for i, endpoint := range options.Endpoints {
		if err := add(endpoint.Tag, i, endpoint.Options); err != nil {
			return err
		}
	}
	state := make(map[string]uint8)
	var visit func(string, []string) error
	visit = func(tag string, path []string) error {
		if state[tag] == 2 {
			return nil
		}
		if state[tag] == 1 {
			return E.New("circular outbound dependency: ", strings.Join(append(path, tag), " -> "))
		}
		state[tag] = 1
		for _, dependency := range nodes[tag] {
			if _, exists := nodes[dependency]; !exists {
				return fmt.Errorf("outbound[%s]: dependency not found: %s", tag, dependency)
			}
			if err := visit(dependency, append(path, tag)); err != nil {
				return err
			}
		}
		state[tag] = 2
		return nil
	}
	for _, tag := range order {
		if err := visit(tag, nil); err != nil {
			return err
		}
	}
	return nil
}
