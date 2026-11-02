package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"teemohub/internal/auth"
	"teemohub/internal/config"
	"teemohub/internal/meter"
	"teemohub/internal/store"
	"teemohub/internal/upstream"
)

type Control struct {
	cfg     *config.Config
	st      *store.Store
	up      *upstream.Client
	gateway http.Handler
}

func NewControl(cfg *config.Config, st *store.Store, up *upstream.Client, gateway http.Handler) *Control {
	return &Control{cfg: cfg, st: st, up: up, gateway: gateway}
}

func (c *Control) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", c.login)
	mux.HandleFunc("POST /api/auth/logout", c.logout)
	mux.HandleFunc("GET /api/me", c.me)
	mux.HandleFunc("GET /api/keys", c.listKeys)
	mux.HandleFunc("POST /api/keys", c.createKey)
	mux.HandleFunc("DELETE /api/keys/{id}", c.revokeKey)
	mux.HandleFunc("GET /api/models", c.listModels)
	mux.HandleFunc("GET /api/requests", c.listRequests)
	mux.HandleFunc("GET /api/requests/{id}", c.requestDetail)
	mux.HandleFunc("GET /api/usage/summary", c.summary)
	mux.HandleFunc("GET /api/usage/analytics", c.analytics)
	mux.HandleFunc("GET /api/wallet", c.wallet)
	mux.HandleFunc("GET /api/admin/users", c.adminListUsers)
	mux.HandleFunc("POST /api/admin/users", c.adminCreateUser)
	mux.HandleFunc("POST /api/admin/users/{id}/topup", c.adminTopUp)
	mux.HandleFunc("GET /api/admin/models", c.adminListModels)
	mux.HandleFunc("PUT /api/admin/models/{id}", c.adminUpdateModel)
	mux.HandleFunc("GET /api/admin/models/{id}/versions", c.adminModelVersions)
	mux.HandleFunc("GET /api/admin/audit", c.adminAudit)
	mux.HandleFunc("GET /api/admin/upstream", c.adminUpstream)
	mux.HandleFunc("GET /healthz", c.health)
	mux.Handle("/v1/", c.gateway)
	mux.HandleFunc("/", c.static)
	return recoverMW(mux)
}

func (c *Control) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Username == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required", "invalid_request")
		return
	}
	u, err := c.st.GetUserByUsername(in.Username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, in.Password) {
		writeError(w, http.StatusUnauthorized, "invalid username or password", "invalid_credentials")
		return
	}
	if u.Status != 1 {
		writeError(w, http.StatusForbidden, "account disabled", "account_disabled")
		return
	}
	token := auth.RandomToken(32)
	expires := time.Now().Add(7 * 24 * time.Hour)
	if err := c.st.CreateSession(u.ID, auth.HashToken(token), expires.UnixMilli()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session", "internal_error")
		return
	}
	auth.SetSessionCookie(w, token, expires)
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

func (c *Control) logout(w http.ResponseWriter, r *http.Request) {
	if token := auth.SessionToken(r); token != "" {
		c.st.DeleteSession(auth.HashToken(token))
	}
	auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Control) me(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

func (c *Control) listKeys(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	keys, err := c.st.ListKeys(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list keys", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": keys})
}

func (c *Control) createKey(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Name       string `json:"name"`
		RPM        int    `json:"rpm"`
		TPM        int64  `json:"tpm"`
		Concurrent int    `json:"concurrent"`
		MaxOutput  int64  `json:"max_output"`
	}
	_ = decodeJSON(r, &in)
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "default"
	}
	if in.RPM <= 0 {
		in.RPM = 60
	}
	if in.TPM < 0 {
		in.TPM = 0
	}
	if in.Concurrent < 0 {
		in.Concurrent = 0
	}
	if in.MaxOutput < 0 {
		in.MaxOutput = 0
	}
	full, prefix, suffix, hash := auth.NewAPIKey()
	limits := store.KeyLimits{RPM: in.RPM, TPM: in.TPM, Concurrent: in.Concurrent, MaxOutput: in.MaxOutput}
	key, err := c.st.CreateKey(u.ID, in.Name, hash, prefix, suffix, limits, adminActor(u))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create key", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "secret": full})
}

