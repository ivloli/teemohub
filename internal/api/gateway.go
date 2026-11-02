package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"teemohub/internal/auth"
	"teemohub/internal/config"
	"teemohub/internal/meter"
	"teemohub/internal/store"
	"teemohub/internal/upstream"
)

type Gateway struct {
	cfg     *config.Config
	st      *store.Store
	up      *upstream.Client
	limiter *rateLimiter
}

// SSE keepalive cadence. A comment line is sent only while the upstream has
// been silent, so active streams carry no extra traffic. Tests shorten these.
var (
	sseKeepaliveEvery = 15 * time.Second
	sseKeepaliveIdle  = 15 * time.Second
)

func NewGateway(cfg *config.Config, st *store.Store, up *upstream.Client) *Gateway {
	return &Gateway{cfg: cfg, st: st, up: up, limiter: newRateLimiter(4)}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.chatCompletions)
	mux.HandleFunc("GET /v1/models", g.models)
	mux.HandleFunc("GET /healthz", g.health)
	return recoverMW(mux)
}

type sseChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens         int64 `json:"prompt_tokens"`
		CompletionTokens     int64 `json:"completion_tokens"`
		TotalTokens          int64 `json:"total_tokens"`
		PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails  *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func (g *Gateway) chatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	key, user, err := g.authKey(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid API key", "invalid_api_key")
		return
	}
	if !g.limiter.Acquire(key.ID, key.RPM, key.Concurrent) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded", "rate_limit_exceeded")
		return
	}
	defer g.limiter.Release(key.ID)
	g.st.TouchKey(key.ID)

	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil || len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request")
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", "invalid_request")
		return
	}
	var modelID string
	_ = json.Unmarshal(obj["model"], &modelID)
	if modelID == "" {
		writeError(w, http.StatusBadRequest, "model is required", "invalid_request")
		return
	}
	var stream bool
	_ = json.Unmarshal(obj["stream"], &stream)

	m, err := g.resolveModel(modelID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown model: "+modelID, "model_not_found")
		return
	}
	if !m.Enabled {
		writeError(w, http.StatusForbidden, "model is disabled", "model_disabled")
		return
	}

	var messages []json.RawMessage
	if err := json.Unmarshal(obj["messages"], &messages); err != nil || len(messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages is required", "invalid_request")
		return
	}

	maxOut := m.MaxOutput
	if maxOut <= 0 {
		maxOut = 4096
	}
	for _, k := range []string{"max_completion_tokens", "max_tokens"} {
		if v, ok := obj[k]; ok {
			var n int64
			if json.Unmarshal(v, &n) == nil && n > 0 {
				maxOut = n
				break
			}
		}
	}
	if key.MaxOutput > 0 && maxOut > key.MaxOutput {
		maxOut = key.MaxOutput
	}
	reserveOut := maxOut
	if reserveOut > 8192 {
		reserveOut = 8192
	}

	counter := meter.Counter{Family: m.Family}
	localIn := counter.CountMessages(messages, obj["tools"])
	if !g.limiter.AcquireTokens(key.ID, key.TPM, localIn) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "token rate limit exceeded", "rate_limit_exceeded")
		return
	}
	reserve := meter.CostFor(localIn, reserveOut, m.InputPrice, m.OutputPrice)
	if reserve > user.BalanceNano {
		writeError(w, http.StatusPaymentRequired, fmt.Sprintf("insufficient balance: need at least %s, have %s", meter.FormatCNY(reserve), meter.FormatCNY(user.BalanceNano)), "insufficient_balance")
		return
	}

	requestID := "req_" + randHex(8)
	traceID := randHex(16)
	req := &store.Request{
		ID: requestID, UserID: user.ID, APIKeyID: key.ID, Model: m.ID,
		Endpoint: "/v1/chat/completions", Stream: stream, Status: "reserved",
		ReservedNano: reserve, PriceVersion: m.UpdatedAt, PriceVersionID: m.PriceVersionID,
		CreatedAt: start.UnixMilli(), TraceID: traceID,
		HasTools: len(obj["tools"]) > 2,
	}
	if err := g.st.CreateReservedRequest(req); err != nil {
		if errors.Is(err, store.ErrInsufficient) {
			writeError(w, http.StatusPaymentRequired, "insufficient balance", "insufficient_balance")
		} else {
			log.Printf("create request: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to admit request", "internal_error")
		}
		return
	}

	clampKeyMaxOutput(obj, key.MaxOutput)
	upBody, err := buildUpstreamBody(obj, m.UpstreamName, stream)
	if err != nil {
		g.finish(req, m, localIn, nil, "upstream_error", 0, false, false, "encode upstream body: "+err.Error(), start)
		writeError(w, http.StatusBadRequest, "invalid request body", "invalid_request")
		return
	}

	upCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), g.cfg.UpstreamTime)
	defer cancel()
	resp, err := g.up.Chat(upCtx, upBody, stream)
	if err != nil {
		g.finish(req, m, localIn, nil, "upstream_error", 0, false, false, "upstream: "+err.Error(), start)
		writeError(w, http.StatusBadGateway, "upstream unavailable", "upstream_error")
		return
	}
	defer resp.Body.Close()

	w.Header().Set("X-TeemoHub-Request-Id", requestID)
	w.Header().Set("X-TeemoHub-Trace-Id", traceID)
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		copyHeader(w.Header(), resp.Header, "Content-Type")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		g.finish(req, m, localIn, nil, "upstream_error", resp.StatusCode, false, false, fmt.Sprintf("upstream status %d", resp.StatusCode), start)
		return
	}

	if stream && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		g.streamResponse(w, r, req, m, localIn, resp, upCtx, cancel, start, counter)
		return
	}
	g.nonStreamResponse(w, req, m, localIn, resp, start, counter)
}

