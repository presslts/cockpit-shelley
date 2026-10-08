package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
)

type cockpitTransport struct{ transport *http.Transport }

func (transport cockpitTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	pricing := request.Method == http.MethodGet && request.URL.Host == "exe.dev" &&
		request.URL.Path == "/llm-gateway-models.json" && request.URL.RawQuery == ""
	if request.URL.Scheme != "https" || (request.URL.Host != "reflection.int.exe.xyz" &&
		(!strings.HasPrefix(request.URL.Host, "presslts-") || !strings.HasSuffix(request.URL.Host, ".int.exe.xyz")) && !pricing) {
		return nil, errors.New("network access outside model broker is disabled")
	}
	request = request.Clone(request.Context())
	request.URL.Scheme = "http"
	return transport.transport.RoundTrip(request)
}

func configureCockpitTransport() {
	socket := os.Getenv("PRESSLTS_MODEL_SOCKET")
	if socket == "" {
		return
	}
	http.DefaultTransport = cockpitTransport{transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
}
