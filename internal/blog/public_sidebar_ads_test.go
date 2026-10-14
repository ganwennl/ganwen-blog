package blog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func publicAdSettings(t *testing.T, app *App) SiteSettings {
	t.Helper()
	settings, err := app.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.SidebarAds = sidebarAdsFixture()
	settings.ArticleSidebarEnabled = true
	settings.SidebarAds.Items[0].Title = "AD_SEQUENCE_ZERO"
	settings.SidebarAds.Items[1].Title = "AD_SEQUENCE_ONE"
	if err := app.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func publicAdRequest(t *testing.T, app *App, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://www.ganwen.nl"+path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	app.handlePublic(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("public ad request %d: %s", w.Code, w.Body.String())
	}
	return w
}

func publicAdCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == publicSidebarAdCookieName {
			return cookie
		}
	}
	t.Fatal("selected advertisement did not emit its cookie")
	return nil
}

func TestPublicSidebarAdsRefreshSequenceAndCookiePolicy(t *testing.T) {
	app, _ := analyticsFixture(t)
	settings := publicAdSettings(t, app)
	app.Config.Preview = false
	var cookie *http.Cookie
	for index, want := range []string{"0", "1", "0"} {
		w := publicAdRequest(t, app, http.MethodGet, "/?p=42", cookie)
		cookie = publicAdCookie(t, w)
		if cookie.Value != want || cookie.Path != "/" || cookie.MaxAge != 30*24*60*60 || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("refresh %d cookie %+v", index, cookie)
		}
		selected, other := settings.SidebarAds.Items[0].Title, settings.SidebarAds.Items[1].Title
		if want == "1" {
			selected, other = other, selected
		}
		body := w.Body.String()
		if !strings.Contains(body, selected) || strings.Contains(body, other) || strings.Count(body, "data-sidebar-ad") != 1 || strings.Contains(body, "data-ad-carousel") || strings.Contains(body, "data-ad-interval") {
			t.Fatalf("refresh %d rendered more than the selected advertisement", index)
		}
		if w.Header().Get("Cache-Control") != "private, no-cache" || !strings.Contains(w.Header().Get("Vary"), "Cookie") {
			t.Fatalf("personalized advertisement was publicly cacheable %+v", w.Header())
		}
	}
	app.Config.Preview = true
	if cookie := publicAdCookie(t, publicAdRequest(t, app, http.MethodGet, "/?p=42", nil)); cookie.Secure {
		t.Fatal("HTTP preview requires a non-Secure index cookie")
	}
}

func TestPublicSidebarAdsHeadDoesNotConsumeSequence(t *testing.T) {
	app, _ := analyticsFixture(t)
	_ = publicAdSettings(t, app)
	first := publicAdCookie(t, publicAdRequest(t, app, http.MethodGet, "/?p=42", nil))
	for _, cookie := range []*http.Cookie{nil, first} {
		w := publicAdRequest(t, app, http.MethodHead, "/?p=42", cookie)
		if w.Header().Get("Set-Cookie") != "" || w.Body.Len() != 0 {
			t.Fatal("HEAD consumed the sequence or returned a body")
		}
	}
	if next := publicAdCookie(t, publicAdRequest(t, app, http.MethodGet, "/?p=42", first)); next.Value != "1" {
		t.Fatalf("HEAD advanced the next GET: %+v", next)
	}
}