func (g *Gateway) nonStreamResponse(w http.ResponseWriter, req *store.Request, m *store.Model, localIn int64, resp *http.Response, start time.Time, counter meter.Counter) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		g.finish(req, m, localIn, nil, "upstream_error", resp.StatusCode, false, false, "read upstream: "+err.Error(), start)
		writeError(w, http.StatusBadGateway, "upstream read error", "upstream_error")
		return
	}
	copyHeader(w.Header(), resp.Header, "Content-Type")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)

	acc := meter.NewAccumulator(counter, localIn, m.MaxOutput)
	var ur struct {
		Usage *struct {
			PromptTokens         int64 `json:"prompt_tokens"`
			CompletionTokens     int64 `json:"completion_tokens"`
			TotalTokens          int64 `json:"total_tokens"`
			PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
			PromptTokensDetails  *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails *struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &ur) == nil && ur.Usage != nil {
		acc.OnUsage(meter.Usage{
			PromptTokens:     ur.Usage.PromptTokens,
			CompletionTokens: ur.Usage.CompletionTokens,
			TotalTokens:      ur.Usage.TotalTokens,
			ReasoningTokens:  reasoningTokens(ur.Usage.CompletionTokensDetails),
			CachedTokens:     cachedTokens(ur.Usage.PromptTokensDetails, ur.Usage.PromptCacheHitTokens),
		})
	}
	g.finish(req, m, localIn, acc, "completed", resp.StatusCode, false, true, "", start)
}