func (c *Control) revokeKey(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key id", "invalid_request")
		return
	}
	if err := c.st.RevokeKey(u.ID, id, adminActor(u)); err != nil {
		writeError(w, http.StatusNotFound, "key not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type publicModel struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name"`
	Family           string `json:"family"`
	InputPrice       int64  `json:"input_price_nano"`
	OutputPrice      int64  `json:"output_price_nano"`
	CachedInputPrice int64  `json:"cached_input_price_nano"`
	ContextWindow    int64  `json:"context_window"`
	MaxOutput        int64  `json:"max_output"`
	Enabled          bool   `json:"enabled"`
	UpdatedAt        int64  `json:"updated_at"`
}

func sanitizeModel(m store.Model) publicModel {
	return publicModel{
		ID: m.ID, DisplayName: m.DisplayName, Family: m.Family,
		InputPrice: m.InputPrice, OutputPrice: m.OutputPrice, CachedInputPrice: m.CachedInputPrice,
		ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
		Enabled: m.Enabled, UpdatedAt: m.UpdatedAt,
	}
}

func (c *Control) listModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireUser(w, r); !ok {
		return
	}
	models, err := c.st.ListModels(true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list models", "internal_error")
		return
	}
	items := make([]publicModel, 0, len(models))
	for _, m := range models {
		items = append(items, sanitizeModel(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "tokenizer": "heuristic-calibrated"})
}

func (c *Control) listRequests(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	keyID, _ := strconv.ParseInt(q.Get("key_id"), 10, 64)
	filter := store.RequestFilter{
		UserID: u.ID,
		Limit:  limit,
		Offset: offset,
		Model:  strings.TrimSpace(q.Get("model")),
		Status: strings.TrimSpace(q.Get("status")),
		Query:  strings.TrimSpace(q.Get("q")),
		KeyID:  keyID,
	}
	items, total, err := c.st.ListRequests(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list requests", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (c *Control) requestDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	req, err := c.st.GetRequest(u.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "request not found", "not_found")
		return
	}
	events, ledger, err := c.st.UsageForRequest(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request", "internal_error")
		return
	}
	var version *store.ModelPriceVersion
	if req.PriceVersionID > 0 {
		version, _ = c.st.GetModelPriceVersion(req.PriceVersionID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": req, "usage_events": events, "ledger": ledger, "price_version": version})
}

func (c *Control) summary(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 30 {
		days = 7
	}
	sum, err := c.st.Summary(u.ID, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load summary", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (c *Control) analytics(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 7
	}
	a, err := c.st.Analytics(u.ID, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load analytics", "internal_error")
		return
	}
	hidden := u.Role != "admin"
	if hidden {
		a.Totals.ProviderCostNano = 0
		for i := range a.Daily {
			a.Daily[i].ProviderCostNano = 0
		}
		for i := range a.Models {
			a.Models[i].ProviderCostNano = 0
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"analytics": a, "provider_cost_hidden": hidden})
}

func (c *Control) wallet(w http.ResponseWriter, r *http.Request) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	entries, total, err := c.st.Wallet(u.ID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load wallet", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balance_nano": u.BalanceNano, "entries": entries, "total": total, "limit": limit, "offset": offset})
}

func (c *Control) adminListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireAdmin(w, r); !ok {
		return
	}
	users, err := c.st.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": users})
}

func (c *Control) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	admin, ok := c.requireAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Username    string  `json:"username"`
		Password    string  `json:"password"`
		Role        string  `json:"role"`
		DisplayName string  `json:"display_name"`
		BalanceCNY  float64 `json:"balance_cny"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Username == "" || len(in.Password) < 8 {
		writeError(w, http.StatusBadRequest, "username and a password of at least 8 characters are required", "invalid_request")
		return
	}
	if in.Role != "admin" {
		in.Role = "user"
	}
	u, err := c.st.CreateUser(in.Username, in.Password, in.Role, in.DisplayName, cnyToNano(in.BalanceCNY), adminActor(admin))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "username already exists", "conflict")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(u)})
}

