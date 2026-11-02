package store

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"teemohub/internal/config"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := &config.Config{
		DBPath:                filepath.Join(t.TempDir(), "test.db"),
		AdminUser:             "admin",
		AdminPassword:         "admin-pw",
		DemoUser:              "demo",
		DemoPassword:          "demo-pw",
		DeepSeekModel:         "deepseek-chat",
		DeepSeekReasonerModel: "deepseek-reasoner",
		KimiModel:             "kimi-k3.1-pro",
	}
	st, err := Open(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func ledgerFor(t *testing.T, st *Store, requestID string) ([]LedgerEntry, int64) {
	t.Helper()
	_, entries, err := st.UsageForRequest(requestID)
	if err != nil {
		t.Fatalf("usage for request: %v", err)
	}
	var balance int64
	if err := st.DB.QueryRow(`SELECT balance_nano FROM users WHERE id=(SELECT user_id FROM requests WHERE id=?)`, requestID).Scan(&balance); err != nil {
		t.Fatalf("balance: %v", err)
	}
	return entries, balance
}

func admittedRequest(t *testing.T, st *Store, userID int64, reserved int64) *Request {
	t.Helper()
	key, err := st.CreateKey(userID, "test", "hash-"+nowMSString(), "sk-", "abcd", KeyLimits{RPM: 60}, Actor{Name: "test"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	r := &Request{
		ID:           "req_test_" + nowMSString(),
		UserID:       userID,
		APIKeyID:     key.ID,
		Model:        "deepseek-chat",
		Endpoint:     "/v1/chat/completions",
		Status:       "reserved",
		ReservedNano: reserved,
		PriceVersion: 2,
		CreatedAt:    nowMS(),
		TraceID:      "trace-test",
		UsageSource:  "",
		UsageTrust:   "",
	}
	if err := st.CreateReservedRequest(r); err != nil {
		t.Fatalf("admit request: %v", err)
	}
	return r
}

func TestSettleReplayDoesNotDoubleBill(t *testing.T) {
	st := newTestStore(t)
	u, err := st.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("admin user: %v", err)
	}
	start, err := st.TopUp(u.ID, 1_000_000, "test topup", Actor{Name: "test"})
	if err != nil {
		t.Fatalf("topup: %v", err)
	}

	r := admittedRequest(t, st, u.ID, 5000)
	if _, bal := ledgerFor(t, st, r.ID); bal != start-5000 {
		t.Fatalf("balance after reserve = %d, want %d", bal, start-5000)
	}

	in := SettleInput{
		RequestID:        r.ID,
		Status:           "completed",
		HTTPStatus:       200,
		UpstreamFinished: true,
		Usage: UsageEvent{
			Source:       "provider_final",
			Trust:        "reported",
			InputTokens:  100,
			OutputTokens: 20,
			DetailJSON:   `{"cached_input":0}`,
		},
		CostNano:         3000,
		ProviderCostNano: 2500,
		LatencyMS:        900,
	}
	ok, err := st.Settle(in)
	if err != nil || !ok {
		t.Fatalf("first settle = (%v,%v), want (true,nil)", ok, err)
	}
	entries, bal := ledgerFor(t, st, r.ID)
	if bal != start-3000 {
		t.Fatalf("balance after settle = %d, want %d", bal, start-3000)
	}
	if len(entries) != 2 {
		t.Fatalf("ledger entries after settle = %d, want 2 (reserve+settle)", len(entries))
	}

	ok, err = st.Settle(in)
	if err != nil {
		t.Fatalf("replay settle error: %v", err)
	}
	if ok {
		t.Fatal("replay settle reported success; ledger write must be guarded by status")
	}
	entries, bal = ledgerFor(t, st, r.ID)
	if bal != start-3000 {
		t.Fatalf("balance after replay = %d, want unchanged %d", bal, start-3000)
	}
	if len(entries) != 2 {
		t.Fatalf("ledger entries after replay = %d, want still 2", len(entries))
	}
	var events int
	if err := st.DB.QueryRow(`SELECT COUNT(1) FROM usage_events WHERE request_id=?`, r.ID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Fatalf("usage_events = %d, want 1", events)
	}
}

func TestSettleZeroCostReleasesReservation(t *testing.T) {
	st := newTestStore(t)
	u, err := st.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("admin user: %v", err)
	}
	start, err := st.TopUp(u.ID, 1_000_000, "test topup", Actor{Name: "test"})
	if err != nil {
		t.Fatalf("topup: %v", err)
	}

	r := admittedRequest(t, st, u.ID, 8000)
	in := SettleInput{
		RequestID:  r.ID,
		Status:     "upstream_error",
		HTTPStatus: 502,
		Usage: UsageEvent{
			Source:     "none",
			Trust:      "none",
			DetailJSON: `{}`,
		},
		CostNano: 0,
	}
	ok, err := st.Settle(in)
	if err != nil || !ok {
		t.Fatalf("settle = (%v,%v), want (true,nil)", ok, err)
	}
	entries, bal := ledgerFor(t, st, r.ID)
	if bal != start {
		t.Fatalf("balance after zero-cost settle = %d, want fully refunded %d", bal, start)
	}
	var release int
	for _, e := range entries {
		if e.EntryType == "release" {
			release++
			if e.AmountNano != 8000 {
				t.Fatalf("release amount = %d, want 8000", e.AmountNano)
			}
		}
	}
	if release != 1 {
		t.Fatalf("release entries = %d, want 1", release)
	}

	if ok, err := st.Settle(in); err != nil || ok {
		t.Fatalf("release replay = (%v,%v), want (false,nil)", ok, err)
	}
	if _, bal := ledgerFor(t, st, r.ID); bal != start {
		t.Fatalf("balance after release replay = %d, want %d", bal, start)
	}
}

func TestSettleUnknownRequest(t *testing.T) {
	st := newTestStore(t)
	if ok, err := st.Settle(SettleInput{RequestID: "req_missing"}); !errors.Is(err, ErrNotFound) || ok {
		t.Fatalf("settle unknown = (%v,%v), want (false, ErrNotFound)", ok, err)
	}
}

func nowMSString() string {
	return strconv.FormatInt(nowMS(), 10)
}

func TestPriceVersioningAppendsAndAudits(t *testing.T) {
	st := newTestStore(t)
	m, err := st.GetModel("deepseek-chat")
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	if m.PriceVersionID == 0 {
		t.Fatalf("seed should backfill a baseline price version")
	}
	versions, err := st.ListModelPriceVersions(m.ID, 10)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 || versions[0].ID != m.PriceVersionID {
		t.Fatalf("expected exactly one baseline version, got %+v", versions)
	}

	p := PriceTick{
		InputPrice: m.InputPrice, OutputPrice: m.OutputPrice,
		ProviderInputPrice: m.ProviderInputPrice, ProviderOutputPrice: m.ProviderOutputPrice,
		CachedInputPrice: m.CachedInputPrice, ProviderCachedInputPrice: m.ProviderCachedInputPrice,
	}
	p.InputPrice += 7
	vid, err := st.UpdateModelPrices(m.ID, p, m.Enabled, Actor{ID: 1, Name: "admin"}, "raise input")
	if err != nil {
		t.Fatalf("update prices: %v", err)
	}
	if vid == m.PriceVersionID {
		t.Fatalf("price change must append a new version")
	}
	updated, _ := st.GetModel(m.ID)
	if updated.PriceVersionID != vid || updated.InputPrice != p.InputPrice {
		t.Fatalf("model did not move to new version: %+v", updated)
	}

	// Toggling enabled alone must not append a version.
	vid2, err := st.UpdateModelPrices(m.ID, p, !m.Enabled, Actor{ID: 1, Name: "admin"}, "")
	if err != nil {
		t.Fatalf("toggle enabled: %v", err)
	}
	if vid2 != vid {
		t.Fatalf("enabled-only change appended a version: %d -> %d", vid, vid2)
	}

	if _, err := st.DB.Exec(`UPDATE model_price_versions SET input_price=1 WHERE id=?`, vid); err == nil {
		t.Fatalf("model_price_versions must reject updates")
	}
	if _, err := st.DB.Exec(`DELETE FROM model_price_versions WHERE id=?`, vid); err == nil {
		t.Fatalf("model_price_versions must reject deletes")
	}

	events, err := st.ListAuditEvents(AuditFilter{Resource: "model", Limit: 10})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 audit rows for two edits, got %d", len(events))
	}
	if events[0].Action != "model.prices.update" || events[0].BeforeJSON == "" || events[0].AfterJSON == "" {
		t.Fatalf("audit row missing before/after: %+v", events[0])
	}
	if _, err := st.DB.Exec(`DELETE FROM audit_events WHERE id=?`, events[0].ID); err == nil {
		t.Fatalf("audit_events must reject deletes")
	}
}