func (g *Gateway) streamResponse(w http.ResponseWriter, r *http.Request, req *store.Request, m *store.Model, localIn int64, resp *http.Response, upCtx context.Context, cancel context.CancelFunc, start time.Time, counter meter.Counter) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported", "internal_error")
		g.finish(req, m, localIn, nil, "upstream_error", 0, false, false, "no flusher", start)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	acc := meter.NewAccumulator(counter, localIn, m.MaxOutput)
	var clientGone atomic.Bool
	var bytesRead atomic.Int64
	var drainStart atomic.Int64
	var lastChunk atomic.Int64
	lastChunk.Store(time.Now().UnixNano())

	var writeMu sync.Mutex
	var lastWrite atomic.Int64
	lastWrite.Store(time.Now().UnixNano())

	handlerDone := make(chan struct{})
	defer close(handlerDone)
	go func() {
		select {
		case <-r.Context().Done():
			clientGone.Store(true)
		case <-handlerDone:
		}
	}()

	keepStop := make(chan struct{})
	keepDone := make(chan struct{})
	go func() {
		defer close(keepDone)
		t := time.NewTicker(sseKeepaliveEvery)
		defer t.Stop()
		for {
			select {
			case <-keepStop:
				return
			case <-t.C:
				if clientGone.Load() {
					continue
				}
				if time.Since(time.Unix(0, lastWrite.Load())) < sseKeepaliveIdle {
					continue
				}
				writeMu.Lock()
				if !clientGone.Load() {
					if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
						clientGone.Store(true)
					} else {
						flusher.Flush()
						lastWrite.Store(time.Now().UnixNano())
					}
				}
				writeMu.Unlock()
			}
		}
	}()

	watchStop := make(chan struct{})
	defer close(watchStop)
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-watchStop:
				return
			case <-t.C:
				if !clientGone.Load() {
					continue
				}
				now := time.Now().UnixNano()
				if drainStart.Load() == 0 {
					drainStart.Store(now)
				}
				elapsed := time.Duration(now - drainStart.Load())
				stalled := time.Duration(now-lastChunk.Load()) > 5*time.Second
				if elapsed > g.cfg.Drain || acc.ApproxOutput() > g.cfg.DrainMaxTokens || bytesRead.Load() > g.cfg.DrainMaxBytes || stalled {
					cancel()
					return
				}
			}
		}
	}()

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	done := false
	finishSeen := false
	var readErr error
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			bytesRead.Add(int64(len(line)))
			lastChunk.Store(time.Now().UnixNano())
			if !clientGone.Load() {
				writeMu.Lock()
				if _, werr := w.Write(line); werr != nil {
					clientGone.Store(true)
				} else {
					flusher.Flush()
					lastWrite.Store(time.Now().UnixNano())
				}
				writeMu.Unlock()
			}
			trimmed := strings.TrimSpace(string(line))
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload == "[DONE]" {
					done = true
					finishSeen = true
				} else if payload != "" {
					var ch sseChunk
					if json.Unmarshal([]byte(payload), &ch) == nil {
						acc.NextSeq()
						for _, choice := range ch.Choices {
							acc.AddContent(choice.Delta.Content)
							if choice.Delta.ReasoningContent != "" {
								acc.AddReasoning(choice.Delta.ReasoningContent)
							} else if choice.Delta.Reasoning != "" {
								acc.AddReasoning(choice.Delta.Reasoning)
							}
							for _, tc := range choice.Delta.ToolCalls {
								acc.AddToolCall(tc.Index, tc.Function.Name, tc.Function.Arguments)
							}
							if choice.FinishReason != nil && *choice.FinishReason != "" {
								acc.OnFinish(*choice.FinishReason)
								finishSeen = true
							}
						}
						if ch.Usage != nil {
							acc.OnUsage(meter.Usage{
								PromptTokens:     ch.Usage.PromptTokens,
								CompletionTokens: ch.Usage.CompletionTokens,
								TotalTokens:      ch.Usage.TotalTokens,
								ReasoningTokens:  reasoningTokens(ch.Usage.CompletionTokensDetails),
								CachedTokens:     cachedTokens(ch.Usage.PromptTokensDetails, ch.Usage.PromptCacheHitTokens),
							})
						}
					}
				}
			}
		}
		if err != nil {
			readErr = err
			break
		}
		if done {
			break
		}
	}

	close(keepStop)
	<-keepDone

	if !clientGone.Load() && !done {
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}

	status := "completed"
	errMsg := ""
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		errMsg = readErr.Error()
		switch {
		case clientGone.Load():
			status = "client_aborted"
		case upCtx.Err() != nil:
			status = "upstream_error"
		default:
			status = "upstream_error"
		}
	} else if clientGone.Load() {
		if finishSeen || done {
			status = "drained"
		} else {
			status = "client_aborted"
		}
	}
	g.finish(req, m, localIn, acc, status, http.StatusOK, clientGone.Load(), done || finishSeen, errMsg, start)
}

