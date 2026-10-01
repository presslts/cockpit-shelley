package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/db/generated"
)

func (s *Server) cockpitConversationHandler(next http.Handler) http.Handler {
	if os.Getenv("PRESSLTS_PLUGIN_ROOT") == "" {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identifier := ""
		for _, prefix := range []string{"/api/conversation/", "/export/"} {
			if strings.HasPrefix(request.URL.Path, prefix) {
				identifier = strings.Split(strings.TrimPrefix(request.URL.Path, prefix), "/")[0]
			}
		}
		if identifier != "" {
			err := s.db.Queries(request.Context(), func(queries *generated.Queries) error {
				_, err := queries.GetConversation(request.Context(), identifier)
				return err
			})
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(writer, request)
				return
			}
			if err != nil {
				http.Error(writer, "conversation unavailable", 503)
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func cockpitPath(root, value string) bool {
	if value == "" {
		return true
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	value = filepath.Clean(value)
	if value != root && !strings.HasPrefix(value, root+"/") {
		return false
	}
	for {
		resolved, err := filepath.EvalSymlinks(value)
		if err == nil {
			return resolved == root || strings.HasPrefix(resolved, root+"/")
		}
		if !os.IsNotExist(err) || value == root {
			return false
		}
		value = filepath.Dir(value)
	}
}

func cockpitHandler(next http.Handler) http.Handler {
	root := os.Getenv("PRESSLTS_PLUGIN_ROOT")
	if root == "" {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, key := range []string{"path", "cwd", "dir", "directory", "repo", "repo_path", "base_path", "working_dir"} {
			for _, value := range request.URL.Query()[key] {
				if !cockpitPath(root, value) {
					http.Error(writer, "path outside plugin", 403)
					return
				}
			}
		}
		if request.Body != nil && request.Method != "GET" && request.Method != "HEAD" && !strings.HasPrefix(request.URL.Path, "/api/upload") {
			body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 32<<20))
			if err != nil {
				http.Error(writer, "request too large", 413)
				return
			}
			var payload map[string]json.RawMessage
			if json.Unmarshal(body, &payload) == nil {
				for _, key := range []string{"path", "cwd", "dir", "directory", "repo", "repo_path", "base_path", "working_dir"} {
					var value string
					if raw, ok := payload[key]; ok && json.Unmarshal(raw, &value) == nil && !cockpitPath(root, value) {
						http.Error(writer, "path outside plugin", 403)
						return
					}
				}
				if raw, ok := payload["cwd"]; ok {
					var cwd string
					if json.Unmarshal(raw, &cwd) == nil {
						if !filepath.IsAbs(cwd) {
							cwd = filepath.Join(root, cwd)
						}
						payload["cwd"], _ = json.Marshal(filepath.Clean(cwd))
					}
				}
				if request.URL.Path == "/api/conversations/new" || request.URL.Path == "/api/conversations/draft" {
					var cwd string
					_ = json.Unmarshal(payload["cwd"], &cwd)
					if cwd == "" {
						payload["cwd"], _ = json.Marshal(root)
						body, _ = json.Marshal(payload)
					}
				}
				body, _ = json.Marshal(payload)
			}
			request.Body = io.NopCloser(bytes.NewReader(body))
			request.ContentLength = int64(len(body))
		}
		next.ServeHTTP(writer, request)
	})
}
