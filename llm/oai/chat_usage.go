package oai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"shelley.exe.dev/llm"
)

type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	Details          struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

func (s *Service) toLLMChatUsage(usage chatCompletionUsage, headers http.Header) llm.Usage {
	total := uint64(max(usage.PromptTokens, 0))
	read := min(uint64(max(usage.Details.CachedTokens, 0)), total)
	write := min(uint64(max(usage.Details.CacheWriteTokens, 0)), total-read)
	return llm.Usage{
		InputTokens:              total - read - write,
		CacheReadInputTokens:     read,
		CacheCreationInputTokens: write,
		OutputTokens:             uint64(max(usage.CompletionTokens, 0)),
		CostUSD:                  llm.CostUSDFromResponse(headers),
	}
}

// The SDK's Chat Completions usage type omits OpenRouter's cache_write_tokens.
type chatUsageClient struct {
	base  *http.Client
	usage *chatCompletionUsage
}

func (client chatUsageClient) Do(request *http.Request) (*http.Response, error) {
	response, err := client.base.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		return response, err
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	var metadata struct {
		Usage chatCompletionUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, err
	}
	*client.usage = metadata.Usage
	return response, nil
}
