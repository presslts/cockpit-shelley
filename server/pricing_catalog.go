package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/models/modelsdev"
)

const pricingCatalogURL = "https://exe.dev/llm-gateway-models.json"

type modelCostRequest struct {
	Model string `json:"model"`
	URL   string `json:"url"`
}

// The cockpit uses exe.dev's current catalog, never the embedded snapshot.
// A failed refresh is visible to the UI instead of silently using stale rates.
type pricingCatalog struct {
	mu        sync.Mutex
	client    *http.Client
	costs     map[string]modelsdev.Cost
	updatedAt time.Time
	expiresAt time.Time
	err       error
}

func (catalog *pricingCatalog) load(ctx context.Context) (map[string]modelsdev.Cost, time.Time, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	if time.Now().Before(catalog.expiresAt) {
		return catalog.costs, catalog.updatedAt, catalog.err
	}
	costs, err := catalog.fetch(ctx)
	if err != nil && ctx.Err() != nil {
		// Closing one popup must not cache its cancellation for other users.
		return nil, time.Time{}, err
	}
	catalog.costs, catalog.err = costs, err
	if err != nil {
		catalog.updatedAt = time.Time{}
		catalog.expiresAt = time.Now().Add(30 * time.Second)
	} else {
		catalog.updatedAt = time.Now().UTC()
		catalog.expiresAt = catalog.updatedAt.Add(5 * time.Minute)
	}
	return catalog.costs, catalog.updatedAt, catalog.err
}

func (catalog *pricingCatalog) fetch(ctx context.Context) (map[string]modelsdev.Cost, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pricingCatalogURL, nil)
	if err != nil {
		return nil, err
	}
	client := catalog.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pricing catalog returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4<<20 {
		return nil, fmt.Errorf("pricing catalog exceeds size limit")
	}
	var data struct {
		SchemaVersion int `json:"schemaVersion"`
		Providers     []struct {
			ID     string `json:"id"`
			Models []struct {
				ID    string   `json:"id"`
				Input []string `json:"input"`
				Cost  *struct {
					Input      *float64 `json:"input"`
					Output     *float64 `json:"output"`
					CacheRead  *float64 `json:"cacheRead"`
					CacheWrite *float64 `json:"cacheWrite"`
				} `json:"cost"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if data.SchemaVersion != 1 || len(data.Providers) == 0 {
		return nil, fmt.Errorf("unsupported pricing catalog")
	}
	valid := func(rate *float64) bool {
		return rate != nil && *rate >= 0 && !math.IsNaN(*rate) && !math.IsInf(*rate, 0)
	}
	costs := make(map[string]modelsdev.Cost)
	for _, provider := range data.Providers {
		for _, model := range provider.Models {
			cost := model.Cost
			if provider.ID == "" || model.ID == "" || cost == nil ||
				!valid(cost.Input) || !valid(cost.Output) || !valid(cost.CacheRead) || !valid(cost.CacheWrite) {
				continue
			}
			for _, input := range model.Input {
				if input == "text" {
					costs[provider.ID+"/"+model.ID] = modelsdev.Cost{Input: *cost.Input, Output: *cost.Output, CacheRead: *cost.CacheRead, CacheWrite: *cost.CacheWrite}
					break
				}
			}
		}
	}
	return costs, nil
}

var modelDateSuffix = regexp.MustCompile(`-20[0-9]{2}-[0-9]{2}-[0-9]{2}$|-20[0-9]{6}$`)

func catalogCost(costs map[string]modelsdev.Cost, request modelCostRequest) *modelsdev.Cost {
	endpoint, err := url.Parse(request.URL)
	if err != nil || endpoint.Scheme != "https" || (!strings.HasSuffix(endpoint.Hostname(), ".int.exe.xyz") && !strings.HasSuffix(endpoint.Hostname(), ".team.exe.xyz")) {
		return nil
	}
	provider := ""
	switch {
	case strings.HasSuffix(endpoint.Path, "/v1/responses"):
		provider = "openai"
	case strings.HasSuffix(endpoint.Path, "/v1/messages"):
		provider = "anthropic"
	default:
		// Chat-compatible custom providers (including OpenRouter) have their
		// own rates. Never assign them another provider's catalog prices.
		return nil
	}
	model := strings.TrimPrefix(request.Model, provider+"/")
	cost, found := costs[provider+"/"+model]
	if !found {
		cost, found = costs[provider+"/"+modelDateSuffix.ReplaceAllString(model, "")]
	}
	if !found {
		return nil
	}
	return &cost
}

func (s *Server) modelCosts(ctx context.Context, requests []modelCostRequest) (map[string]*modelsdev.Cost, string, time.Time, error) {
	costs := make(map[string]*modelsdev.Cost, len(requests))
	if s.pricingCatalog != nil {
		if len(requests) == 0 {
			return costs, "exe.dev", time.Time{}, nil
		}
		catalog, updatedAt, err := s.pricingCatalog.load(ctx)
		if err != nil {
			return nil, "exe.dev", time.Time{}, err
		}
		for _, request := range requests {
			cost := catalogCost(catalog, request)
			if previous, seen := costs[request.Model]; seen && (previous == nil || cost == nil ||
				previous.Input != cost.Input || previous.Output != cost.Output || previous.CacheRead != cost.CacheRead || previous.CacheWrite != cost.CacheWrite) {
				// The response is keyed by model name. Ambiguous provider rates
				// must stay unknown rather than silently choosing one endpoint.
				cost = nil
			}
			costs[request.Model] = cost
		}
		return costs, "exe.dev", updatedAt, nil
	}
	for _, request := range requests {
		if cost, found := modelsdev.LookupCost(request.URL, request.Model); found {
			costs[request.Model] = &cost
		} else {
			costs[request.Model] = nil
		}
	}
	return costs, "models.dev snapshot", time.Time{}, nil
}
