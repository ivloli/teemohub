package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"teemohub/internal/auth"
	"teemohub/internal/config"
	"teemohub/internal/store"
	"teemohub/internal/upstream"
)

func newTestGateway(t *testing.T, upstreamURL string) (*Gateway, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DBPath:                filepath.Join(dir, "test.db"),
		AdminUser:             "admin",
		AdminPassword:         "TestAdmin!2026",
		DemoUser:              "demo",
		DemoPassword:          "TestDemo!2026",
		NewAPIBaseURL:         upstreamURL,
		NewAPIToken:           "test-token",
		Drain:                 2 * time.Second,
		DrainMaxTokens:        512,
		DrainMaxBytes:         1 << 20,
		UpstreamTime:          10 * time.Second,
		DeepSeekModel:         "deepseek-chat",
		DeepSeekReasonerModel: "deepseek-reasoner",
		KimiModel:             "kimi-k3.1-pro",
	}
	st, err := store.Open(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, err := st.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	token := "sk-th-" + auth.RandomToken(12)
	if _, err := st.CreateKey(admin.ID, "test", auth.HashToken(token), "sk-th-", "test", store.KeyLimits{RPM: 120}, store.Actor{Name: "test"}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	up := &upstream.Client{BaseURL: upstreamURL, Token: "test-token", HTTP: &http.Client{Timeout: 10 * time.Second}}
	return NewGateway(cfg, st, up), st, token
}

func TestStreamKeepaliveDuringUpstreamSilence(t *testing.T) {
	oldEvery, oldIdle := sseKeepaliveEvery, sseKeepaliveIdle
	sseKeepaliveEvery, sseKeepaliveIdle = 20*time.Millisecond, 20*time.Millisecond
	defer func() { sseKeepaliveEvery, sseKeepaliveIdle = oldEvery, oldIdle }()

	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`data: {"id":"c1","model":"deepseek-chat","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}` + "\n\n"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
	}))
	defer upstreamSrv.Close()

	g, st, token := newTestGateway(t, upstreamSrv.URL)

	body := `{"model":"deepseek-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	g.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if n := strings.Count(out, ": keepalive\n\n"); n < 3 {
		t.Fatalf("keepalive count = %d, want >= 3; body=%q", n, out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("missing DONE; body=%q", out)
	}
	if strings.Index(out, "data: [DONE]") < strings.LastIndex(out, ": keepalive") {
		t.Fatalf("DONE written before keepalive stopped; body=%q", out)
	}

	var reqID string
	rows, err := st.DB.Query(`SELECT id,status FROM requests ORDER BY created_at DESC LIMIT 1`)
	if err != nil {
		t.Fatalf("query requests: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no request row")
	}
	var status string
	if err := rows.Scan(&reqID, &status); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "completed" {
		t.Fatalf("request status = %q, want completed", status)
	}
	_ = json.Valid([]byte(out))
}
