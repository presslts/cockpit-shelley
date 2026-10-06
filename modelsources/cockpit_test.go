package modelsources

import (
	"net/http"
	"testing"

	"shelley.exe.dev/models"
)

func TestCockpitModelIDsSurviveIntegrationReplacement(t *testing.T) {
	t.Setenv("PRESSLTS_MODEL_SOCKET", "/run/model.sock")
	const id = "anthropic/claude-haiku-4-5-20251001"
	for _, name := range []string{"presslts-first-key", "presslts-replacement-key"} {
		integration := &LLMIntegrationConfig{Name: name, Host: name + ".int.exe.xyz", URL: "https://" + name + ".int.exe.xyz", Models: []IntegrationModel{{ID: id, NativeID: "claude-haiku-4-5-20251001", Provider: "anthropic", APIs: []string{"anthropic_messages"}}}}
		built := Build(models.All(), []Source{LLMIntegration(integration, "@"+name)}, &http.Client{}, nil)
		if len(built) != 1 || built[0].ID != id {
			t.Fatalf("key replacement changed the conversation model ID: %+v", built)
		}
	}
}
