package modelroute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"

	"github.com/tidwall/gjson"
)

// Command Code's public model list names the endpoints that serve each model:
//
//	{"id":"deepseek/deepseek-v4-flash","supported_endpoints":["/chat/completions","/responses"]}
//	{"id":"claude-sonnet-4-6","supported_endpoints":["/messages"]}
//
// Routing uses it to forward a request unchanged whenever the model supports
// the inbound protocol. Lists are cached per channel and URL, so a failing key
// only backs off its own channel, and stale lists refresh in the background.
// Until a list first loads, requests wait at most catalogFirstLoadWait, sharing
// the deadline of that refresh, and then fall back to the built-in rules. A
// failed refresh keeps the previous list and backs off for catalogRetryBackoff.

const (
	catalogTTL           = 30 * time.Minute
	catalogRetryBackoff  = 5 * time.Minute
	catalogFetchTimeout  = 15 * time.Second
	catalogFirstLoadWait = 2 * time.Second
	catalogMaxBodyBytes  = 2 << 20
)

type catalogEntry struct {
	// models maps lower-case model IDs to their protocols in upstream order.
	models     map[string][]string
	fetchedAt  time.Time
	retryAt    time.Time
	refreshing bool
	// done is closed when the running refresh ends.
	done chan struct{}
	// firstLoadDeadline bounds how long requests wait for the first load.
	firstLoadDeadline time.Time
}

type modelCatalog struct {
	mu      sync.Mutex
	entries map[string]*catalogEntry
}

var catalogs modelCatalog

func (c *modelCatalog) entryLocked(key string) *catalogEntry {
	if c.entries == nil {
		c.entries = make(map[string]*catalogEntry)
	}
	entry := c.entries[key]
	if entry == nil {
		entry = &catalogEntry{}
		c.entries[key] = entry
	}
	return entry
}

// lookup returns the protocols the catalog under key lists for model, or nil
// when the catalog is not loaded or does not list the model. A missing or stale
// catalog that is neither refreshing nor backing off starts refresh in a new
// goroutine; refresh must end by calling store.
func (c *modelCatalog) lookup(ctx context.Context, key, model string, now time.Time, refresh func()) []string {
	if ctx.Err() != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entryLocked(key)
	stale := entry.models == nil || now.Sub(entry.fetchedAt) >= catalogTTL
	if stale && !entry.refreshing && !now.Before(entry.retryAt) {
		entry.refreshing = true
		entry.done = make(chan struct{})
		if entry.models == nil {
			entry.firstLoadDeadline = now.Add(catalogFirstLoadWait)
		}
		go refresh()
	}
	if entry.models == nil && entry.refreshing && now.Before(entry.firstLoadDeadline) {
		done := entry.done
		timer := time.NewTimer(entry.firstLoadDeadline.Sub(now))
		c.mu.Unlock()
		select {
		case <-done:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		c.mu.Lock()
	}
	if entry.models == nil || ctx.Err() != nil {
		return nil
	}
	return entry.models[strings.ToLower(strings.TrimSpace(model))]
}

// store records the result of a refresh. A failure keeps the previous catalog
// and backs off.
func (c *modelCatalog) store(key string, models map[string][]string, err error, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entryLocked(key)
	entry.refreshing = false
	if entry.done != nil {
		close(entry.done)
		entry.done = nil
	}
	if err != nil {
		entry.retryAt = now.Add(catalogRetryBackoff)
		return
	}
	entry.models = models
	entry.fetchedAt = now
	entry.retryAt = time.Time{}
}

func fetchCatalog(modelsURL string, header http.Header, setting dto.ChannelSettings) (map[string][]string, error) {
	client, err := service.GetHttpClientWithProxySettings(setting.Proxy, setting)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), catalogFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header = header
	// Match relay transport: a catalog request must never carry credentials
	// through redirects, including same-host HTTPS downgrades.
	catalogClient := *client
	catalogClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := catalogClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("model list returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, catalogMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > catalogMaxBodyBytes {
		return nil, fmt.Errorf("model list exceeds %d bytes", catalogMaxBodyBytes)
	}
	return parseCatalog(body)
}

// parseCatalog reads a model list shaped as {"data":[...]}, {"models":[...]} or
// a bare array. A list where no model names a known endpoint is an error: the
// upstream does not publish supported_endpoints.
func parseCatalog(body []byte) (map[string][]string, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("invalid model protocol catalog JSON")
	}
	entries := gjson.GetBytes(body, "data")
	if !entries.IsArray() {
		entries = gjson.GetBytes(body, "models")
	}
	if !entries.IsArray() {
		entries = gjson.ParseBytes(body)
	}
	models := make(map[string][]string)
	for _, entry := range entries.Array() {
		id := strings.ToLower(strings.TrimSpace(entry.Get("id").String()))
		if id == "" {
			continue
		}
		var protocols []string
		for _, endpoint := range entry.Get("supported_endpoints").Array() {
			path := strings.ToLower(strings.TrimRight(strings.TrimSpace(endpoint.String()), "/"))
			protocol := ""
			switch {
			case strings.HasSuffix(path, "/chat/completions"):
				protocol = dto.ModelProtocolChatCompletions
			case strings.HasSuffix(path, "/responses"):
				protocol = dto.ModelProtocolResponses
			case strings.HasSuffix(path, "/messages"):
				protocol = dto.ModelProtocolAnthropic
			}
			if protocol != "" && !slices.Contains(protocols, protocol) {
				protocols = append(protocols, protocol)
			}
		}
		if len(protocols) > 0 {
			models[id] = protocols
		}
	}
	if len(models) == 0 {
		return nil, errors.New("model list has no supported_endpoints")
	}
	return models, nil
}
