package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"teemohub/internal/config"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrInsufficient = errors.New("insufficient balance")
)

type Store struct {
	DB *sql.DB
}

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"`
	DisplayName  string `json:"display_name"`
	BalanceNano  int64  `json:"balance_nano"`
	Status       int    `json:"status"`
	CreatedAt    int64  `json:"created_at"`
}

type Session struct {
	TokenHash string
	UserID    int64
	ExpiresAt int64
}

type APIKey struct {
	ID      int64  `json:"id"`
	UserID  int64  `json:"user_id"`
	Name    string `json:"name"`
	KeyHash string `json:"-"`
	Prefix  string `json:"prefix"`
	Suffix  string `json:"suffix"`
	Status  int    `json:"status"`
	RPM     int    `json:"rpm"`
	// TPM caps tokens per sliding 60s window: estimated input at admission plus
	// actual output at settle. 0 means unlimited.
	TPM int64 `json:"tpm"`
	// Concurrent caps simultaneous in-flight requests for this key; 0 uses the
	// gateway default.
	Concurrent int `json:"concurrent"`
	// MaxOutput caps max_tokens for this key and is enforced on both the
	// reservation and the upstream request. 0 means no per-key cap.
	MaxOutput  int64 `json:"max_output"`
	CreatedAt  int64 `json:"created_at"`
	LastUsedAt int64 `json:"last_used_at"`
}

type KeyLimits struct {
	RPM        int   `json:"rpm"`
	TPM        int64 `json:"tpm"`
	Concurrent int   `json:"concurrent"`
	MaxOutput  int64 `json:"max_output"`
}

type Model struct {
	ID                  string `json:"id"`
	DisplayName         string `json:"display_name"`
	UpstreamName        string `json:"upstream_name"`
	Family              string `json:"family"`
	InputPrice          int64  `json:"input_price_nano"`
	OutputPrice         int64  `json:"output_price_nano"`
	ProviderInputPrice  int64  `json:"provider_input_price_nano"`
	ProviderOutputPrice int64  `json:"provider_output_price_nano"`
	// CachedInputPrice is the customer price for prompt tokens that hit the
	// provider's prompt cache; ProviderCachedInputPrice is what the provider
	// charges us. Both default to 0, meaning cached tokens bill at the normal
	// input price.
	CachedInputPrice         int64 `json:"cached_input_price_nano"`
	ProviderCachedInputPrice int64 `json:"provider_cached_input_price_nano"`
	ContextWindow            int64 `json:"context_window"`
	MaxOutput                int64 `json:"max_output"`
	Enabled                  bool  `json:"enabled"`
	UpdatedAt                int64 `json:"updated_at"`
	// PriceVersionID points at the append-only row in model_price_versions that
	// these prices came from; requests snapshot it at admission.
	PriceVersionID int64 `json:"price_version_id"`
}

// PriceTick is the immutable price tuple carried by a rate-card version.
type PriceTick struct {
	InputPrice               int64 `json:"input_price_nano"`
	OutputPrice              int64 `json:"output_price_nano"`
	ProviderInputPrice       int64 `json:"provider_input_price_nano"`
	ProviderOutputPrice      int64 `json:"provider_output_price_nano"`
	CachedInputPrice         int64 `json:"cached_input_price_nano"`
	ProviderCachedInputPrice int64 `json:"provider_cached_input_price_nano"`
}

// ModelPriceVersion is one append-only rate card. Rows are never updated or
// deleted (enforced by trigger); a price change appends a new row.
type ModelPriceVersion struct {
	ID        int64     `json:"id"`
	ModelID   string    `json:"model_id"`
	Price     PriceTick `json:"price"`
	Actor     string    `json:"actor"`
	Note      string    `json:"note"`
	CreatedAt int64     `json:"created_at"`
}

// Actor identifies who caused an audited write.
type Actor struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type AuditEvent struct {
	ID         int64  `json:"id"`
	ActorID    int64  `json:"actor_id"`
	ActorName  string `json:"actor_name"`
	ActorType  string `json:"actor_type"`
	Action     string `json:"action"`
	Resource   string `json:"resource"`
	ResourceID string `json:"resource_id"`
	BeforeJSON string `json:"before_json"`
	AfterJSON  string `json:"after_json"`
	Reason     string `json:"reason"`
	CreatedAt  int64  `json:"created_at"`
}

