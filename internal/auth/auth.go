package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"teemohub/internal/store"
)

const SessionCookie = "th_session"

func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func NewAPIKey() (full, prefix, suffix, hash string) {
	body := RandomToken(32)
	full = "sk-th-" + body
	hash = HashToken(full)
	if len(full) > 12 {
		prefix, suffix = full[:10], full[len(full)-4:]
	}
	return
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func SetSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func SessionToken(r *http.Request) string {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func Bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

type Identity struct {
	User *store.User
}

func Authenticate(s *store.Store, r *http.Request) (*Identity, error) {
	token := SessionToken(r)
	if token == "" {
		return nil, store.ErrNotFound
	}
	userID, err := s.GetSession(HashToken(token))
	if err != nil {
		return nil, err
	}
	u, err := s.GetUser(userID)
	if err != nil {
		return nil, err
	}
	if u.Status != 1 {
		return nil, store.ErrNotFound
	}
	return &Identity{User: u}, nil
}
