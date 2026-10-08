package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCockpitPricingTransportUsesOnlyFixedBrokerRoute(t *testing.T) {
	// macOS Unix sockets have a short path limit; testing.T's long names
	// under TMPDIR can exceed it.
	directory, err := os.MkdirTemp("/tmp", "shelley-broker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "model.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "exe.dev" || r.URL.Path != "/llm-gateway-models.json" || r.Method != "GET" {
			t.Errorf("unexpected broker request %s %s%s", r.Method, r.Host, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"schemaVersion":1}`)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	base := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(base.CloseIdleConnections)
	transport := cockpitTransport{transport: base}
	request := httptest.NewRequest("GET", "https://exe.dev/llm-gateway-models.json", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || request.URL.Scheme != "https" {
		t.Fatal("catalog request failed or original request mutated")
	}
	for _, tc := range []struct{ method, endpoint string }{
		{"POST", "https://exe.dev/llm-gateway-models.json"},
		{"GET", "https://exe.dev/llm-gateway-models.json?url=private"},
		{"GET", "https://exe.dev/user/shelley"},
		{"GET", "http://exe.dev/llm-gateway-models.json"},
		{"GET", "https://other.example/llm-gateway-models.json"},
	} {
		if _, err := transport.RoundTrip(httptest.NewRequest(tc.method, tc.endpoint, nil)); err == nil {
			t.Errorf("allowed %s %s", tc.method, tc.endpoint)
		}
	}
}