type Request struct {
	ID                 string `json:"id"`
	UserID             int64  `json:"user_id"`
	APIKeyID           int64  `json:"api_key_id"`
	Model              string `json:"model"`
	Endpoint           string `json:"endpoint"`
	Stream             bool   `json:"stream"`
	Status             string `json:"status"`
	HTTPStatus         int    `json:"http_status"`
	ClientDisconnected bool   `json:"client_disconnected"`
	UpstreamFinished   bool   `json:"upstream_finished"`
	UsageSource        string `json:"usage_source"`
	UsageTrust         string `json:"usage_trust"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	ReasoningTokens    int64  `json:"reasoning_tokens"`
	// CachedInputTokens is the subset of InputTokens that hit the provider's
	// prompt cache (reported by upstream usage, 0 when unknown).
	CachedInputTokens int64 `json:"cached_input_tokens"`
	CostNano          int64 `json:"cost_nano"`
	ProviderCostNano  int64 `json:"provider_cost_nano"`
	ReservedNano      int64 `json:"reserved_nano"`
	PriceVersion      int64 `json:"price_version"`
	// PriceVersionID is the snapshotted model_price_versions row (0 for
	// requests admitted before versioning existed).
	PriceVersionID int64  `json:"price_version_id"`
	LatencyMS      int64  `json:"latency_ms"`
	FirstTokenMS   int64  `json:"first_token_ms"`
	CreatedAt      int64  `json:"created_at"`
	FinishedAt     int64  `json:"finished_at"`
	ErrorMessage   string `json:"error_message"`
	TraceID        string `json:"trace_id"`
	// HasTools marks requests carrying tool schemas. Provider-side tool
	// scaffolding inflates prompt tokens, so such samples must not drive the
	// input calibration factor used for abort billing.
	HasTools bool `json:"-"`
}

type UsageEvent struct {
	ID                int64  `json:"id"`
	RequestID         string `json:"request_id"`
	Attempt           int    `json:"attempt"`
	Final             bool   `json:"final"`
	Source            string `json:"source"`
	Trust             string `json:"trust"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	ReasoningTokens   int64  `json:"reasoning_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	SnapshotSeq       int64  `json:"snapshot_seq"`
	LocalTokensAtSnap int64  `json:"local_tokens_at_snapshot"`
	LocalOutputTokens int64  `json:"local_output_tokens"`
	DetailJSON        string `json:"detail_json"`
	CreatedAt         int64  `json:"created_at"`
}

type LedgerEntry struct {
	ID               int64  `json:"id"`
	RequestID        string `json:"request_id"`
	UserID           int64  `json:"user_id"`
	EntryType        string `json:"entry_type"`
	AmountNano       int64  `json:"amount_nano"`
	BalanceAfterNano int64  `json:"balance_after_nano"`
	Memo             string `json:"memo"`
	CreatedAt        int64  `json:"created_at"`
}

type SettleInput struct {
	RequestID          string
	Status             string
	HTTPStatus         int
	ClientDisconnected bool
	UpstreamFinished   bool
	Usage              UsageEvent
	CostNano           int64
	ProviderCostNano   int64
	LatencyMS          int64
	FirstTokenMS       int64
	ErrorMessage       string
}

func Open(cfg *config.Config) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + cfg.DBPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`SELECT 1`); err != nil {
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	if err := s.seed(cfg); err != nil {
		return nil, err
	}
	if err := s.ensurePriceVersions(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'user',
  display_name TEXT NOT NULL DEFAULT '',
  balance_nano INTEGER NOT NULL DEFAULT 0,
  status INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  prefix TEXT NOT NULL,
  suffix TEXT NOT NULL,
  status INTEGER NOT NULL DEFAULT 1,
  rpm INTEGER NOT NULL DEFAULT 60,
  tpm INTEGER NOT NULL DEFAULT 0,
  concurrent INTEGER NOT NULL DEFAULT 0,
  max_output INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS models (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  upstream_name TEXT NOT NULL,
  family TEXT NOT NULL,
  input_price INTEGER NOT NULL,
  output_price INTEGER NOT NULL,
  provider_input_price INTEGER NOT NULL,
  provider_output_price INTEGER NOT NULL,
  cached_input_price INTEGER NOT NULL DEFAULT 0,
  provider_cached_input_price INTEGER NOT NULL DEFAULT 0,
  context_window INTEGER NOT NULL DEFAULT 0,
  max_output INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS requests (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  api_key_id INTEGER NOT NULL,
  model TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  stream INTEGER NOT NULL,
  status TEXT NOT NULL,
  http_status INTEGER NOT NULL DEFAULT 0,
  client_disconnected INTEGER NOT NULL DEFAULT 0,
  upstream_finished INTEGER NOT NULL DEFAULT 0,
  usage_source TEXT NOT NULL DEFAULT '',
  usage_trust TEXT NOT NULL DEFAULT '',
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  cached_input_tokens INTEGER NOT NULL DEFAULT 0,
  cost_nano INTEGER NOT NULL DEFAULT 0,
  provider_cost_nano INTEGER NOT NULL DEFAULT 0,
  reserved_nano INTEGER NOT NULL DEFAULT 0,
  price_version INTEGER NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  first_token_ms INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  trace_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS requests_user_created ON requests(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS requests_created ON requests(created_at DESC);
CREATE TABLE IF NOT EXISTS usage_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL,
  attempt INTEGER NOT NULL DEFAULT 1,
  final INTEGER NOT NULL DEFAULT 1,
  source TEXT NOT NULL,
  trust TEXT NOT NULL,
  input_tokens INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  reasoning_tokens INTEGER NOT NULL,
  cached_input_tokens INTEGER NOT NULL DEFAULT 0,
  snapshot_seq INTEGER NOT NULL DEFAULT 0,
  local_tokens_at_snapshot INTEGER NOT NULL DEFAULT 0,
  local_output_tokens INTEGER NOT NULL DEFAULT 0,
  detail_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  UNIQUE(request_id, attempt, final)
);
CREATE TABLE IF NOT EXISTS ledger_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL DEFAULT '',
  user_id INTEGER NOT NULL,
  entry_type TEXT NOT NULL,
  amount_nano INTEGER NOT NULL,
  balance_after_nano INTEGER NOT NULL,
  memo TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ledger_user ON ledger_entries(user_id, id DESC);
CREATE TABLE IF NOT EXISTS calibrations (
  model TEXT NOT NULL,
  direction TEXT NOT NULL,
  k REAL NOT NULL DEFAULT 1.0,
  samples INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(model, direction)
);
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS model_price_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  model_id TEXT NOT NULL,
  input_price INTEGER NOT NULL,
  output_price INTEGER NOT NULL,
  provider_input_price INTEGER NOT NULL,
  provider_output_price INTEGER NOT NULL,
  cached_input_price INTEGER NOT NULL DEFAULT 0,
  provider_cached_input_price INTEGER NOT NULL DEFAULT 0,
  actor TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS model_price_versions_model ON model_price_versions(model_id, id DESC);
CREATE TRIGGER IF NOT EXISTS model_price_versions_append_only
BEFORE UPDATE ON model_price_versions BEGIN SELECT RAISE(ABORT, 'model_price_versions is append-only'); END;
CREATE TRIGGER IF NOT EXISTS model_price_versions_no_delete
BEFORE DELETE ON model_price_versions BEGIN SELECT RAISE(ABORT, 'model_price_versions is append-only'); END;
CREATE TABLE IF NOT EXISTS audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_id INTEGER NOT NULL DEFAULT 0,
  actor_name TEXT NOT NULL DEFAULT '',
  actor_type TEXT NOT NULL DEFAULT 'user',
  action TEXT NOT NULL,
  resource TEXT NOT NULL,
  resource_id TEXT NOT NULL DEFAULT '',
  before_json TEXT NOT NULL DEFAULT '',
  after_json TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_created ON audit_events(id DESC);
CREATE INDEX IF NOT EXISTS audit_events_resource ON audit_events(resource, resource_id);
CREATE TRIGGER IF NOT EXISTS audit_events_append_only
BEFORE UPDATE ON audit_events BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;
CREATE TRIGGER IF NOT EXISTS audit_events_no_delete
BEFORE DELETE ON audit_events BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;
`)
	if err != nil {
		return err
	}
	for _, c := range []struct{ table, column, ddl string }{
		{"models", "cached_input_price", "INTEGER NOT NULL DEFAULT 0"},
		{"models", "provider_cached_input_price", "INTEGER NOT NULL DEFAULT 0"},
		{"models", "price_version_id", "INTEGER NOT NULL DEFAULT 0"},
		{"requests", "cached_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"requests", "price_version_id", "INTEGER NOT NULL DEFAULT 0"},
		{"usage_events", "cached_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"api_keys", "tpm", "INTEGER NOT NULL DEFAULT 0"},
		{"api_keys", "concurrent", "INTEGER NOT NULL DEFAULT 0"},
		{"api_keys", "max_output", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn(c.table, c.column, c.ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureColumn(table, column, ddl string) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('`+table+`') WHERE name=?`, column).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.DB.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + ddl)
	return err
}

func (s *Store) seed(cfg *config.Config) error {
	users := []struct {
		username, password, role, display string
		balance                           int64
	}{
		{cfg.AdminUser, cfg.AdminPassword, "admin", "Administrator", 100 * 100_000_000},
		{cfg.DemoUser, cfg.DemoPassword, "user", "Demo User", 100 * 100_000_000},
	}
	for _, u := range users {
		var count int
		if err := s.DB.QueryRow(`SELECT COUNT(1) FROM users WHERE username=?`, u.username).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(u.password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err := s.DB.Exec(`INSERT INTO users(username,password_hash,role,display_name,balance_nano,status,created_at) VALUES(?,?,?,?,?,1,?)`,
			u.username, string(hash), u.role, u.display, u.balance, nowMS()); err != nil {
			return err
		}
	}

	models := []Model{
		{ID: "deepseek-chat", DisplayName: "DeepSeek Chat", UpstreamName: cfg.DeepSeekModel, Family: "deepseek", InputPrice: 120, OutputPrice: 480, ProviderInputPrice: 100, ProviderOutputPrice: 400, CachedInputPrice: 3, ProviderCachedInputPrice: 2, ContextWindow: 128000, MaxOutput: 8192, Enabled: true},
		{ID: "deepseek-reasoner", DisplayName: "DeepSeek Reasoner", UpstreamName: cfg.DeepSeekReasonerModel, Family: "deepseek", InputPrice: 120, OutputPrice: 480, ProviderInputPrice: 100, ProviderOutputPrice: 400, CachedInputPrice: 3, ProviderCachedInputPrice: 2, ContextWindow: 128000, MaxOutput: 8192, Enabled: true},
		{ID: "kimi-k3.1-pro", DisplayName: "Kimi K3.1 Pro", UpstreamName: cfg.KimiModel, Family: "kimi", InputPrice: 1600, OutputPrice: 8200, ProviderInputPrice: 1300, ProviderOutputPrice: 6800, CachedInputPrice: 160, ProviderCachedInputPrice: 130, ContextWindow: 256000, MaxOutput: 8192, Enabled: true},
	}
	for _, m := range models {
		if _, err := s.DB.Exec(`INSERT INTO models(id,display_name,upstream_name,family,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,context_window,max_output,enabled,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET upstream_name=excluded.upstream_name`, m.ID, m.DisplayName, m.UpstreamName, m.Family, m.InputPrice, m.OutputPrice, m.ProviderInputPrice, m.ProviderOutputPrice, m.CachedInputPrice, m.ProviderCachedInputPrice, m.ContextWindow, m.MaxOutput, boolInt(m.Enabled), nowMS()); err != nil {
			return err
		}
	}
	return s.applyPricingV2(models)
}

// ensurePriceVersions gives every model that predates rate-card versioning one
// baseline version so request snapshots can always resolve a row.
func (s *Store) ensurePriceVersions() error {
	type pending struct {
		id   string
		p    PriceTick
		when int64
	}
	rows, err := s.DB.Query(`SELECT id,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,updated_at FROM models WHERE price_version_id=0`)
	if err != nil {
		return err
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.p.InputPrice, &p.p.OutputPrice, &p.p.ProviderInputPrice, &p.p.ProviderOutputPrice, &p.p.CachedInputPrice, &p.p.ProviderCachedInputPrice, &p.when); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range todo {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO model_price_versions(model_id,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,actor,note,created_at)
VALUES(?,?,?,?,?,?,?,'system','baseline: prices in effect before rate-card versioning',?)`,
			p.id, p.p.InputPrice, p.p.OutputPrice, p.p.ProviderInputPrice, p.p.ProviderOutputPrice, p.p.CachedInputPrice, p.p.ProviderCachedInputPrice, p.when)
		if err != nil {
			tx.Rollback()
			return err
		}
		vid, _ := res.LastInsertId()
		if _, err := tx.Exec(`UPDATE models SET price_version_id=? WHERE id=?`, vid, p.id); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// applyPricingV2 moves an existing database from the placeholder prices used
// during bring-up to the official list prices observed on 2026-10-29. It runs
// once per database so later admin price edits are not overwritten on restart.
func (s *Store) applyPricingV2(models []Model) error {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM meta WHERE key='pricing_version'`).Scan(&v)
	if err == nil && v == "2" {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, m := range models {
		if _, err := s.DB.Exec(`UPDATE models SET input_price=?,output_price=?,provider_input_price=?,provider_output_price=?,cached_input_price=?,provider_cached_input_price=?,updated_at=? WHERE id=?`,
			m.InputPrice, m.OutputPrice, m.ProviderInputPrice, m.ProviderOutputPrice, m.CachedInputPrice, m.ProviderCachedInputPrice, nowMS(), m.ID); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(`INSERT INTO meta(key,value) VALUES('pricing_version','2') ON CONFLICT(key) DO UPDATE SET value=excluded.value`)
	return err
}

func nowMS() int64 { return time.Now().UnixMilli() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(`SELECT id,username,password_hash,role,display_name,balance_nano,status,created_at FROM users WHERE username=?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.BalanceNano, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) GetUser(id int64) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(`SELECT id,username,password_hash,role,display_name,balance_nano,status,created_at FROM users WHERE id=?`, id).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.BalanceNano, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateUser(username, password, role, display string, balance int64, actor Actor) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO users(username,password_hash,role,display_name,balance_nano,status,created_at) VALUES(?,?,?,?,?,1,?)`,
		username, string(hash), role, display, balance, nowMS())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrConflict
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	after := map[string]any{"username": username, "role": role, "display_name": display, "balance_nano": balance}
	if err := insertAudit(tx, actor, "user.create", "user", strconv.FormatInt(id, 10), nil, after, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUser(id)
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.DB.Query(`SELECT id,username,'',role,display_name,balance_nano,status,created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &u.BalanceNano, &u.Status, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) TopUp(userID, amount int64, memo string, actor Actor) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET balance_nano=balance_nano+? WHERE id=?`, amount, userID); err != nil {
		return 0, err
	}
	var bal int64
	if err := tx.QueryRow(`SELECT balance_nano FROM users WHERE id=?`, userID).Scan(&bal); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO ledger_entries(request_id,user_id,entry_type,amount_nano,balance_after_nano,memo,created_at) VALUES('',?,?,?,?,?,?)`,
		userID, "topup", amount, bal, memo, nowMS()); err != nil {
		return 0, err
	}
	delta := map[string]any{"amount_nano": amount, "balance_after_nano": bal, "memo": memo}
	if err := insertAudit(tx, actor, "user.topup", "user", strconv.FormatInt(userID, 10), nil, delta, memo); err != nil {
		return 0, err
	}
	return bal, tx.Commit()
}

func (s *Store) CreateSession(userID int64, tokenHash string, expiresAt int64) error {
	_, err := s.DB.Exec(`INSERT INTO sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`, tokenHash, userID, expiresAt, nowMS())
	return err
}

func (s *Store) GetSession(tokenHash string) (int64, error) {
	var userID, exp int64
	err := s.DB.QueryRow(`SELECT user_id,expires_at FROM sessions WHERE token_hash=?`, tokenHash).Scan(&userID, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err == nil && exp < nowMS() {
		_, _ = s.DB.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
		return 0, ErrNotFound
	}
	return userID, err
}

func (s *Store) DeleteSession(tokenHash string) {
	_, _ = s.DB.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
}

func (s *Store) ListKeys(userID int64) ([]APIKey, error) {
	rows, err := s.DB.Query(`SELECT id,user_id,name,key_hash,prefix,suffix,status,rpm,tpm,concurrent,max_output,created_at,last_used_at FROM api_keys WHERE user_id=? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.KeyHash, &k.Prefix, &k.Suffix, &k.Status, &k.RPM, &k.TPM, &k.Concurrent, &k.MaxOutput, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) CreateKey(userID int64, name, keyHash, prefix, suffix string, limits KeyLimits, actor Actor) (*APIKey, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO api_keys(user_id,name,key_hash,prefix,suffix,status,rpm,tpm,concurrent,max_output,created_at) VALUES(?,?,?,?,?,1,?,?,?,?,?)`,
		userID, name, keyHash, prefix, suffix, limits.RPM, limits.TPM, limits.Concurrent, limits.MaxOutput, nowMS())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	// The hash and the plaintext secret never reach the audit log.
	after := map[string]any{"user_id": userID, "name": name, "prefix": prefix, "suffix": suffix,
		"rpm": limits.RPM, "tpm": limits.TPM, "concurrent": limits.Concurrent, "max_output": limits.MaxOutput}
	if err := insertAudit(tx, actor, "key.create", "api_key", strconv.FormatInt(id, 10), nil, after, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	k := &APIKey{}
	err = s.DB.QueryRow(`SELECT id,user_id,name,key_hash,prefix,suffix,status,rpm,tpm,concurrent,max_output,created_at,last_used_at FROM api_keys WHERE id=?`, id).
		Scan(&k.ID, &k.UserID, &k.Name, &k.KeyHash, &k.Prefix, &k.Suffix, &k.Status, &k.RPM, &k.TPM, &k.Concurrent, &k.MaxOutput, &k.CreatedAt, &k.LastUsedAt)
	return k, err
}

func (s *Store) RevokeKey(userID, keyID int64, actor Actor) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE api_keys SET status=0 WHERE id=? AND user_id=?`, keyID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	before := map[string]any{"status": 1}
	after := map[string]any{"status": 0}
	if err := insertAudit(tx, actor, "key.revoke", "api_key", strconv.FormatInt(keyID, 10), before, after, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetKeyByHash(hash string) (*APIKey, error) {
	k := &APIKey{}
	err := s.DB.QueryRow(`SELECT id,user_id,name,key_hash,prefix,suffix,status,rpm,tpm,concurrent,max_output,created_at,last_used_at FROM api_keys WHERE key_hash=?`, hash).
		Scan(&k.ID, &k.UserID, &k.Name, &k.KeyHash, &k.Prefix, &k.Suffix, &k.Status, &k.RPM, &k.TPM, &k.Concurrent, &k.MaxOutput, &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return k, err
}

func (s *Store) TouchKey(id int64) {
	_, _ = s.DB.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=?`, nowMS(), id)
}

func (s *Store) ListModels(onlyEnabled bool) ([]Model, error) {
	q := `SELECT id,display_name,upstream_name,family,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,context_window,max_output,enabled,updated_at,price_version_id FROM models`
	if onlyEnabled {
		q += ` WHERE enabled=1`
	}
	q += ` ORDER BY id`
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		var enabled int
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.UpstreamName, &m.Family, &m.InputPrice, &m.OutputPrice, &m.ProviderInputPrice, &m.ProviderOutputPrice, &m.CachedInputPrice, &m.ProviderCachedInputPrice, &m.ContextWindow, &m.MaxOutput, &enabled, &m.UpdatedAt, &m.PriceVersionID); err != nil {
			return nil, err
		}
		m.Enabled = enabled == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetModel(id string) (*Model, error) {
	m := &Model{}
	var enabled int
	err := s.DB.QueryRow(`SELECT id,display_name,upstream_name,family,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,context_window,max_output,enabled,updated_at,price_version_id FROM models WHERE id=?`, id).
		Scan(&m.ID, &m.DisplayName, &m.UpstreamName, &m.Family, &m.InputPrice, &m.OutputPrice, &m.ProviderInputPrice, &m.ProviderOutputPrice, &m.CachedInputPrice, &m.ProviderCachedInputPrice, &m.ContextWindow, &m.MaxOutput, &enabled, &m.UpdatedAt, &m.PriceVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	m.Enabled = enabled == 1
	return m, err
}

// UpdateModelPrices applies a price edit. Price changes append a new
// model_price_versions row inside the same transaction as the models update, so
// a version can never disagree with the row it describes. A change limited to
// `enabled` reuses the current version.
func (s *Store) UpdateModelPrices(id string, p PriceTick, enabled bool, actor Actor, note string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	cur := Model{}
	var enabledInt int
	err = tx.QueryRow(`SELECT id,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,enabled,updated_at,price_version_id FROM models WHERE id=?`, id).
		Scan(&cur.ID, &cur.InputPrice, &cur.OutputPrice, &cur.ProviderInputPrice, &cur.ProviderOutputPrice, &cur.CachedInputPrice, &cur.ProviderCachedInputPrice, &enabledInt, &cur.UpdatedAt, &cur.PriceVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	cur.Enabled = enabledInt == 1
	old := PriceTick{
		InputPrice: cur.InputPrice, OutputPrice: cur.OutputPrice,
		ProviderInputPrice: cur.ProviderInputPrice, ProviderOutputPrice: cur.ProviderOutputPrice,
		CachedInputPrice: cur.CachedInputPrice, ProviderCachedInputPrice: cur.ProviderCachedInputPrice,
	}

	versionID := cur.PriceVersionID
	if old != p {
		res, err := tx.Exec(`INSERT INTO model_price_versions(model_id,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,actor,note,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?)`,
			id, p.InputPrice, p.OutputPrice, p.ProviderInputPrice, p.ProviderOutputPrice, p.CachedInputPrice, p.ProviderCachedInputPrice, actorLabel(actor), note, nowMS())
		if err != nil {
			return 0, err
		}
		versionID, _ = res.LastInsertId()
	}

	if _, err := tx.Exec(`UPDATE models SET input_price=?,output_price=?,provider_input_price=?,provider_output_price=?,cached_input_price=?,provider_cached_input_price=?,enabled=?,updated_at=?,price_version_id=? WHERE id=?`,
		p.InputPrice, p.OutputPrice, p.ProviderInputPrice, p.ProviderOutputPrice, p.CachedInputPrice, p.ProviderCachedInputPrice, boolInt(enabled), nowMS(), versionID, id); err != nil {
		return 0, err
	}

	before := map[string]any{"prices": old, "enabled": cur.Enabled, "price_version_id": cur.PriceVersionID}
	after := map[string]any{"prices": p, "enabled": enabled, "price_version_id": versionID}
	if err := insertAudit(tx, actor, "model.prices.update", "model", id, before, after, note); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return versionID, nil
}

func (s *Store) ListModelPriceVersions(modelID string, limit int) ([]ModelPriceVersion, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT id,model_id,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,actor,note,created_at
FROM model_price_versions WHERE model_id=? ORDER BY id DESC LIMIT ?`, modelID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelPriceVersion{}
	for rows.Next() {
		var v ModelPriceVersion
		if err := rows.Scan(&v.ID, &v.ModelID, &v.Price.InputPrice, &v.Price.OutputPrice, &v.Price.ProviderInputPrice, &v.Price.ProviderOutputPrice, &v.Price.CachedInputPrice, &v.Price.ProviderCachedInputPrice, &v.Actor, &v.Note, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetModelPriceVersion(id int64) (*ModelPriceVersion, error) {
	v := &ModelPriceVersion{}
	err := s.DB.QueryRow(`SELECT id,model_id,input_price,output_price,provider_input_price,provider_output_price,cached_input_price,provider_cached_input_price,actor,note,created_at FROM model_price_versions WHERE id=?`, id).
		Scan(&v.ID, &v.ModelID, &v.Price.InputPrice, &v.Price.OutputPrice, &v.Price.ProviderInputPrice, &v.Price.ProviderOutputPrice, &v.Price.CachedInputPrice, &v.Price.ProviderCachedInputPrice, &v.Actor, &v.Note, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

func actorLabel(a Actor) string {
	switch {
	case a.Name != "":
		return a.Name
	case a.ID != 0:
		return fmt.Sprintf("user:%d", a.ID)
	default:
		return "system"
	}
}

func insertAudit(tx *sql.Tx, a Actor, action, resource, resourceID string, before, after any, reason string) error {
	beforeJSON, err := jsonText(before)
	if err != nil {
		return err
	}
	afterJSON, err := jsonText(after)
	if err != nil {
		return err
	}
	actorType := a.Type
	if actorType == "" {
		actorType = "user"
	}
	_, err = tx.Exec(`INSERT INTO audit_events(actor_id,actor_name,actor_type,action,resource,resource_id,before_json,after_json,reason,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		a.ID, actorLabel(a), actorType, action, resource, resourceID, beforeJSON, afterJSON, reason, nowMS())
	return err
}

func jsonText(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ListAuditEvents returns the newest audit rows, optionally filtered by
// resource/action, with keyset pagination via beforeID.
type AuditFilter struct {
	Resource string
	Action   string
	Query    string
	BeforeID int64
	Limit    int
}

func (s *Store) ListAuditEvents(f AuditFilter) ([]AuditEvent, error) {
	where := []string{"1=1"}
	var args []any
	if f.Resource != "" {
		where = append(where, "resource=?")
		args = append(args, f.Resource)
	}
	if f.Action != "" {
		where = append(where, "action=?")
		args = append(args, f.Action)
	}
	if f.Query != "" {
		pat := likeContains(f.Query)
		where = append(where, `(resource_id LIKE ? ESCAPE '\' OR actor_name LIKE ? ESCAPE '\' OR reason LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat, pat)
	}
	if f.BeforeID > 0 {
		where = append(where, "id<?")
		args = append(args, f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := s.DB.Query(`SELECT id,actor_id,actor_name,actor_type,action,resource,resource_id,before_json,after_json,reason,created_at
FROM audit_events WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.ActorType, &e.Action, &e.Resource, &e.ResourceID, &e.BeforeJSON, &e.AfterJSON, &e.Reason, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Reserve(userID int64, requestID string, amount int64) error {
	if amount <= 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET balance_nano=balance_nano-? WHERE id=? AND balance_nano>=?`, amount, userID, amount)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInsufficient
	}
	var bal int64
	if err := tx.QueryRow(`SELECT balance_nano FROM users WHERE id=?`, userID).Scan(&bal); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO ledger_entries(request_id,user_id,entry_type,amount_nano,balance_after_nano,memo,created_at) VALUES(?,?,?,?,?,?,?)`,
		requestID, userID, "reserve", -amount, bal, "reserve at admission", nowMS()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateRequest(r *Request) error {
	_, err := s.DB.Exec(`INSERT INTO requests(id,user_id,api_key_id,model,endpoint,stream,status,http_status,client_disconnected,upstream_finished,usage_source,usage_trust,input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cost_nano,provider_cost_nano,reserved_nano,price_version,price_version_id,latency_ms,first_token_ms,created_at,finished_at,error_message,trace_id)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.UserID, r.APIKeyID, r.Model, r.Endpoint, boolInt(r.Stream), r.Status, r.HTTPStatus,
		boolInt(r.ClientDisconnected), boolInt(r.UpstreamFinished), r.UsageSource, r.UsageTrust,
		r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CachedInputTokens, r.CostNano, r.ProviderCostNano, r.ReservedNano,
		r.PriceVersion, r.PriceVersionID, r.LatencyMS, r.FirstTokenMS, r.CreatedAt, r.FinishedAt, r.ErrorMessage, r.TraceID)
	return err
}

// CreateReservedRequest admits a request and moves its budget reservation in
// one transaction, so a failed insert cannot leak a debit.
func (s *Store) CreateReservedRequest(r *Request) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.ReservedNano > 0 {
		res, err := tx.Exec(`UPDATE users SET balance_nano=balance_nano-? WHERE id=? AND balance_nano>=?`, r.ReservedNano, r.UserID, r.ReservedNano)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrInsufficient
		}
		var bal int64
		if err := tx.QueryRow(`SELECT balance_nano FROM users WHERE id=?`, r.UserID).Scan(&bal); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ledger_entries(request_id,user_id,entry_type,amount_nano,balance_after_nano,memo,created_at) VALUES(?,?,?,?,?,?,?)`,
			r.ID, r.UserID, "reserve", -r.ReservedNano, bal, "reserve at admission", nowMS()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO requests(id,user_id,api_key_id,model,endpoint,stream,status,http_status,client_disconnected,upstream_finished,usage_source,usage_trust,input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cost_nano,provider_cost_nano,reserved_nano,price_version,price_version_id,latency_ms,first_token_ms,created_at,finished_at,error_message,trace_id)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.UserID, r.APIKeyID, r.Model, r.Endpoint, boolInt(r.Stream), r.Status, r.HTTPStatus,
		boolInt(r.ClientDisconnected), boolInt(r.UpstreamFinished), r.UsageSource, r.UsageTrust,
		r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CachedInputTokens, r.CostNano, r.ProviderCostNano, r.ReservedNano,
		r.PriceVersion, r.PriceVersionID, r.LatencyMS, r.FirstTokenMS, r.CreatedAt, r.FinishedAt, r.ErrorMessage, r.TraceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkStreaming(id string) {
	_, _ = s.DB.Exec(`UPDATE requests SET status='streaming' WHERE id=? AND status='reserved'`, id)
}

// Settle is idempotent: the status transition guards the ledger write.
func (s *Store) Settle(in SettleInput) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var userID, reserved int64
	var status string
	err = tx.QueryRow(`SELECT user_id,reserved_nano,status FROM requests WHERE id=?`, in.RequestID).Scan(&userID, &reserved, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if status != "reserved" && status != "streaming" {
		return false, nil
	}

	delta := reserved - in.CostNano // positive means refund
	res, err := tx.Exec(`UPDATE users SET balance_nano=balance_nano+? WHERE id=?`, delta, userID)
	if err != nil {
		return false, err
	}
	_ = res
	var bal int64
	if err := tx.QueryRow(`SELECT balance_nano FROM users WHERE id=?`, userID).Scan(&bal); err != nil {
		return false, err
	}

	entryType := "settle"
	if in.CostNano == 0 {
		entryType = "release"
	}
	if delta != 0 || in.CostNano == 0 {
		if _, err := tx.Exec(`INSERT INTO ledger_entries(request_id,user_id,entry_type,amount_nano,balance_after_nano,memo,created_at) VALUES(?,?,?,?,?,?,?)`,
			in.RequestID, userID, entryType, delta, bal, in.Status, nowMS()); err != nil {
			return false, err
		}
	}

	e := in.Usage
	if _, err := tx.Exec(`INSERT INTO usage_events(request_id,attempt,final,source,trust,input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,snapshot_seq,local_tokens_at_snapshot,local_output_tokens,detail_json,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(request_id,attempt,final) DO NOTHING`,
		in.RequestID, 1, 1, e.Source, e.Trust, e.InputTokens, e.OutputTokens, e.ReasoningTokens, e.CachedInputTokens,
		e.SnapshotSeq, e.LocalTokensAtSnap, e.LocalOutputTokens, e.DetailJSON, nowMS()); err != nil {
		return false, err
	}

	res, err = tx.Exec(`UPDATE requests SET status=?,http_status=?,client_disconnected=?,upstream_finished=?,usage_source=?,usage_trust=?,input_tokens=?,output_tokens=?,reasoning_tokens=?,cached_input_tokens=?,cost_nano=?,provider_cost_nano=?,latency_ms=?,first_token_ms=?,finished_at=?,error_message=? WHERE id=? AND status IN ('reserved','streaming')`,
		in.Status, in.HTTPStatus, boolInt(in.ClientDisconnected), boolInt(in.UpstreamFinished), e.Source, e.Trust,
		e.InputTokens, e.OutputTokens, e.ReasoningTokens, e.CachedInputTokens, in.CostNano, in.ProviderCostNano, in.LatencyMS, in.FirstTokenMS, nowMS(), in.ErrorMessage, in.RequestID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// RequestFilter narrows a request listing. Query matches request id or trace
// id by substring; KeyID matches the api key that produced the request.
type RequestFilter struct {
	UserID int64
	Limit  int
	Offset int
	Model  string
	Status string
	Query  string
	KeyID  int64
}

func likeContains(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func (s *Store) ListRequests(f RequestFilter) ([]Request, int64, error) {
	where := []string{"user_id=?"}
	args := []any{f.UserID}
	if f.Model != "" {
		where = append(where, "model=?")
		args = append(args, f.Model)
	}
	if f.Status != "" {
		where = append(where, "status=?")
		args = append(args, f.Status)
	}
	if f.Query != "" {
		where = append(where, `(id LIKE ? ESCAPE '\' OR trace_id LIKE ? ESCAPE '\')`)
		pat := likeContains(f.Query)
		args = append(args, pat, pat)
	}
	if f.KeyID > 0 {
		where = append(where, "api_key_id=?")
		args = append(args, f.KeyID)
	}
	cond := strings.Join(where, " AND ")
	limit, offset := f.Limit, f.Offset
	var total int64
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM requests WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT id,user_id,api_key_id,model,endpoint,stream,status,http_status,client_disconnected,upstream_finished,usage_source,usage_trust,input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cost_nano,provider_cost_nano,reserved_nano,price_version,price_version_id,latency_ms,first_token_ms,created_at,finished_at,error_message,trace_id FROM requests WHERE ` + cond + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		var stream, cd, uf int
		if err := rows.Scan(&r.ID, &r.UserID, &r.APIKeyID, &r.Model, &r.Endpoint, &stream, &r.Status, &r.HTTPStatus, &cd, &uf, &r.UsageSource, &r.UsageTrust, &r.InputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.CachedInputTokens, &r.CostNano, &r.ProviderCostNano, &r.ReservedNano, &r.PriceVersion, &r.PriceVersionID, &r.LatencyMS, &r.FirstTokenMS, &r.CreatedAt, &r.FinishedAt, &r.ErrorMessage, &r.TraceID); err != nil {
			return nil, 0, err
		}
		r.Stream = stream == 1
		r.ClientDisconnected = cd == 1
		r.UpstreamFinished = uf == 1
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (s *Store) GetRequest(userID int64, id string) (*Request, error) {
	r := &Request{}
	var stream, cd, uf int
	err := s.DB.QueryRow(`SELECT id,user_id,api_key_id,model,endpoint,stream,status,http_status,client_disconnected,upstream_finished,usage_source,usage_trust,input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,cost_nano,provider_cost_nano,reserved_nano,price_version,price_version_id,latency_ms,first_token_ms,created_at,finished_at,error_message,trace_id FROM requests WHERE id=? AND user_id=?`, id, userID).
		Scan(&r.ID, &r.UserID, &r.APIKeyID, &r.Model, &r.Endpoint, &stream, &r.Status, &r.HTTPStatus, &cd, &uf, &r.UsageSource, &r.UsageTrust, &r.InputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.CachedInputTokens, &r.CostNano, &r.ProviderCostNano, &r.ReservedNano, &r.PriceVersion, &r.PriceVersionID, &r.LatencyMS, &r.FirstTokenMS, &r.CreatedAt, &r.FinishedAt, &r.ErrorMessage, &r.TraceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.Stream, r.ClientDisconnected, r.UpstreamFinished = stream == 1, cd == 1, uf == 1
	return r, err
}

func (s *Store) UsageForRequest(requestID string) ([]UsageEvent, []LedgerEntry, error) {
	rows, err := s.DB.Query(`SELECT id,request_id,attempt,final,source,trust,input_tokens,output_tokens,reasoning_tokens,cached_input_tokens,snapshot_seq,local_tokens_at_snapshot,local_output_tokens,detail_json,created_at FROM usage_events WHERE request_id=? ORDER BY id`, requestID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var events []UsageEvent
	for rows.Next() {
		var e UsageEvent
		var final int
		if err := rows.Scan(&e.ID, &e.RequestID, &e.Attempt, &final, &e.Source, &e.Trust, &e.InputTokens, &e.OutputTokens, &e.ReasoningTokens, &e.CachedInputTokens, &e.SnapshotSeq, &e.LocalTokensAtSnap, &e.LocalOutputTokens, &e.DetailJSON, &e.CreatedAt); err != nil {
			return nil, nil, err
		}
		e.Final = final == 1
		events = append(events, e)
	}
	lrows, err := s.DB.Query(`SELECT id,request_id,user_id,entry_type,amount_nano,balance_after_nano,memo,created_at FROM ledger_entries WHERE request_id=? ORDER BY id`, requestID)
	if err != nil {
		return nil, nil, err
	}
	defer lrows.Close()
	var ledger []LedgerEntry
	for lrows.Next() {
		var l LedgerEntry
		if err := lrows.Scan(&l.ID, &l.RequestID, &l.UserID, &l.EntryType, &l.AmountNano, &l.BalanceAfterNano, &l.Memo, &l.CreatedAt); err != nil {
			return nil, nil, err
		}
		ledger = append(ledger, l)
	}
	return events, ledger, nil
}

type Summary struct {
	TodayCostNano int64        `json:"today_cost_nano"`
	TodayRequests int64        `json:"today_requests"`
	TodayInput    int64        `json:"today_input_tokens"`
	TodayOutput   int64        `json:"today_output_tokens"`
	TotalCostNano int64        `json:"total_cost_nano"`
	TotalRequests int64        `json:"total_requests"`
	Daily         []DailyUsage `json:"daily"`
}

type DailyUsage struct {
	Date        string `json:"date"`
	Requests    int64  `json:"requests"`
	CostNano    int64  `json:"cost_nano"`
	InputToken  int64  `json:"input_tokens"`
	OutputToken int64  `json:"output_tokens"`
}

func (s *Store) Summary(userID int64, days int) (*Summary, error) {
	sum := &Summary{}
	now := time.Now()
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	_ = s.DB.QueryRow(`SELECT COALESCE(SUM(cost_nano),0),COUNT(1),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0) FROM requests WHERE user_id=? AND created_at>=?`, userID, startToday).
		Scan(&sum.TodayCostNano, &sum.TodayRequests, &sum.TodayInput, &sum.TodayOutput)
	_ = s.DB.QueryRow(`SELECT COALESCE(SUM(cost_nano),0),COUNT(1) FROM requests WHERE user_id=?`, userID).
		Scan(&sum.TotalCostNano, &sum.TotalRequests)

	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	rows, err := s.DB.Query(`SELECT date(created_at/1000,'unixepoch','localtime') d,COUNT(1),COALESCE(SUM(cost_nano),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0)
FROM requests WHERE user_id=? AND created_at>=? GROUP BY d`, userID, start.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]DailyUsage{}
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Date, &d.Requests, &d.CostNano, &d.InputToken, &d.OutputToken); err != nil {
			return nil, err
		}
		byDay[d.Date] = d
	}
	for i := 0; i < days; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		d := byDay[date]
		d.Date = date
		sum.Daily = append(sum.Daily, d)
	}
	return sum, nil
}

type AnalyticsTotals struct {
	Requests          int64 `json:"requests"`
	CostNano          int64 `json:"cost_nano"`
	ProviderCostNano  int64 `json:"provider_cost_nano"`
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	Failed            int64 `json:"failed"`
	Disconnected      int64 `json:"disconnected"`
	AvgLatencyMS      int64 `json:"avg_latency_ms"`
	AvgFirstTokenMS   int64 `json:"avg_first_token_ms"`
}

type AnalyticsDaily struct {
	Date              string `json:"date"`
	Requests          int64  `json:"requests"`
	CostNano          int64  `json:"cost_nano"`
	ProviderCostNano  int64  `json:"provider_cost_nano"`
	InputTokens       int64  `json:"input_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	Failed            int64  `json:"failed"`
}

type ModelUsage struct {
	Model             string `json:"model"`
	Requests          int64  `json:"requests"`
	CostNano          int64  `json:"cost_nano"`
	ProviderCostNano  int64  `json:"provider_cost_nano"`
	InputTokens       int64  `json:"input_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	AvgLatencyMS      int64  `json:"avg_latency_ms"`
}

type StatusUsage struct {
	Status   string `json:"status"`
	Requests int64  `json:"requests"`
	CostNano int64  `json:"cost_nano"`
}

type KeyUsage struct {
	APIKeyID int64  `json:"api_key_id"`
	Name     string `json:"name"`
	Prefix   string `json:"prefix"`
	Suffix   string `json:"suffix"`
	Requests int64  `json:"requests"`
	CostNano int64  `json:"cost_nano"`
}

type Analytics struct {
	Days     int              `json:"days"`
	Since    int64            `json:"since"`
	Totals   AnalyticsTotals  `json:"totals"`
	Daily    []AnalyticsDaily `json:"daily"`
	Models   []ModelUsage     `json:"models"`
	Statuses []StatusUsage    `json:"statuses"`
	Keys     []KeyUsage       `json:"keys"`
}

// Analytics aggregates requests over the trailing window (including today).
// Scoped to one user; provider cost is included so the console can show margin
// for admins, callers decide whether to expose it.
func (s *Store) Analytics(userID int64, days int) (*Analytics, error) {
	if days <= 0 {
		days = 7
	}
	now := time.Now()
	startDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	since := startDay.UnixMilli()
	a := &Analytics{
		Days:     days,
		Since:    since,
		Daily:    []AnalyticsDaily{},
		Models:   []ModelUsage{},
		Statuses: []StatusUsage{},
		Keys:     []KeyUsage{},
	}

	err := s.DB.QueryRow(`SELECT COUNT(1),COALESCE(SUM(cost_nano),0),COALESCE(SUM(provider_cost_nano),0),
COALESCE(SUM(input_tokens),0),COALESCE(SUM(cached_input_tokens),0),COALESCE(SUM(output_tokens),0),
COALESCE(SUM(CASE WHEN status IN ('upstream_error','client_aborted') OR http_status>=400 THEN 1 ELSE 0 END),0),
COALESCE(SUM(client_disconnected),0)
FROM requests WHERE user_id=? AND created_at>=?`, userID, since).
		Scan(&a.Totals.Requests, &a.Totals.CostNano, &a.Totals.ProviderCostNano,
			&a.Totals.InputTokens, &a.Totals.CachedInputTokens, &a.Totals.OutputTokens,
			&a.Totals.Failed, &a.Totals.Disconnected)
	if err != nil {
		return nil, err
	}
	_ = s.DB.QueryRow(`SELECT COALESCE(CAST(AVG(latency_ms) AS INTEGER),0),COALESCE(CAST(AVG(NULLIF(first_token_ms,0)) AS INTEGER),0)
FROM requests WHERE user_id=? AND created_at>=? AND latency_ms>0`, userID, since).
		Scan(&a.Totals.AvgLatencyMS, &a.Totals.AvgFirstTokenMS)

	rows, err := s.DB.Query(`SELECT date(created_at/1000,'unixepoch','localtime') d,COUNT(1),COALESCE(SUM(cost_nano),0),
COALESCE(SUM(provider_cost_nano),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(cached_input_tokens),0),
COALESCE(SUM(output_tokens),0),COALESCE(SUM(CASE WHEN status IN ('upstream_error','client_aborted') OR http_status>=400 THEN 1 ELSE 0 END),0)
FROM requests WHERE user_id=? AND created_at>=? GROUP BY d`, userID, since)
	if err != nil {
		return nil, err
	}
	byDay := map[string]AnalyticsDaily{}
	for rows.Next() {
		var d AnalyticsDaily
		if err := rows.Scan(&d.Date, &d.Requests, &d.CostNano, &d.ProviderCostNano, &d.InputTokens, &d.CachedInputTokens, &d.OutputTokens, &d.Failed); err != nil {
			rows.Close()
			return nil, err
		}
		byDay[d.Date] = d
	}
	rows.Close()
	for i := 0; i < days; i++ {
		date := startDay.AddDate(0, 0, i).Format("2006-01-02")
		d := byDay[date]
		d.Date = date
		a.Daily = append(a.Daily, d)
	}

	mrows, err := s.DB.Query(`SELECT model,COUNT(1),COALESCE(SUM(cost_nano),0),COALESCE(SUM(provider_cost_nano),0),
COALESCE(SUM(input_tokens),0),COALESCE(SUM(cached_input_tokens),0),COALESCE(SUM(output_tokens),0),
COALESCE(CAST(AVG(NULLIF(latency_ms,0)) AS INTEGER),0)
FROM requests WHERE user_id=? AND created_at>=? GROUP BY model ORDER BY SUM(cost_nano) DESC`, userID, since)
	if err != nil {
		return nil, err
	}
	for mrows.Next() {
		var m ModelUsage
		if err := mrows.Scan(&m.Model, &m.Requests, &m.CostNano, &m.ProviderCostNano, &m.InputTokens, &m.CachedInputTokens, &m.OutputTokens, &m.AvgLatencyMS); err != nil {
			mrows.Close()
			return nil, err
		}
		a.Models = append(a.Models, m)
	}
	mrows.Close()

	srows, err := s.DB.Query(`SELECT status,COUNT(1),COALESCE(SUM(cost_nano),0) FROM requests WHERE user_id=? AND created_at>=? GROUP BY status ORDER BY COUNT(1) DESC`, userID, since)
	if err != nil {
		return nil, err
	}
	for srows.Next() {
		var u StatusUsage
		if err := srows.Scan(&u.Status, &u.Requests, &u.CostNano); err != nil {
			srows.Close()
			return nil, err
		}
		a.Statuses = append(a.Statuses, u)
	}
	srows.Close()

	krows, err := s.DB.Query(`SELECT r.api_key_id,COALESCE(k.name,''),COALESCE(k.prefix,''),COALESCE(k.suffix,''),COUNT(1),COALESCE(SUM(r.cost_nano),0)
FROM requests r LEFT JOIN api_keys k ON k.id=r.api_key_id
WHERE r.user_id=? AND r.created_at>=? GROUP BY r.api_key_id ORDER BY COUNT(1) DESC`, userID, since)
	if err != nil {
		return nil, err
	}
	for krows.Next() {
		var u KeyUsage
		if err := krows.Scan(&u.APIKeyID, &u.Name, &u.Prefix, &u.Suffix, &u.Requests, &u.CostNano); err != nil {
			krows.Close()
			return nil, err
		}
		a.Keys = append(a.Keys, u)
	}
	return a, krows.Err()
}

type CalibrationSample struct {
	Model     string  `json:"model"`
	Direction string  `json:"direction"`
	K         float64 `json:"k"`
	Samples   int64   `json:"samples"`
	UpdatedAt int64   `json:"updated_at"`
}

func (s *Store) Calibrations() ([]CalibrationSample, error) {
	rows, err := s.DB.Query(`SELECT model,direction,k,samples,updated_at FROM calibrations ORDER BY model,direction`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalibrationSample
	for rows.Next() {
		var c CalibrationSample
		if err := rows.Scan(&c.Model, &c.Direction, &c.K, &c.Samples, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Wallet(userID int64, limit, offset int) ([]LedgerEntry, int64, error) {
	var total int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM ledger_entries WHERE user_id=?`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT id,request_id,user_id,entry_type,amount_nano,balance_after_nano,memo,created_at FROM ledger_entries WHERE user_id=? ORDER BY id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var l LedgerEntry
		if err := rows.Scan(&l.ID, &l.RequestID, &l.UserID, &l.EntryType, &l.AmountNano, &l.BalanceAfterNano, &l.Memo, &l.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

func (s *Store) Calibration(model, direction string) (float64, int64) {
	var k float64
	var samples int64
	err := s.DB.QueryRow(`SELECT k,samples FROM calibrations WHERE model=? AND direction=?`, model, direction).Scan(&k, &samples)
	if err != nil || k <= 0 {
		return 1.0, 0
	}
	return k, samples
}

func (s *Store) UpdateCalibration(model, direction string, ratio float64) {
	if ratio <= 0 {
		return
	}
	k, samples := s.Calibration(model, direction)
	if k < 0.7 {
		k = 0.7
	}
	if k > 1.8 {
		k = 1.8
	}
	next := k
	if samples > 0 {
		next = 0.7*k + 0.3*ratio
	} else {
		next = 0.7 + 0.3*ratio
	}
	if next < 0.7 {
		next = 0.7
	}
	if next > 1.8 {
		next = 1.8
	}
	_, _ = s.DB.Exec(`INSERT INTO calibrations(model,direction,k,samples,updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(model,direction) DO UPDATE SET k=excluded.k,samples=excluded.samples,updated_at=excluded.updated_at`,
		model, direction, next, samples+1, nowMS())
}

func (s *Store) CountRequestsToday() (int64, error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(1) FROM requests WHERE created_at>=?`, start).Scan(&n)
	return n, err
}

func (s *Store) String() string { return fmt.Sprintf("store(%p)", s.DB) }
