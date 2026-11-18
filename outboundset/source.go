package outboundset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service/filemanager"
)

const maxSourceSize = 16 << 20

type loader struct {
	ctx       context.Context
	log       logger.ContextLogger
	client    *http.Client
	cachePath string
	cacheID   string
	options   option.Options
	download  DownloadFunc
	loaded    map[string][]option.Outbound
	cacheOnly bool
}

// DownloadFunc downloads a source using an isolated instance of the supplied
// bootstrap configuration. It must close that instance before returning.
type DownloadFunc func(context.Context, string, option.HTTPClientOptions, option.Options) ([]byte, error)

var errUncached = errors.New("outbound-set has no bootstrap content")

type cacheEntry struct {
	path    string
	content []byte
	refresh bool
}

func newLoader(ctx context.Context, log logger.ContextLogger, experimental *option.ExperimentalOptions) *loader {
	l := &loader{ctx: ctx, log: log}
	// Bootstrap downloads cannot depend on outbounds that have not been
	// constructed yet. Use a dedicated direct transport, with no environment
	// proxy or implicit route through the default outbound.
	l.client = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
	if experimental != nil && experimental.CacheFile != nil && experimental.CacheFile.Enabled {
		path := experimental.CacheFile.Path
		if path == "" {
			path = "cache.db"
		}
		l.cachePath = filemanager.BasePath(ctx, path+".outbound-set")
		l.cacheID = experimental.CacheFile.CacheID
	}
	return l
}

func (l *loader) close() {
	l.client.CloseIdleConnections()
}

func (l *loader) load(set option.OutboundSet, tag string) ([]option.Outbound, cacheEntry, error) {
	if err := l.ctx.Err(); err != nil {
		return nil, cacheEntry{}, err
	}
	switch set.Type {
	case "", C.OutboundSetTypeInline:
		outbounds, err := decodeMembers(l.ctx, set, tag, set.InlineOptions.Outbounds)
		return outbounds, cacheEntry{}, err
	case C.OutboundSetTypeLocal:
		path := strings.ReplaceAll(set.LocalOptions.Path, C.OutboundSetTagPlaceholder, tag)
		content, err := l.readFile(filemanager.BasePath(l.ctx, path))
		if err != nil {
			return nil, cacheEntry{}, err
		}
		outbounds, err := l.decode(set, tag, content)
		return outbounds, cacheEntry{}, err
	case C.OutboundSetTypeRemote:
		return l.loadRemote(set, tag)
	default:
		return nil, cacheEntry{}, E.New("unknown outbound-set type: ", set.Type)
	}
}

func (l *loader) decode(set option.OutboundSet, tag string, content []byte) ([]option.Outbound, error) {
	var source option.PlainOutboundSet
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, E.New("unexpected data after outbound-set document")
	}
	if source.Version != C.OutboundSetVersion1 {
		return nil, E.New("unsupported outbound-set version: ", source.Version, " (expected 1)")
	}
	return decodeMembers(l.ctx, set, tag, source.Outbounds)
}

func (l *loader) loadRemote(set option.OutboundSet, tag string) ([]option.Outbound, cacheEntry, error) {
	url := strings.ReplaceAll(set.RemoteOptions.URL, C.OutboundSetTagPlaceholder, tag)
	cachePath := l.sourceCachePath(tag, url)
	clientOptions, err := ResolveHTTPClient(l.options, set.RemoteOptions.HTTPClient)
	if err != nil {
		return nil, cacheEntry{}, err
	}
	paths := []string{cachePath}
	if set.RemoteOptions.InitialPath != "" {
		path := strings.ReplaceAll(set.RemoteOptions.InitialPath, C.OutboundSetTagPlaceholder, tag)
		paths = append(paths, filemanager.BasePath(l.ctx, path))
	}
	for _, path := range paths {
		if err := l.ctx.Err(); err != nil {
			return nil, cacheEntry{}, err
		}
		if path == "" {
			continue
		}
		content, err := l.readFile(path)
		if err == nil {
			outbounds, err := l.decode(set, tag, content)
			if err == nil {
				return outbounds, cacheEntry{refresh: true}, nil
			}
		}
	}
	if l.cacheOnly {
		return nil, cacheEntry{}, errUncached
	}
	var content []byte
	if l.download != nil {
		var bootstrap option.Options
		bootstrap, err = bootstrapOptions(l.bootstrapOptions(), clientOptions)
		if err == nil {
			content, err = l.download(l.ctx, url, clientOptions, bootstrap)
		}
	} else if !clientOptions.IsEmpty() {
		err = E.New("http_client requires an initialized download transport")
	} else {
		content, err = l.fetch(url)
	}
	if err != nil {
		return nil, cacheEntry{}, E.Cause(err, "initial download failed; provide a usable cache or initial_path for bootstrap")
	}
	outbounds, err := l.decode(set, tag, content)
	if err != nil {
		return nil, cacheEntry{}, err
	}
	return outbounds, cacheEntry{path: cachePath, content: content}, nil
}