func TestPublicSidebarAdsDisabledEmptyAndInvalidDoNotSetCookie(t *testing.T) {
	app, _ := analyticsFixture(t)
	settings := publicAdSettings(t, app)
	for _, test := range []struct {
		name string
		ads  SidebarAdsConfig
	}{
		{"disabled", SidebarAdsConfig{Items: settings.SidebarAds.Items}},
		{"empty", SidebarAdsConfig{Enabled: true}},
		{"invalid image", SidebarAdsConfig{Enabled: true, Items: []SidebarAd{{Title: "UNSAFE_AD", ImageURL: "javascript:alert(1)", TargetURL: "https://example.com"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings.SidebarAds = test.ads
			r := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl/?p=42", nil)
			r.AddCookie(&http.Cookie{Name: publicSidebarAdCookieName, Value: "1"})
			w, view := httptest.NewRecorder(), publicView{SidebarAd: &SidebarAd{Title: "previous"}}
			app.populatePublicSidebarAd(w, r, &view, settings)
			if view.SidebarAd != nil || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("unavailable ad mutated cookie or retained content: %+v %+v", view.SidebarAd, w.Header())
			}
		})
	}
}

func TestPublicSidebarAdsBadCookieAndChangingGallery(t *testing.T) {
	app, _ := analyticsFixture(t)
	settings := publicAdSettings(t, app)
	for _, value := range []string{"", "bad", "-1", "+1", "1.5", "18446744073709551616"} {
		t.Run("bad-"+value, func(t *testing.T) {
			w := publicAdRequest(t, app, http.MethodGet, "/?p=42", &http.Cookie{Name: publicSidebarAdCookieName, Value: value})
			if cookie := publicAdCookie(t, w); cookie.Value != "0" || !strings.Contains(w.Body.String(), settings.SidebarAds.Items[0].Title) {
				t.Fatalf("bad index did not reset: %+v", cookie)
			}
		})
	}
	for _, test := range []struct{ value, want string }{{"2", "1"}, {"5", "0"}, {"18446744073709551615", "0"}} {
		w := publicAdRequest(t, app, http.MethodGet, "/?p=42", &http.Cookie{Name: publicSidebarAdCookieName, Value: test.value})
		if cookie := publicAdCookie(t, w); cookie.Value != test.want {
			t.Fatalf("old/out-of-range index %s: %+v", test.value, cookie)
		}
	}
	settings.SidebarAds.Items[0], settings.SidebarAds.Items[1] = settings.SidebarAds.Items[1], settings.SidebarAds.Items[0]
	if err := app.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	w := publicAdRequest(t, app, http.MethodGet, "/?p=42", &http.Cookie{Name: publicSidebarAdCookieName, Value: "0"})
	if cookie := publicAdCookie(t, w); cookie.Value != "1" || !strings.Contains(w.Body.String(), settings.SidebarAds.Items[1].Title) {
		t.Fatal("reordered gallery did not select its current index")
	}
	settings.SidebarAds.Items = settings.SidebarAds.Items[:1]
	if err := app.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "1", "999"} {
		w := publicAdRequest(t, app, http.MethodGet, "/?p=42", &http.Cookie{Name: publicSidebarAdCookieName, Value: value})
		if cookie := publicAdCookie(t, w); cookie.Value != "0" || !strings.Contains(w.Body.String(), settings.SidebarAds.Items[0].Title) {
			t.Fatal("single advertisement changed or old index caused an error")
		}
	}
}

func TestPublicSidebarAdsRTLAndNoDatabaseSideEffects(t *testing.T) {
	app, _ := analyticsFixture(t)
	settings := publicAdSettings(t, app)
	post, err := app.Store.GetPost(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	post.Translations = map[string]Translation{"ar": {Title: "مقال", ContentHTML: "<p>محتوى</p>", Status: "publish", SourceHash: SourceHash(post)}}
	sidebarSave(t, app, post)
	before, err := app.Store.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w := publicAdRequest(t, app, http.MethodGet, "/ar/?p=42", nil)
	body := w.Body.String()
	if !strings.Contains(body, `<html lang="ar" dir="rtl"`) || !strings.Contains(body, settings.SidebarAds.Items[0].Title) || strings.Contains(body, settings.SidebarAds.Items[1].Title) {
		t.Fatal("RTL article did not render one selected advertisement")
	}
	after, err := app.Store.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before.CreatedAt, after.CreatedAt = "", ""
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rendering an advertisement changed settings, posts or analytics")
	}
	// The index cookie alone grants no administrator access.
	r := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl/admin/api/settings", nil)
	r.AddCookie(publicAdCookie(t, w))
	adminResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(adminResponse, r)
	if adminResponse.Code != http.StatusUnauthorized {
		t.Fatalf("advertisement index affected authentication: %d", adminResponse.Code)
	}
}
