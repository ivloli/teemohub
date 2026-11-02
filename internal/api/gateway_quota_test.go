package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"teemohub/internal/auth"
	"teemohub/internal/store"
)

func createQuotaKey(t *testing.T, st *store.Store, limits store.KeyLimits) string {
	t.Helper()
	admin, err := st.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	token := "sk-th-" + auth.RandomToken(12)
	if _, err := st.CreateKey(admin.ID, "quota", auth.HashToken(token), "sk-th-", "quota", limits, store.Actor{Name: "test"}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	return token
}

func chatPost(t *testing.T, g *Gateway, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	return rec
}

func TestKeyMaxOutputClampedOnWire(t *testing.T) {
	var (
		mu       sync.Mutex
		lastBody map[string]json.RawMessage
	)
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var obj map[string]json.RawMessage
		_ = json.Unmarshal(raw, &obj)
		mu.Lock()
		lastBody = obj
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","model":"deepseek-chat","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`))
	}))
	defer upstreamSrv.Close()

	g, st, _ := newTestGateway(t, upstreamSrv.URL)
	token := createQuotaKey(t, st, store.KeyLimits{RPM: 120, MaxOutput: 12})

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}],"max_tokens":9999,"max_completion_tokens":9999}`
	rec := chatPost(t, g, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	first := lastBody
	mu.Unlock()
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		var n int64
		if err := json.Unmarshal(first[k], &n); err != nil || n != 12 {
			t.Fatalf("upstream %s = %s, want 12", k, first[k])
		}
	}

	// With no client budget the cap is injected so the provider cannot exceed it.
	body = `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	if rec := chatPost(t, g, token, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	second := lastBody
	mu.Unlock()
	var n int64
	if err := json.Unmarshal(second["max_tokens"], &n); err != nil || n != 12 {
		t.Fatalf("injected max_tokens = %s, want 12", second["max_tokens"])
	}
	if _, ok := second["max_completion_tokens"]; ok {
		t.Fatalf("unexpected max_completion_tokens: %s", second["max_completion_tokens"])
	}
}

func TestKeyTPMRejectsOversizedRequest(t *testing.T) {
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called when admission fails")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstreamSrv.Close()

	g, st, _ := newTestGateway(t, upstreamSrv.URL)
	token := createQuotaKey(t, st, store.KeyLimits{RPM: 120, TPM: 10})

	long := strings.Repeat("a", 400) // ~100 estimated input tokens
	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"` + long + `"}]}`
	rec := chatPost(t, g, token, body)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("error code = %q (err=%v), body=%s", e.Error.Code, err, rec.Body.String())
	}
}