func TestRequestSnapshotsPriceVersion(t *testing.T) {
	st := newTestStore(t)
	u, err := st.GetUserByUsername("demo")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	m, _ := st.GetModel("deepseek-chat")
	key, err := st.CreateKey(u.ID, "snap", "hash-"+nowMSString(), "sk-", "efgh", KeyLimits{RPM: 60}, Actor{Name: "demo"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	req := &Request{
		ID: "req_snap_" + nowMSString(), UserID: u.ID, APIKeyID: key.ID, Model: m.ID,
		Endpoint: "/v1/chat/completions", Status: "reserved", ReservedNano: 0,
		PriceVersion: 2, PriceVersionID: m.PriceVersionID, CreatedAt: nowMS(), TraceID: "trace-snap",
	}
	if err := st.CreateReservedRequest(req); err != nil {
		t.Fatalf("admit request: %v", err)
	}
	if _, err := st.Settle(SettleInput{RequestID: req.ID, Status: "completed", HTTPStatus: 200, CostNano: 0,
		Usage: UsageEvent{Source: "counted_text", Trust: "local_estimate"}}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	got, err := st.GetRequest(u.ID, req.ID)
	if err != nil {
		t.Fatalf("get request: %v", err)
	}
	if got.PriceVersionID != m.PriceVersionID {
		t.Fatalf("request price_version_id = %d, want %d", got.PriceVersionID, m.PriceVersionID)
	}
	if v, err := st.GetModelPriceVersion(got.PriceVersionID); err != nil || v.ModelID != m.ID {
		t.Fatalf("version lookup: %v %+v", err, v)
	}
}
