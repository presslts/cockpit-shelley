package oai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/llm"
)

func TestResponsesNonReasoningModelOmitsReasoningDespiteDefaults(t *testing.T) {
	for _, level := range []llm.ThinkingLevel{llm.ThinkingLevelDefault, llm.ThinkingLevelHigh} {
		t.Run(level.Name(), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request responsesRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Reasoning != nil {
					t.Errorf("non-reasoning model received reasoning: %+v", request.Reasoning)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(responsesResponse{ID: "r", Status: "completed", Output: []responsesOutputItem{{Type: "message", Role: "assistant", Content: []responsesContent{{Type: "output_text", Text: "hello"}}}}})
			}))
			defer server.Close()
			s := &ResponsesService{Model: Model{ModelName: "gpt-4.1-mini"}, ModelURL: server.URL, ThinkingLevel: llm.ThinkingLevelMedium, ReasoningEffort: "high"}
			req := &llm.Request{ThinkingLevel: level, Messages: []llm.Message{{Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}}}}}
			if _, err := s.Do(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if req.ThinkingLevel != level {
				t.Fatal("mutated the caller's request")
			}
		})
	}
}
