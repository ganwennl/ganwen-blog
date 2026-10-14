package blog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func authAppRequest(t *testing.T, app *App, jar http.CookieJar, method, target string, value any, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if value != nil {
		var err error
		raw, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(raw))
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if jar != nil {
		for _, cookie := range jar.Cookies(u) {
			r.AddCookie(cookie)
		}
	}
	if method != http.MethodGet {
		r.Header.Set("Origin", u.Scheme+"://"+u.Host)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if jar != nil {
		jar.SetCookies(u, w.Result().Cookies())
	}
	return w
}

func authSessionResponse(t *testing.T, w *httptest.ResponseRecorder) (bool, string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("session request status %d", w.Code)
	}
	var response struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Authenticated, response.CSRF
}

func storedTestAdminSessions(t *testing.T, store *Store) map[string]storedAdminSession {
	t.Helper()
	rows, err := store.db.Query("SELECT `key`,value FROM kv WHERE `key`>=? AND `key`<?", adminSessionKeyPrefix, adminSessionKeyPrefix+"~")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	sessions := map[string]storedAdminSession{}
	for rows.Next() {
		var key, raw string
		var session storedAdminSession
		if err := rows.Scan(&key, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &session); err != nil {
			t.Fatal(err)
		}
		sessions[key] = session
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return sessions
}

func TestAdminSessionSurvivesDatabaseAndApplicationRestart(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	target := c.server.URL + "/admin/api/session"
	u, _ := url.Parse(target)
	var bearer string
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for _, cookie := range c.client.Jar.Cookies(u) {
		if cookie.Name == adminCookieName(sessionCookie, r, c.app.Config) {
			bearer = cookie.Value
		}
	}
	if len(bearer) != 43 {
		t.Fatal("missing session bearer")
	}
	hash := adminHash(bearer)
	if _, found := storedTestAdminSessions(t, c.app.Store)[adminSessionKeyPrefix+hash]; !found {
		t.Fatal("database must store only a digest of the bearer")
	}
	backup, err := c.app.Store.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	backupJSON, _ := json.Marshal(backup)
	for _, secret := range []string{bearer, hash, c.csrf} {
		if bytes.Contains(backupJSON, []byte(secret)) {
			t.Fatal("content database backup included administrator login state")
		}
	}
	// Close both resources, then reopen the same on-disk database and origin.
	c.server.Close()
	if err := c.app.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(filepath.Join(c.app.Config.DataDir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	app, err := NewApp(reopened, c.app.Config)
	if err != nil {
		t.Fatal(err)
	}
	authenticated, csrf := authSessionResponse(t, authAppRequest(t, app, c.client.Jar, http.MethodGet, target, nil, ""))
	if !authenticated || csrf != c.csrf {
		t.Fatal("restart discarded valid session or CSRF token")
	}
	if w := authAppRequest(t, app, c.client.Jar, http.MethodGet, c.server.URL+"/admin/api/posts", nil, ""); w.Code != 200 {
		t.Fatal("restored session cannot access protected editing API")
	}
	if w := authAppRequest(t, app, c.client.Jar, http.MethodPost, c.server.URL+"/admin/api/logout", nil, "wrong-csrf"); w.Code != 403 {
		t.Fatal("restored session bypassed CSRF")
	}
	// A second app must observe revocation immediately and after another restart.
	other := NewAdmin(app)
	if w := authAppRequest(t, app, c.client.Jar, http.MethodPost, c.server.URL+"/admin/api/logout", nil, csrf); w.Code != 200 {
		t.Fatal("logout failed")
	}
	r.AddCookie(&http.Cookie{Name: adminCookieName(sessionCookie, r, app.Config), Value: bearer})
	for _, auth := range []*adminAuth{other.auth, NewAdmin(app).auth} {
		if _, ok, err := auth.sessionState(r); ok || err != nil {
			t.Fatal("replayed logged-out bearer survived revocation")
		}
	}
}

func TestAdminSessionLifetimesAndCredentialChanges(t *testing.T) {
	for _, remember := range []bool{false, true} {
		name := "default-eight-hours"
		lifetime := adminSessionLifetime
		if remember {
			name = "remember-thirty-days"
			lifetime = rememberedAdminSessionLifetime
		}
		t.Run(name, func(t *testing.T) {
			c := adminTestSetup(t, true)
			w := authAppRequest(t, c.app, c.client.Jar, http.MethodPost, c.server.URL+"/admin/api/login", map[string]any{"username": "editor", "password": "test-only-secret", "remember": remember}, c.csrf)
			if w.Code != 200 {
				t.Fatalf("login status %d", w.Code)
			}
			var sessionCookieFound bool
			for _, cookie := range w.Result().Cookies() {
				if strings.HasPrefix(cookie.Name, sessionCookie) {
					sessionCookieFound = true
					if cookie.MaxAge != int(lifetime/time.Second) || cookie.Expires.IsZero() || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/admin/" {
						t.Fatal("wrong session lifetime or cookie flags")
					}
				}
			}
			if !sessionCookieFound {
				t.Fatal("missing session cookie")
			}
			var expires int64
			for _, session := range storedTestAdminSessions(t, c.app.Store) {
				expires = session.ExpiresAt
			}
			remaining := time.Until(time.Unix(expires, 0))
			if remaining < lifetime-5*time.Second || remaining > lifetime {
				t.Fatal("cookie/server expiration diverged")
			}
			newHash, _ := bcrypt.GenerateFromPassword([]byte("changed-test-password"), bcrypt.MinCost)
			for _, change := range []string{"password", "username", "disabled"} {
				cfg := c.app.Config
				switch change {
				case "password":
					cfg.AdminPasswordHash = string(newHash)
				case "username":
					cfg.AdminUser = "other-editor"
				case "disabled":
					cfg.AdminPasswordHash = ""
				}
				changed, err := NewApp(c.app.Store, cfg)
				if err != nil {
					t.Fatal(err)
				}
				if w := authAppRequest(t, changed, c.client.Jar, http.MethodGet, c.server.URL+"/admin/api/posts", nil, ""); w.Code != 401 {
					t.Fatalf("%s change accepted previous credentials", change)
				}
			}
		})
	}
}

func TestAdminPreviewPortsKeepIndependentCookiesAndSessions(t *testing.T) {
	c := adminTestSetup(t, true)
	jar, _ := cookiejar.New(nil)
	origins := []string{"http://127.0.0.1:8097", "http://127.0.0.1:8098"}
	var names []string
	for _, origin := range origins {
		_, csrf := authSessionResponse(t, authAppRequest(t, c.app, jar, http.MethodGet, origin+"/admin/api/session", nil, ""))
		w := authAppRequest(t, c.app, jar, http.MethodPost, origin+"/admin/api/login", map[string]string{"username": "editor", "password": "test-only-secret"}, csrf)
		if w.Code != 200 {
			t.Fatal("preview login failed")
		}
		for _, cookie := range w.Result().Cookies() {
			if strings.HasPrefix(cookie.Name, sessionCookie) {
				names = append(names, cookie.Name)
			}
		}
	}
	if len(names) != 2 || names[0] == names[1] {
		t.Fatal("preview session cookie names collide across ports")
	}
	for _, origin := range origins {
		if authenticated, _ := authSessionResponse(t, authAppRequest(t, c.app, jar, http.MethodGet, origin+"/admin/api/session", nil, "")); !authenticated {
			t.Fatal("second preview login overwrote first preview session")
		}
	}
	firstURL, _ := url.Parse(origins[0] + "/admin/api/session")
	var firstToken string
	for _, cookie := range jar.Cookies(firstURL) {
		if cookie.Name == names[0] {
			firstToken = cookie.Value
		}
	}
	forgedJar, _ := cookiejar.New(nil)
	secondURL, _ := url.Parse(origins[1] + "/admin/api/session")
	forgedJar.SetCookies(secondURL, []*http.Cookie{{Name: names[1], Value: firstToken, Path: "/admin/"}})
	if w := authAppRequest(t, c.app, forgedJar, http.MethodGet, origins[1]+"/admin/api/posts", nil, ""); w.Code != 401 {
		t.Fatal("renamed cookie crossed origin binding")
	}
	production := c.app.Config
	production.Preview = false
	r := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl/admin/api/session", nil)
	if adminCookieName(sessionCookie, r, production) != sessionCookie || adminCookieName(loginCookie, r, production) != loginCookie {
		t.Fatal("production cookie naming unexpectedly changed")
	}
}

func TestAdminSessionStorageFailureDoesNotMasqueradeAsLogout(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	if err := c.app.Store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"session", "editor-session", "posts"} {
		if code, body := c.request(http.MethodGet, path, nil, "", ""); code != 503 || bytes.Contains(body, []byte(`"authenticated":false`)) {
			t.Fatalf("storage failure returned false logout for %s: %d", path, code)
		}
	}
}

func TestDatabaseRestoreNeverReactivatesAdminSessions(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	backup, err := c.app.Store.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Even a manually augmented/old backup must not restore login records.
	for key, stored := range storedTestAdminSessions(t, c.app.Store) {
		raw, _ := json.Marshal(stored)
		backup.Values = append(backup.Values, DatabaseValue{Key: key, Value: string(raw)})
	}
	destination, err := OpenStore(filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = destination.Close() })
	if err := destination.RestoreDatabase(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	if len(storedTestAdminSessions(t, destination)) != 0 {
		t.Fatal("database restore reactivated a login session")
	}
	app, err := NewApp(destination, c.app.Config)
	if err != nil {
		t.Fatal(err)
	}
	if w := authAppRequest(t, app, c.client.Jar, http.MethodGet, c.server.URL+"/admin/api/posts", nil, ""); w.Code != 401 {
		t.Fatal("old cookie authenticated against restored content database")
	}
}