func (g *Gateway) finish(req *store.Request, m *store.Model, localIn int64, acc *meter.Accumulator, status string, httpStatus int, clientGone, upstreamDone bool, errMsg string, start time.Time) {
	res := meter.Result{Source: "none", Trust: "none", Detail: map[string]any{}}
	var localOut int64
	var firstTokenMS int64
	if acc != nil {
		kOut, _ := g.st.Calibration(m.ID, "output")
		kIn, _ := g.st.Calibration(m.ID, "input")
		res = acc.Finalize(kOut, kIn)
		localOut = acc.LocalOutput()
		if ft := acc.FirstTokenAt(); !ft.IsZero() {
			firstTokenMS = ft.Sub(start).Milliseconds()
		}
	}
	cost := meter.CostForCached(res.InputTokens, res.CachedInputTokens, res.OutputTokens, m.InputPrice, m.CachedInputPrice, m.OutputPrice)
	pcost := meter.CostForCached(res.InputTokens, res.CachedInputTokens, res.OutputTokens, m.ProviderInputPrice, m.ProviderCachedInputPrice, m.ProviderOutputPrice)
	if status == "upstream_error" {
		cost, pcost = 0, 0
	}
	if (status == "client_aborted" || status == "drained") && res.OutputTokens == 0 && firstTokenMS == 0 {
		cost, pcost = 0, 0
	}

	ev := store.UsageEvent{
		RequestID: req.ID, Attempt: 1, Final: true,
		Source: res.Source, Trust: res.Trust,
		InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, ReasoningTokens: res.ReasoningTokens,
		CachedInputTokens: res.CachedInputTokens,
		SnapshotSeq:       res.SnapshotSeq, LocalTokensAtSnap: res.LocalAtSnapshot,
		LocalOutputTokens: res.LocalOutputTokens, DetailJSON: res.DetailJSON(),
	}
	settled, err := g.st.Settle(store.SettleInput{
		RequestID: req.ID, Status: status, HTTPStatus: httpStatus,
		ClientDisconnected: clientGone, UpstreamFinished: upstreamDone,
		Usage: ev, CostNano: cost, ProviderCostNano: pcost,
		LatencyMS: time.Since(start).Milliseconds(), FirstTokenMS: firstTokenMS,
		ErrorMessage: errMsg,
	})
	if err != nil {
		log.Printf("settle %s: %v", req.ID, err)
	}
	// Admission already charged the estimated input; record actual output so
	// the key's TPM window reflects real consumption.
	g.limiter.AddTokens(req.APIKeyID, res.OutputTokens)
	if settled && res.Source == "provider_final" {
		// Tiny samples are dominated by chat-template overhead; only adapt the
		// heuristic once there is enough text for the ratio to be meaningful.
		if localOut >= 40 && res.OutputTokens > 0 {
			g.st.UpdateCalibration(m.ID, "output", float64(res.OutputTokens)/float64(localOut))
		}
		if localIn >= 40 && res.InputTokens > 0 && !req.HasTools {
			g.st.UpdateCalibration(m.ID, "input", float64(res.InputTokens)/float64(localIn))
		}
	}
	log.Printf("request=%s model=%s status=%s stream=%v in=%d cached_in=%d out=%d source=%s trust=%s cost=%s provider_cost=%s latency=%dms first_token=%dms settled=%v err=%q",
		req.ID, m.ID, status, req.Stream, res.InputTokens, res.CachedInputTokens, res.OutputTokens, res.Source, res.Trust,
		meter.FormatCNY(cost), meter.FormatCNY(pcost), time.Since(start).Milliseconds(), firstTokenMS, settled, errMsg)
}