func (c *Control) adminTopUp(w http.ResponseWriter, r *http.Request) {
	admin, ok := c.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id", "invalid_request")
		return
	}
	var in struct {
		AmountCNY float64 `json:"amount_cny"`
		Memo      string  `json:"memo"`
	}
	if err := decodeJSON(r, &in); err != nil || in.AmountCNY == 0 {
		writeError(w, http.StatusBadRequest, "amount_cny is required", "invalid_request")
		return
	}
	bal, err := c.st.TopUp(id, cnyToNano(in.AmountCNY), in.Memo, adminActor(admin))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to top up", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balance_nano": bal})
}

func (c *Control) adminListModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireAdmin(w, r); !ok {
		return
	}
	models, err := c.st.ListModels(false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list models", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": models})
}

func (c *Control) adminUpdateModel(w http.ResponseWriter, r *http.Request) {
	admin, ok := c.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	existing, err := c.st.GetModel(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "model not found", "not_found")
		return
	}
	var in struct {
		InputPriceCNYPerM          *float64 `json:"input_price_cny_per_m"`
		OutputPriceCNYPerM         *float64 `json:"output_price_cny_per_m"`
		CachedInputCNYPerM         *float64 `json:"cached_input_price_cny_per_m"`
		ProviderInputCNYPerM       *float64 `json:"provider_input_price_cny_per_m"`
		ProviderOutputCNYPerM      *float64 `json:"provider_output_price_cny_per_m"`
		ProviderCachedInputCNYPerM *float64 `json:"provider_cached_input_price_cny_per_m"`
		Enabled                    *bool    `json:"enabled"`
		Note                       string   `json:"note"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request", "invalid_request")
		return
	}
	p := store.PriceTick{
		InputPrice: existing.InputPrice, OutputPrice: existing.OutputPrice,
		ProviderInputPrice: existing.ProviderInputPrice, ProviderOutputPrice: existing.ProviderOutputPrice,
		CachedInputPrice: existing.CachedInputPrice, ProviderCachedInputPrice: existing.ProviderCachedInputPrice,
	}
	enabled := existing.Enabled
	if in.InputPriceCNYPerM != nil {
		p.InputPrice = cnyPerMToNanoPerToken(*in.InputPriceCNYPerM)
	}
	if in.OutputPriceCNYPerM != nil {
		p.OutputPrice = cnyPerMToNanoPerToken(*in.OutputPriceCNYPerM)
	}
	if in.CachedInputCNYPerM != nil {
		p.CachedInputPrice = cnyPerMToNanoPerToken(*in.CachedInputCNYPerM)
	}
	if in.ProviderInputCNYPerM != nil {
		p.ProviderInputPrice = cnyPerMToNanoPerToken(*in.ProviderInputCNYPerM)
	}
	if in.ProviderOutputCNYPerM != nil {
		p.ProviderOutputPrice = cnyPerMToNanoPerToken(*in.ProviderOutputCNYPerM)
	}
	if in.ProviderCachedInputCNYPerM != nil {
		p.ProviderCachedInputPrice = cnyPerMToNanoPerToken(*in.ProviderCachedInputCNYPerM)
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	versionID, err := c.st.UpdateModelPrices(id, p, enabled, adminActor(admin), strings.TrimSpace(in.Note))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update model", "internal_error")
		return
	}
	m, _ := c.st.GetModel(id)
	writeJSON(w, http.StatusOK, map[string]any{"model": m, "price_version_id": versionID})
}

func (c *Control) adminModelVersions(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireAdmin(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := c.st.GetModel(id); err != nil {
		writeError(w, http.StatusNotFound, "model not found", "not_found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	versions, err := c.st.ListModelPriceVersions(id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list versions", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": versions})
}

func (c *Control) adminAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireAdmin(w, r); !ok {
		return
	}
	q := r.URL.Query()
	beforeID, _ := strconv.ParseInt(q.Get("before_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, err := c.st.ListAuditEvents(store.AuditFilter{
		Resource: strings.TrimSpace(q.Get("resource")),
		Action:   strings.TrimSpace(q.Get("action")),
		Query:    strings.TrimSpace(q.Get("q")),
		BeforeID: beforeID,
		Limit:    limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list audit events", "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func adminActor(u *store.User) store.Actor {
	return store.Actor{ID: u.ID, Name: u.Username, Type: "user"}
}

func (c *Control) adminUpstream(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.requireAdmin(w, r); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	err := c.up.Status(ctx)
	models, _ := c.st.ListModels(false)
	writeJSON(w, http.StatusOK, map[string]any{
		"new_api_base_url": c.cfg.NewAPIBaseURL,
		"healthy":          err == nil,
		"error":            errString(err),
		"models":           models,
	})
}

type healthCheck struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
}

func (c *Control) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	checks := make([]healthCheck, 0, 3)

	dbStart := time.Now()
	dbErr := c.st.DB.PingContext(ctx)
	checks = append(checks, healthCheck{Name: "database", OK: dbErr == nil, Error: errString(dbErr), LatencyMS: time.Since(dbStart).Milliseconds()})

	upStart := time.Now()
	upErr := c.up.Status(ctx)
	checks = append(checks, healthCheck{Name: "new_api", OK: upErr == nil, Error: errString(upErr), LatencyMS: time.Since(upStart).Milliseconds(), Detail: c.cfg.NewAPIBaseURL})

	calibrations, calErr := c.st.Calibrations()
	details := make([]string, 0, len(calibrations))
	totalSamples := int64(0)
	for _, cal := range calibrations {
		totalSamples += cal.Samples
		details = append(details, fmt.Sprintf("%s/%s k=%.3f n=%d", cal.Model, cal.Direction, cal.K, cal.Samples))
	}
	checks = append(checks, healthCheck{
		Name:   "metering",
		OK:     calErr == nil,
		Error:  errString(calErr),
		Detail: fmt.Sprintf("heuristic tokenizer · %d calibration samples", totalSamples),
	})
	if len(details) > 0 {
		checks[len(checks)-1].Detail += " · " + strings.Join(details, ", ")
	}

	healthy := dbErr == nil && upErr == nil && calErr == nil
	status := http.StatusOK
	if !healthy {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{
		"ok":       healthy,
		"upstream": c.cfg.NewAPIBaseURL,
		"error":    errString(upErr),
		"checks":   checks,
	})
}

func (c *Control) static(w http.ResponseWriter, r *http.Request) {
	dir := c.cfg.WebDir
	clean := filepath.Clean("/" + r.URL.Path)
	file := filepath.Join(dir, filepath.FromSlash(clean))
	if info, err := os.Stat(file); err == nil && !info.IsDir() {
		http.ServeFile(w, r, file)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	index := filepath.Join(dir, "index.html")
	if _, err := os.Stat(index); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>teemoHub</title>
<body style="font-family:system-ui;padding:40px;max-width:720px;margin:auto">
<h1>teemoHub control plane</h1>
<p>The frontend bundle is not built yet. Run <code>cd web &amp;&amp; npm install &amp;&amp; npm run build</code>, then restart.</p>
<p>API: <code>/api/me</code> · Gateway: <code>/v1/chat/completions</code></p></body>`)
		return
	}
	http.ServeFile(w, r, index)
}

func (c *Control) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	id, err := auth.Authenticate(c.st, r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "login required", "unauthorized")
		return nil, false
	}
	return id.User, true
}

func (c *Control) requireAdmin(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := c.requireUser(w, r)
	if !ok {
		return nil, false
	}
	if u.Role != "admin" {
		writeError(w, http.StatusForbidden, "admin required", "forbidden")
		return nil, false
	}
	return u, true
}

func publicUser(u *store.User) map[string]any {
	return map[string]any{
		"id": u.ID, "username": u.Username, "role": u.Role, "display_name": u.DisplayName,
		"balance_nano": u.BalanceNano, "balance_cny": meter.FormatCNY(u.BalanceNano), "created_at": u.CreatedAt,
	}
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

func cnyToNano(cny float64) int64 {
	return int64(math.Round(cny * 1e8))
}

func cnyPerMToNanoPerToken(cnyPerM float64) int64 {
	return int64(math.Round(cnyPerM * 100))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
