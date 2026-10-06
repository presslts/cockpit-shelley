package oai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/llmhttp"
)

func TestOpenRouterPreservesCacheUsageForStreamingAndNonStreamingCalls(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if payload["session_id"] == nil || payload["session_id"] == "test-conversation" || payload["cache_control"] == nil {
					t.Error("missing opaque session ID or cache hint")
				}
				if _, exists := payload["stream_options"]; exists {
					t.Error("OpenRouter usage is automatic; deprecated stream_options should not be sent")
				}
				usage := `{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":30}}`
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":%s}\n\ndata: [DONE]\n\n", usage)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":%s}`, usage)
				}
			}))
			defer server.Close()
			service := &Service{APIKey: "implicit", Model: modelForTest("anthropic/claude-haiku-4.5"), ModelURL: server.URL, ProviderName: "openrouter", HTTPC: llmhttp.NewClient(nil)}
			request := &llm.Request{Messages: []llm.Message{{Role: llm.MessageRoleUser}}}
			if streaming {
				request.OnStream = func(llm.StreamDelta) {}
			}
			ctx := llmhttp.WithConversationID(llmhttp.WithProvider(t.Context(), "openrouter"), "test-conversation")
			response, err := service.Do(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			u := response.Usage
			if u.InputTokens != 10 || u.CacheReadInputTokens != 60 || u.CacheCreationInputTokens != 30 || u.OutputTokens != 5 || u.TotalInputTokens() != 100 {
				t.Fatalf("incorrect cache accounting: %+v", u)
			}
		})
	}
}