func (g *Gateway) authKey(r *http.Request) (*store.APIKey, *store.User, error) {
	token := auth.Bearer(r)
	if !strings.HasPrefix(token, "sk-th-") {
		return nil, nil, store.ErrNotFound
	}
	key, err := g.st.GetKeyByHash(auth.HashToken(token))
	if err != nil {
		return nil, nil, err
	}
	if key.Status != 1 {
		return nil, nil, store.ErrNotFound
	}
	user, err := g.st.GetUser(key.UserID)
	if err != nil {
		return nil, nil, err
	}
	if user.Status != 1 {
		return nil, nil, store.ErrNotFound
	}
	return key, user, nil
}

func (g *Gateway) resolveModel(id string) (*store.Model, error) {
	if m, err := g.st.GetModel(id); err == nil {
		return m, nil
	}
	models, err := g.st.ListModels(false)
	if err != nil {
		return nil, err
	}
	for i := range models {
		if models[i].UpstreamName == id {
			return &models[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	if _, _, err := g.authKey(r); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid API key", "invalid_api_key")
		return
	}
	models, err := g.st.ListModels(true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list models", "internal_error")
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id": m.ID, "object": "model", "created": m.UpdatedAt / 1000,
			"owned_by": "teemohub", "context_window": m.ContextWindow,
			"pricing": map[string]string{
				"input_per_million_cny":        fmt.Sprintf("%.4f", float64(m.InputPrice)/100),
				"cached_input_per_million_cny": fmt.Sprintf("%.4f", float64(m.CachedInputPrice)/100),
				"output_per_million_cny":       fmt.Sprintf("%.4f", float64(m.OutputPrice)/100),
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := make([]healthCheck, 0, 2)

	dbStart := time.Now()
	dbErr := g.st.DB.PingContext(ctx)
	checks = append(checks, healthCheck{Name: "database", OK: dbErr == nil, Error: errString(dbErr), LatencyMS: time.Since(dbStart).Milliseconds()})

	upStart := time.Now()
	upErr := g.up.Status(ctx)
	checks = append(checks, healthCheck{Name: "new_api", OK: upErr == nil, Error: errString(upErr), LatencyMS: time.Since(upStart).Milliseconds(), Detail: g.cfg.NewAPIBaseURL})

	healthy := dbErr == nil && upErr == nil
	status := http.StatusOK
	if !healthy {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{
		"ok":       healthy,
		"upstream": g.cfg.NewAPIBaseURL,
		"error":    errString(upErr),
		"checks":   checks,
	})
}

func buildUpstreamBody(obj map[string]json.RawMessage, upstreamName string, stream bool) ([]byte, error) {
	modelJSON, _ := json.Marshal(upstreamName)
	obj["model"] = modelJSON
	if stream {
		obj["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	return json.Marshal(obj)
}

// clampKeyMaxOutput bounds the output budget on the wire so a per-key cap is
// enforced upstream, not just at reservation. limit <= 0 leaves the body alone.
func clampKeyMaxOutput(obj map[string]json.RawMessage, limit int64) {
	if limit <= 0 {
		return
	}
	set := false
	for _, k := range []string{"max_completion_tokens", "max_tokens"} {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		var n int64
		if json.Unmarshal(raw, &n) == nil && n > 0 && n <= limit {
			set = true
			continue
		}
		obj[k], _ = json.Marshal(limit)
		set = true
	}
	if !set {
		obj["max_tokens"], _ = json.Marshal(limit)
	}
}

func reasoningTokens(v *struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}) int64 {
	if v == nil {
		return 0
	}
	return v.ReasoningTokens
}

// cachedTokens reads the prompt-cache hit count that New API relays from the
// provider: OpenAI-style prompt_tokens_details.cached_tokens first, then
// DeepSeek's native prompt_cache_hit_tokens.
func cachedTokens(details *struct {
	CachedTokens int64 `json:"cached_tokens"`
}, native int64) int64 {
	if details != nil && details.CachedTokens > 0 {
		return details.CachedTokens
	}
	return native
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

func copyHeader(dst, src http.Header, keys ...string) {
	for _, k := range keys {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "teemohub_error", "code": code}})
}

func recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic: %v", v)
				writeError(w, http.StatusInternalServerError, "internal error", "internal_error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