// Only already-loaded sets can satisfy a bootstrap dependency. Unavailable
// group references remain marked, so the bootstrap builder can reject cycles.
func (l *loader) bootstrapOptions() option.Options {
	result := l.options
	result.Outbounds = slices.Clone(result.Outbounds)
	expand := func(explicit, sets []string) ([]string, []string) {
		tags := slices.Clone(explicit)
		var missing []string
		for _, tag := range sets {
			if members, found := l.loaded[tag]; found {
				for _, value := range members {
					if !slices.Contains(tags, value.Tag) {
						tags = append(tags, value.Tag)
					}
				}
			} else {
				missing = append(missing, tag)
			}
		}
		return tags, missing
	}
	for i, value := range result.Outbounds {
		switch group := value.Options.(type) {
		case *option.SelectorOutboundOptions:
			clone := *group
			clone.Outbounds, clone.OutboundSet = expand(group.Outbounds, group.OutboundSet)
			result.Outbounds[i].Options = &clone
		case *option.URLTestOutboundOptions:
			clone := *group
			clone.Outbounds, clone.OutboundSet = expand(group.Outbounds, group.OutboundSet)
			result.Outbounds[i].Options = &clone
		case *option.LoadBalanceOutboundOptions:
			clone := *group
			clone.Outbounds, clone.OutboundSet = expand(group.Outbounds, group.OutboundSet)
			result.Outbounds[i].Options = &clone
		}
	}
	for _, set := range result.OutboundSet {
		for _, tag := range set.Tag {
			result.Outbounds = append(result.Outbounds, l.loaded[tag]...)
		}
	}
	return result
}

func (l *loader) sourceCachePath(tag, url string) string {
	if l.cachePath != "" {
		// Length-delimited JSON avoids ambiguous concatenations of identities.
		identity, _ := json.Marshal([]string{l.cacheID, tag, url})
		hash := sha256.Sum256(identity)
		return filepath.Join(l.cachePath, hex.EncodeToString(hash[:])+".json")
	}
	return ""
}

// ResolveHTTPClient uses an explicit client, the configured default, or direct
// dialing when no HTTP client is configured. Bootstrap never silently changes it.
func ResolveHTTPClient(options option.Options, client *option.HTTPClientOptions) (option.HTTPClientOptions, error) {
	var result option.HTTPClientOptions
	if client != nil {
		result = *client
	}
	if client == nil || client.IsEmpty() {
		if options.Route != nil {
			result.Tag = options.Route.DefaultHTTPClient
		}
		if result.Tag == "" && len(options.HTTPClients) > 0 {
			result.Tag = options.HTTPClients[0].Tag
		}
	}
	if result.Tag == "" {
		return result, nil
	}
	for _, definition := range options.HTTPClients {
		if definition.Tag == result.Tag {
			return definition.Options(), nil
		}
	}
	return option.HTTPClientOptions{}, E.New("http_client not found: ", result.Tag)
}

func (l *loader) fetch(url string) ([]byte, error) {
	return Download(l.ctx, l.client, url)
}

func Download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, E.New("unexpected HTTP status: ", response.Status)
	}
	return readSource(response.Body)
}

func (l *loader) readFile(path string) ([]byte, error) {
	file, err := filemanager.Open(l.ctx, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readSource(file)
}

func readSource(reader io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxSourceSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxSourceSize {
		return nil, E.New("outbound-set source exceeds 16 MiB")
	}
	return content, nil
}

func (l *loader) save(entry cacheEntry) error {
	if err := filemanager.MkdirAll(l.ctx, filepath.Dir(entry.path), 0o700); err != nil {
		return err
	}
	// Keep the temporary file on the same filesystem for atomic replacement.
	file, err := os.CreateTemp(filepath.Dir(entry.path), ".outbound-set-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(entry.content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = filemanager.Chown(l.ctx, file.Name()); err != nil {
		return err
	}
	return filemanager.Rename(l.ctx, file.Name(), entry.path)
}
