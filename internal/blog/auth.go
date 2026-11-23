package blog

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "ganwen_admin_session"
const loginCookie = "ganwen_admin_login"
const adminSessionLifetime = 8 * time.Hour
const rememberedAdminSessionLifetime = 30 * 24 * time.Hour

type adminSession struct {
	CSRF    string
	Expires time.Time
}
type loginAttempt struct {
	Count int
	Until time.Time
}

type adminAuth struct {
	mu                 sync.Mutex
	store              *Store
	config             Config
	loginKey           []byte
	attempts           map[string]loginAttempt
	trustLoopbackProxy bool
}

func newAdminAuth(cfg Config) *adminAuth {
	// Login challenges are stateless (HMAC-signed), so anonymous requests
	// cannot exhaust server-side storage and block the owner's login.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &adminAuth{config: cfg, loginKey: key, attempts: make(map[string]loginAttempt), trustLoopbackProxy: !cfg.Preview}
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func secureRequest(r *http.Request, cfg Config) bool {
	u, _ := url.Parse(cfg.BaseURL)
	return r.TLS != nil || (!cfg.Preview && u != nil && u.Scheme == "https")
}
func setAdminCookie(w http.ResponseWriter, r *http.Request, cfg Config, name, value string, seconds int) {
	cookie := &http.Cookie{Name: adminCookieName(name, r, cfg), Value: value, Path: "/admin/", HttpOnly: true, Secure: secureRequest(r, cfg), SameSite: http.SameSiteStrictMode, MaxAge: seconds}
	if seconds > 0 {
		cookie.Expires = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	http.SetCookie(w, cookie)
}
func adminHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func adminCookieName(name string, r *http.Request, cfg Config) string {
	// Cookies do not isolate ports. Keep simultaneous local previews independent.
	if cfg.Preview {
		return name + "_" + adminHash(strings.ToLower(r.Host))[:12]
	}
	return name
}
func adminCredentialHash(cfg Config) string {
	return adminHash(cfg.AdminUser + "\x00" + cfg.AdminPasswordHash)
}
func adminOriginHash(r *http.Request, cfg Config) string {
	scheme := "http"
	if secureRequest(r, cfg) {
		scheme = "https"
	}
	return adminHash(scheme + "://" + strings.ToLower(r.Host))
}
func (a *adminAuth) cleanup(now time.Time) {
	for k, v := range a.attempts {
		if !now.Before(v.Until) {
			delete(a.attempts, k)
		}
	}
}
func (a *adminAuth) session(r *http.Request) (adminSession, bool) {
	s, ok, _ := a.sessionState(r)
	return s, ok
}
func (a *adminAuth) sessionState(r *http.Request) (adminSession, bool, error) {
	c, err := r.Cookie(adminCookieName(sessionCookie, r, a.config))
	if err != nil {
		return adminSession{}, false, nil
	}
	if len(c.Value) != 43 || !configuredAdmin(a.config) {
		return adminSession{}, false, nil
	}
	if a.store == nil {
		return adminSession{}, false, errors.New("session storage unavailable")
	}
	return a.store.adminSession(r.Context(), adminHash(c.Value), adminOriginHash(r, a.config), adminCredentialHash(a.config), time.Now())
}
func sameToken(got, want string) bool {
	return got != "" && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
func (a *adminAuth) loginIP(r *http.Request) string {
	h := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		h = host
	}
	peer := net.ParseIP(h)
	if peer != nil {
		h = peer.String()
	}
	// Production Nginx overwrites X-Real-IP. Only a loopback peer is trusted;
	// previews, direct remote clients, repeated headers, lists and IP:port fail closed.
	if a.trustLoopbackProxy && peer != nil && peer.IsLoopback() {
		values := r.Header.Values("X-Real-IP")
		if len(values) == 1 {
			if client := net.ParseIP(strings.TrimSpace(values[0])); client != nil {
				return client.String()
			}
		}
	}
	return h
}

// rateLimitKey groups IPv6 clients by /64: one subscriber normally owns a
// whole /64, so per-address keys would let it rotate addresses freely.
func rateLimitKey(ip string) string {
	if p := net.ParseIP(ip); p != nil && p.To4() == nil {
		return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return ip
}

const loginOverflowKey = "*overflow"

func (a *adminAuth) rateAllowed(r *http.Request) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.cleanup(now)
	key, limit := rateLimitKey(a.loginIP(r)), 6
	if _, seen := a.attempts[key]; !seen && len(a.attempts) >= 4096 {
		// A full table must not lock out unseen clients outright; they share
		// one small bucket instead, which still bounds password guessing.
		key, limit = loginOverflowKey, 30
	}
	v := a.attempts[key]
	if v.Count >= limit {
		return false
	}
	if v.Count == 0 {
		v.Until = now.Add(15 * time.Minute)
	}
	v.Count++
	a.attempts[key] = v
	return true
}
func (a *adminAuth) failedLogin(r *http.Request) {
	// Attempts are reserved before password hashing, including concurrent requests.
}

// loginChallengeCSRF validates a "<nonce>.<unix expiry>" login cookie and
// returns the CSRF token derived from it.
func (a *adminAuth) loginChallengeCSRF(value string, now time.Time) (string, bool) {
	nonce, rawExpiry, ok := strings.Cut(value, ".")
	if !ok || len(nonce) != 43 || len(rawExpiry) > 20 {
		return "", false
	}
	expiry, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil || now.Unix() >= expiry || expiry > now.Add(16*time.Minute).Unix() {
		return "", false
	}
	mac := hmac.New(sha256.New, a.loginKey)
	mac.Write([]byte("login\x00" + value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), true
}
func (a *adminAuth) issueLogin(w http.ResponseWriter, r *http.Request, cfg Config) (string, error) {
	now := time.Now()
	if c, err := r.Cookie(adminCookieName(loginCookie, r, cfg)); err == nil {
		if csrf, ok := a.loginChallengeCSRF(c.Value, now); ok {
			return csrf, nil
		}
	}
	nonce, err := randomToken()
	if err != nil {
		return "", err
	}
	value := nonce + "." + strconv.FormatInt(now.Add(15*time.Minute).Unix(), 10)
	csrf, _ := a.loginChallengeCSRF(value, now)
	setAdminCookie(w, r, cfg, loginCookie, value, 900)
	return csrf, nil
}
func (a *adminAuth) loginCSRF(r *http.Request) bool {
	c, err := r.Cookie(adminCookieName(loginCookie, r, a.config))
	if err != nil {
		return false
	}
	csrf, ok := a.loginChallengeCSRF(c.Value, time.Now())
	return ok && sameToken(r.Header.Get("X-CSRF-Token"), csrf)
}
func configuredAdmin(cfg Config) bool {
	if strings.TrimSpace(cfg.AdminUser) == "" || cfg.AdminPasswordHash == "" {
		return false
	}
	cost, err := bcrypt.Cost([]byte(cfg.AdminPasswordHash))
	return err == nil && cost >= bcrypt.MinCost && cost <= 16
}
func (a *adminAuth) authenticate(w http.ResponseWriter, r *http.Request, cfg Config, user, password string, remember bool) (string, error) {
	if !configuredAdmin(cfg) || !sameToken(user, cfg.AdminUser) {
		a.failedLogin(r)
		return "", errCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(cfg.AdminPasswordHash), []byte(password)); err != nil {
		a.failedLogin(r)
		return "", errCredentials
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanup(time.Now())
	lifetime := adminSessionLifetime
	if remember {
		lifetime = rememberedAdminSessionLifetime
	}
	previousHash := ""
	if c, e := r.Cookie(adminCookieName(sessionCookie, r, cfg)); e == nil {
		previousHash = adminHash(c.Value)
	}
	if a.store == nil {
		return "", errors.New("session storage unavailable")
	}
	session := adminSession{CSRF: csrf, Expires: time.Now().Add(lifetime)}
	if err := a.store.createAdminSession(r.Context(), adminHash(token), previousHash, adminOriginHash(r, cfg), adminCredentialHash(cfg), session); err != nil {
		return "", err
	}
	delete(a.attempts, rateLimitKey(a.loginIP(r)))
	setAdminCookie(w, r, cfg, sessionCookie, token, int(lifetime/time.Second))
	setAdminCookie(w, r, cfg, loginCookie, "", -1)
	return csrf, nil
}
func (a *adminAuth) logout(w http.ResponseWriter, r *http.Request, cfg Config) error {
	if c, err := r.Cookie(adminCookieName(sessionCookie, r, cfg)); err == nil {
		if err := a.store.revokeAdminSession(r.Context(), adminHash(c.Value)); err != nil {
			return err
		}
	}
	setAdminCookie(w, r, cfg, sessionCookie, "", -1)
	return nil
}
func sameOrigin(r *http.Request, cfg Config) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	raw := r.Header.Get("Origin")
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	scheme := "http"
	if secureRequest(r, cfg) {
		scheme = "https"
	}
	return u.Scheme == scheme
}
