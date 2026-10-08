package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type pricingTransport func(*http.Request) (*http.Response, error)

func (transport pricingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

const pricingFixture = `{"schemaVersion":1,"providers":[{"id":"openai","models":[
{"id":"gpt-future","input":["text"],"cost":{"input":2,"output":10,"cacheRead":0.1,"cacheWrite":2.5}},
{"id":"free","input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}},
{"id":"missing-rate","input":["text"],"cost":{"input":2,"output":10}},
{"id":"negative","input":["text"],"cost":{"input":-2,"output":10,"cacheRead":0,"cacheWrite":0}}
]},{"id":"anthropic","models":[{"id":"claude-future","input":["text"],"cost":{"input":3,"output":15,"cacheRead":0.3,"cacheWrite":3.75}}]}]}`

func testPricingCatalog(body *string, calls *atomic.Int32) *pricingCatalog {
	return &pricingCatalog{client: &http.Client{Transport: pricingTransport(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(*body))}, nil
	})}}
}

func TestLivePricingCatalogRefreshAndFailure(t *testing.T) {
	t.Parallel()
	body := pricingFixture
	var calls atomic.Int32
	catalog := testPricingCatalog(&body, &calls)
	requests := []modelCostRequest{{Model: "gpt-future", URL: "https://presslts-key.int.exe.xyz/v1/responses"}}
	srv := &Server{pricingCatalog: catalog}
	costs, source, updatedAt, err := srv.modelCosts(t.Context(), requests)
	if err != nil || source != "exe.dev" || updatedAt.IsZero() || costs["gpt-future"].Input != 2 {
		t.Fatalf("initial costs %v, %s, %s, %v", costs, source, updatedAt, err)
	}
	// Concurrent graph/subagent requests reuse a single fetch.
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, err := catalog.load(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("cached fetches %d, want 1", calls.Load())
	}
	body = strings.ReplaceAll(body, `"input":2`, `"input":4`)
	catalog.expiresAt = time.Time{}
	costs, _, _, err = srv.modelCosts(t.Context(), requests)
	if err != nil || costs["gpt-future"].Input != 4 {
		t.Fatalf("refreshed costs %v: %v", costs, err)
	}
	// A malformed refresh must not return either the last success or the
	// embedded snapshot. Requests retry after the bounded error cache expires.
	body = "malformed"
	catalog.expiresAt = time.Time{}
	if _, _, _, err = srv.modelCosts(t.Context(), requests); err == nil {
		t.Fatal("invalid refresh succeeded")
	}
	if _, _, _, err = srv.modelCosts(t.Context(), requests); err == nil {
		t.Fatal("cached error disappeared")
	}
	if calls.Load() != 3 {
		t.Fatalf("error cache fetches %d, want 3", calls.Load())
	}
	body = pricingFixture
	catalog.expiresAt = time.Time{}
	if _, _, _, err = srv.modelCosts(t.Context(), requests); err != nil {
		t.Fatal(err)
	}
}

func TestLivePricingCatalogProviderIdentityAndUnknownPrices(t *testing.T) {
	t.Parallel()
	body := pricingFixture
	var calls atomic.Int32
	catalog, _, err := testPricingCatalog(&body, &calls).load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model, endpoint string
		want            float64
		known           bool
	}{
		{"gpt-future", "https://presslts-key.int.exe.xyz/v1/responses", 2, true},
		{"gpt-future", "https://shared-key.team.exe.xyz/v1/responses", 2, true},
		{"openai/gpt-future", "https://presslts-key.int.exe.xyz/v1/responses", 2, true},
		{"gpt-future-2026-10-08", "https://presslts-key.int.exe.xyz/v1/responses", 2, true},
		{"claude-future-20261008", "https://presslts-key.int.exe.xyz/v1/messages", 3, true},
		{"free", "https://presslts-key.int.exe.xyz/v1/responses", 0, true},
		{"gpt-future", "https://presslts-key.int.exe.xyz/v1/messages", 0, false},
		{"openai/gpt-future", "https://presslts-key.int.exe.xyz/v1/chat/completions", 0, false},
		{"gpt-future", "https://custom.example/v1/responses", 0, false},
		{"gpt-missing", "https://presslts-key.int.exe.xyz/v1/responses", 0, false},
		{"missing-rate", "https://presslts-key.int.exe.xyz/v1/responses", 0, false},
		{"negative", "https://presslts-key.int.exe.xyz/v1/responses", 0, false},
	} {
		cost := catalogCost(catalog, modelCostRequest{Model: tc.model, URL: tc.endpoint})
		if (cost != nil) != tc.known || (cost != nil && cost.Input != tc.want) {
			t.Errorf("%s at %s = %+v", tc.model, tc.endpoint, cost)
		}
	}
}

