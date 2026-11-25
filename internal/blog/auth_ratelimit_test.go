package blog

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAdminLoginChallengeIsStatelessAndCannotBeExhausted(t *testing.T) {
	auth := newAdminAuth(Config{Preview: true})
	var cookie *http.Cookie
	var csrf string
	for i := 0; i < 5000; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://example.test/admin/api/session", nil)
		got, err := auth.issueLogin(w, r, auth.config)
		if err != nil {
			t.Fatalf("challenge %d refused: %v", i, err)
		}
		cookie, csrf = w.Result().Cookies()[0], got
	}
	login := func(value, token string) bool {
		r := httptest.NewRequest("POST", "http://example.test/admin/api/login", nil)
		r.AddCookie(&http.Cookie{Name: cookie.Name, Value: value})
		r.Header.Set("X-CSRF-Token", token)
		return auth.loginCSRF(r)
	}
	if !login(cookie.Value, csrf) {
		t.Fatal("valid login challenge rejected")
	}
	// Re-requesting the session with the cookie keeps the same CSRF.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://example.test/admin/api/session", nil)
	r.AddCookie(cookie)
	if again, _ := auth.issueLogin(w, r, auth.config); again != csrf || len(w.Result().Cookies()) != 0 {
		t.Fatal("existing challenge was not reused")
	}
	nonce, _, _ := strings.Cut(cookie.Value, ".")
	expired := nonce + "." + strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10)
	forged := nonce + "." + strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	for _, bad := range [][2]string{{cookie.Value, "fake-token"}, {expired, csrf}, {forged, csrf}, {"", csrf}, {"x.1", csrf}} {
		if login(bad[0], bad[1]) {
			t.Fatalf("invalid login challenge accepted: %q", bad[0])
		}
	}
	if other := newAdminAuth(Config{Preview: true}); func() bool {
		r := httptest.NewRequest("POST", "http://example.test/admin/api/login", nil)
		r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		r.Header.Set("X-CSRF-Token", csrf)
		return other.loginCSRF(r)
	}() {
		t.Fatal("challenge accepted under a different signing key")
	}
}

func TestAdminLoginRateGroupsIPv6AndSurvivesFullTable(t *testing.T) {
	auth := newAdminAuth(Config{Preview: true})
	attempt := func(addr string) bool {
		r := httptest.NewRequest("POST", "http://example.test/admin/api/login", nil)
		r.RemoteAddr = addr
		return auth.rateAllowed(r)
	}
	allowed := 0
	for i := 0; i < 20; i++ {
		if attempt("[2001:db8:1:2::" + strconv.FormatInt(int64(i+1), 16) + "]:443") {
			allowed++
		}
	}
	if allowed != 6 {
		t.Fatalf("one IPv6 /64 admitted %d attempts", allowed)
	}
	if !attempt("[2001:db8:1:3::1]:443") {
		t.Fatal("neighbouring /64 was blocked")
	}
	for i := 0; len(auth.attempts) < 4096; i++ {
		attempt("10." + strconv.Itoa(i/65536%256) + "." + strconv.Itoa(i/256%256) + "." + strconv.Itoa(i%256) + ":1")
	}
	if !attempt("198.51.100.7:1") {
		t.Fatal("full table locked out an unseen client")
	}
	overflow := 1
	for i := 0; i < 50; i++ {
		if attempt("198.51.100." + strconv.Itoa(10+i) + ":1") {
			overflow++
		}
	}
	if overflow != 30 {
		t.Fatalf("overflow bucket admitted %d attempts", overflow)
	}
}

func TestAdminLoginRateUsesCloudflareClientIP(t *testing.T) {
	auth := newAdminAuth(Config{Preview: false})
	cfAttempt := func(edgeIP, cfIP, cfIPv6 string) bool {
		r := httptest.NewRequest("POST", "https://example.test/admin/api/login", nil)
		r.RemoteAddr = "127.0.0.1:41234"
		r.Header.Set("X-Real-IP", edgeIP)
		if cfIP != "" {
			r.Header.Set("CF-Connecting-IP", cfIP)
		}
		if cfIPv6 != "" {
			r.Header.Set("CF-Connecting-IPv6", cfIPv6)
		}
		return auth.rateAllowed(r)
	}
	edges := []string{"173.245.48.10", "104.16.0.10", "108.162.192.10", "162.158.10.10", "172.64.10.10", "131.0.72.10", "190.93.240.10"}
	allowed := 0
	for _, edge := range edges {
		if cfAttempt(edge, "203.0.113.42", "") {
			allowed++
		}
	}
	if allowed != 6 {
		t.Fatalf("rotating Cloudflare edge IPs admitted %d attempts for one visitor, want 6", allowed)
	}
	if !cfAttempt("173.245.48.10", "203.0.113.43", "") {
		t.Fatal("different visitor behind same Cloudflare edge IP was blocked")
	}
	// IPv6 visitor via CF-Connecting-IPv6 groups by /64.
	v6Allowed := 0
	for i := 1; i <= 8; i++ {
		if cfAttempt("104.16.0.10", "", "2001:db8:42:1::"+strconv.Itoa(i)) {
			v6Allowed++
		}
	}
	if v6Allowed != 6 {
		t.Fatalf("Cloudflare IPv6 /64 admitted %d attempts, want 6", v6Allowed)
	}
	// Spoofed CF-Connecting-IP from a non-Cloudflare X-Real-IP must be ignored.
	spoofAllowed := 0
	for i := 1; i <= 8; i++ {
		if cfAttempt("198.51.100.5", "203.0.113."+strconv.Itoa(100+i), "") {
			spoofAllowed++
		}
	}
	if spoofAllowed != 6 {
		t.Fatalf("non-Cloudflare proxy spoofed CF-Connecting-IP admitted %d attempts, want 6", spoofAllowed)
	}
}

func TestAnalyticsDailyCapPerSource(t *testing.T) {
	app, _ := analyticsFixture(t)
	base := analyticsTestDate.Truncate(24 * time.Hour)
	minute := 0
	app.Analytics.now = func() time.Time { return base.Add(time.Duration(minute) * time.Minute) }
	request := func(addr string) bool {
		r := httptest.NewRequest("POST", "http://example.test/analytics/event", nil)
		r.RemoteAddr = addr
		return app.Analytics.rateAllowed(r)
	}
	allowed := 0
	for minute = 0; minute < 15; minute++ {
		for i := 0; i < 120; i++ {
			// Rotating addresses inside one /64 share the same quota.
			if request("[2001:db8:9:9::" + strconv.FormatInt(int64(minute*120+i+1), 16) + "]:1") {
				allowed++
			}
		}
	}
	if allowed != analyticsDailyPerSource {
		t.Fatalf("one /64 recorded %d events in a day", allowed)
	}
	if !request("203.0.113.9:1") {
		t.Fatal("another source was blocked")
	}
	minute = 24 * 60
	if !request("[2001:db8:9:9::1]:1") {
		t.Fatal("daily quota did not reset")
	}
}
