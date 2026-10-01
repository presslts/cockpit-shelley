package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCockpitNormalizesConversationCWD(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRESSLTS_PLUGIN_ROOT", root)
	handler := cockpitHandler(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			CWD string `json:"cwd"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.CWD != root {
			t.Errorf("cwd %q, expected plugin root", payload.CWD)
		}
		writer.WriteHeader(204)
	}))
	for _, body := range []string{`{}`, `{"cwd":"."}`, `{"cwd":"nested/.."}`} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/conversations/draft", strings.NewReader(body)))
		if response.Code != 204 {
			t.Errorf("status %d", response.Code)
		}
	}
}

func TestCockpitRejectsTraversalSymlinksAndCrossPluginCWD(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRESSLTS_PLUGIN_ROOT", root)
	handler := cockpitHandler(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(204) }))
	for _, path := range []string{"../sibling", outside, root + "/../sibling", root + "/escape/file", root + "/escape/new/file"} {
		request := httptest.NewRequest("POST", "/api/conversations/new", strings.NewReader(`{"cwd":"`+path+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 403 {
			t.Errorf("path %s: status %d", path, response.Code)
		}
	}
	if !cockpitPath(root, root+"/nested/new.php") {
		t.Fatal("nested path rejected")
	}
}

func TestCockpitChecksEveryConversationRouteBeforeDispatch(t *testing.T) {
	server, database, _ := newTestServer(t)
	identifier, _ := seedForkConversation(t, database)
	t.Setenv("PRESSLTS_PLUGIN_ROOT", t.TempDir())
	handler := server.cockpitConversationHandler(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(204) }))
	for _, suffix := range []string{"", "/stream", "/archive", "/delete", "/fork", "/chat", "/cwd", "/subagents"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/conversation/foreign"+suffix, nil))
		if response.Code != 404 {
			t.Errorf("foreign%s: %d", suffix, response.Code)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/conversation/"+identifier+suffix, nil))
		if response.Code != 204 {
			t.Errorf("own%s: %d", suffix, response.Code)
		}
	}
}