func TestCockpitModelCostsAPIUsesLiveRatesAndExposesOutage(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	body := pricingFixture
	var calls atomic.Int32
	srv.pricingCatalog = testPricingCatalog(&body, &calls)
	requestBody := `{"models":[{"model":"gpt-future","url":"https://presslts-key.int.exe.xyz/v1/responses"},{"model":"gpt-missing","url":"https://presslts-key.int.exe.xyz/v1/responses"}]}`
	response := httptest.NewRecorder()
	srv.handleModelCosts(response, httptest.NewRequest("POST", "/api/model-costs", strings.NewReader(requestBody)))
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var result struct {
		Costs     map[string]json.RawMessage
		Source    string
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Source != "exe.dev" || result.UpdatedAt == "" || string(result.Costs["gpt-missing"]) != "null" {
		t.Fatalf("response: %s", response.Body.String())
	}
	body = `{"schemaVersion":2,"providers":[]}`
	srv.pricingCatalog.expiresAt = time.Time{}
	response = httptest.NewRecorder()
	srv.handleModelCosts(response, httptest.NewRequest("POST", "/api/model-costs", strings.NewReader(requestBody)))
	if response.Code != 503 {
		t.Fatalf("outage status %d", response.Code)
	}
}

func TestLivePricingCatalogHTTPFailuresAndCancellation(t *testing.T) {
	t.Parallel()
	for _, status := range []int{302, 403, 429, 503} {
		catalog := &pricingCatalog{client: &http.Client{Transport: pricingTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}}
		if _, _, err := catalog.load(t.Context()); err == nil {
			t.Errorf("HTTP %d accepted", status)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	catalog := &pricingCatalog{client: &http.Client{Transport: pricingTransport(func(request *http.Request) (*http.Response, error) { return nil, request.Context().Err() })}}
	if _, _, err := catalog.load(ctx); err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
}

func TestLivePricingCatalogAmbiguousProviderRatesRemainUnknown(t *testing.T) {
	t.Parallel()
	body := strings.ReplaceAll(pricingFixture, `"claude-future"`, `"gpt-future"`)
	var calls atomic.Int32
	srv := &Server{pricingCatalog: testPricingCatalog(&body, &calls)}
	costs, _, _, err := srv.modelCosts(t.Context(), []modelCostRequest{
		{Model: "gpt-future", URL: "https://presslts-openai.int.exe.xyz/v1/responses"},
		{Model: "gpt-future", URL: "https://presslts-anthropic.int.exe.xyz/v1/messages"},
	})
	if err != nil || costs["gpt-future"] != nil {
		t.Fatalf("ambiguous rates = %v, error %v", costs, err)
	}
}

func TestLivePricingCancellationDoesNotPoisonCache(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	catalog := &pricingCatalog{client: &http.Client{Transport: pricingTransport(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(pricingFixture))}, nil
	})}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := catalog.load(ctx); err == nil {
		t.Fatal("cancelled fetch succeeded")
	}
	if _, _, err := catalog.load(t.Context()); err != nil || calls.Load() != 2 {
		t.Fatalf("fresh request failed after cancellation: %v, %d fetches", err, calls.Load())
	}
}
