package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ControlAddr    string
	GatewayAddr    string
	DBPath         string
	WebDir         string
	SessionSecret  string
	AdminUser      string
	AdminPassword  string
	DemoUser       string
	DemoPassword   string
	NewAPIBaseURL  string
	NewAPIToken    string
	Drain          time.Duration
	DrainMaxTokens int64
	DrainMaxBytes  int64
	UpstreamTime   time.Duration

	DeepSeekModel         string
	DeepSeekReasonerModel string
	KimiModel             string
}

func Load() (*Config, error) {
	loadDotEnv(".env")

	c := &Config{
		ControlAddr:    env("TEEMOHUB_CONTROL_ADDR", "127.0.0.1:18080"),
		GatewayAddr:    env("TEEMOHUB_GATEWAY_ADDR", "127.0.0.1:18081"),
		DBPath:         env("TEEMOHUB_DB_PATH", "./runtime/teemohub.db"),
		WebDir:         env("TEEMOHUB_WEB_DIR", "./web/dist"),
		SessionSecret:  env("TEEMOHUB_SESSION_SECRET", ""),
		AdminUser:      env("TEEMOHUB_ADMIN_USER", "admin"),
		AdminPassword:  env("TEEMOHUB_ADMIN_PASSWORD", ""),
		DemoUser:       env("TEEMOHUB_DEMO_USER", "demo"),
		DemoPassword:   env("TEEMOHUB_DEMO_PASSWORD", ""),
		NewAPIBaseURL:  strings.TrimRight(env("NEWAPI_BASE_URL", "http://127.0.0.1:3000"), "/"),
		NewAPIToken:    env("NEWAPI_TOKEN", ""),
		Drain:          time.Duration(envInt("TEEMOHUB_DRAIN_MS", 2000)) * time.Millisecond,
		DrainMaxTokens: int64(envInt("TEEMOHUB_DRAIN_MAX_TOKENS", 512)),
		DrainMaxBytes:  int64(envInt("TEEMOHUB_DRAIN_MAX_BYTES", 1<<20)),
		UpstreamTime:   envDuration("TEEMOHUB_UPSTREAM_TIMEOUT", 10*time.Minute),

		DeepSeekModel:         env("DEEPSEEK_MODEL", "deepseek-chat"),
		DeepSeekReasonerModel: env("DEEPSEEK_REASONER_MODEL", "deepseek-reasoner"),
		KimiModel:             env("KIMI_MODEL", "kimi-k3.1-pro"),
	}
	if c.SessionSecret == "" {
		return nil, fmt.Errorf("TEEMOHUB_SESSION_SECRET is required")
	}
	if c.NewAPIToken == "" {
		return nil, fmt.Errorf("NEWAPI_TOKEN is required (run scripts/bootstrap-newapi.sh)")
	}
	if c.AdminPassword == "" || c.DemoPassword == "" {
		return nil, fmt.Errorf("TEEMOHUB_ADMIN_PASSWORD and TEEMOHUB_DEMO_PASSWORD are required")
	}
	return c, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(k string, def int) int {
	v := env(k, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDuration(k string, def time.Duration) time.Duration {
	v := env(k, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

// loadDotEnv is deliberately small: KEY=VALUE, # comments, optional quotes.
// Existing environment variables always win, so `set -a; . .env` still works.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
