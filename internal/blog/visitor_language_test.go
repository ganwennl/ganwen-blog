package blog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVisitorLanguagePreferenceAndQuality(t *testing.T) {
	tests := []struct {
		name, cookie, accept, location string
	}{
		{"English cookie wins", "en", "nl;q=1,zh;q=0.9", "/en/"},
		{"Dutch cookie wins", "nl", "es-ES", "/nl/"},
		{"Spanish cookie wins", "es", "en-US", "/es/"},
		{"Chinese cookie wins", "zh", "en-US", ""},
		{"Case-insensitive cookie", "ES", "nl", "/es/"},
		{"Unsupported cookie ignored", "de", "nl-NL", "/nl/"},
		{"Regional cookie is not a choice", "en-US", "es-MX", "/es/"},
		{"Cookie cannot inject redirect", "https://evil.example", "en", "/en/"},
		{"Browser quality order", "", "en-US;q=0.4, nl-BE;q=0.9, es-ES;q=0.7", "/nl/"},
		{"Unweighted preference", "", "en-GB, nl;q=0.9", "/en/"},
		{"Spanish region", "", "es-AR", "/es/"},
		{"Chinese script and region", "", "zh-Hant-TW,en;q=0.9", ""},
		{"Uppercase region and quality", "", "NL-nl;Q=0.8,ES;q=0.7", "/nl/"},
		{"Equal quality preserves order", "", "es-MX;q=0.8,en-US;q=0.8,nl;q=0.8", "/es/"},
		{"Unknown top language skipped", "", "de-DE,ja;q=0.9,nl;q=0.8", "/nl/"},
		{"Unknown language defaults Chinese", "", "de-DE,ja-JP", ""},
		{"Empty defaults Chinese", "", "", ""},
		{"Wildcard defaults Chinese", "", "*", ""},
		{"Zero quality excluded", "", "nl;q=0,en;q=0.4", "/en/"},
		{"Negative quality excluded", "", "nl;q=-1,es;q=0.4", "/es/"},
		{"NaN excluded", "", "nl;q=NaN,en;q=0.1", "/en/"},
		{"Positive infinity excluded", "", "nl;q=+Inf,es;q=0.1", "/es/"},
		{"Negative infinity excluded", "", "nl;q=-Inf,en;q=0.1", "/en/"},
		{"Out of range quality excluded", "", "nl;q=1.1,es;q=0.5", "/es/"},
		{"Invalid quality excluded", "", "nl;q=high,en;q=0.5", "/en/"},
		{"Empty quality excluded", "", "nl;q=,es;q=0.5", "/es/"},
		{"Duplicate quality excluded", "", "nl;q=1;q=0,en;q=0.2", "/en/"},
		{"Unknown parameter excluded", "", "nl;level=1,en;q=0.2", "/en/"},
		{"Malformed regions excluded", "", "nl_NL,en--US,es/evil,zh-", ""},
		{"All rejected defaults Chinese", "", "en;q=0,nl;q=NaN,es;q=Inf", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := &App{}
			request := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl/", nil)
			request.Header.Set("Accept-Language", test.accept)
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: test.cookie})
			}
			response := httptest.NewRecorder()
			redirected := app.redirectVisitorLanguage(response, request)
			if redirected != (test.location != "") || response.Header().Get("Location") != test.location {
				t.Fatalf("redirect=%t, location=%q, want %q", redirected, response.Header().Get("Location"), test.location)
			}
			if test.location != "" && response.Code != http.StatusFound {
				t.Errorf("status %d, want 302", response.Code)
			}
			if test.location == "" && (response.Code != http.StatusOK || response.Body.Len() != 0) {
				t.Errorf("Chinese selection wrote response: %d %q", response.Code, response.Body.String())
			}
			if response.Header().Get("Vary") != "Accept-Language, Cookie" || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Errorf("missing private negotiation headers: %v", response.Header())
			}
			if len(response.Header().Values("Set-Cookie")) != 0 {
				t.Error("server must not remember implicit language choices")
			}
		})
	}
}

func TestVisitorLanguagePreservesExplicitRoutesAndMethods(t *testing.T) {
	for _, target := range []string{
		"/?p=1447", "/index.php?p=1447", "/index.php", "/?page_id=2", "/?cat=13", "/?tag=blog", "/?paged=2", "/?s=hello",
		"/?feed=rss2", "/?sitemap=index", "/?all=1", "/?", "/en/", "/nl/", "/es/", "/es/?p=1447", "/robots.txt", "/missing", "/admin/",
	} {
		t.Run(target, func(t *testing.T) {
			assertVisitorLanguageUnchanged(t, http.MethodGet, target)
		})
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		t.Run(method, func(t *testing.T) { assertVisitorLanguageUnchanged(t, method, "/") })
	}
}

func assertVisitorLanguageUnchanged(t *testing.T, method, target string) {
	t.Helper()
	request := httptest.NewRequest(method, "https://www.ganwen.nl"+target, nil)
	request.Header.Set("Accept-Language", "nl")
	request.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "es"})
	response := httptest.NewRecorder()
	response.Header().Set("Cache-Control", "public, max-age=60")
	response.Header().Set("Vary", "Accept-Encoding")
	if (&App{}).redirectVisitorLanguage(response, request) {
		t.Fatal("explicit route or non-read request redirected")
	}
	if response.Header().Get("Location") != "" || response.Body.Len() != 0 || response.Header().Get("Vary") != "Accept-Encoding" || response.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("response changed: %v %q", response.Header(), response.Body.String())
	}
}

func TestVisitorLanguageLeavesSearchCrawlersOnChineseHome(t *testing.T) {
	for _, userAgent := range []string{
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"GOOGLE-INSPECTIONTOOL/1.0", "bingbot/2.0", "msnbot/2.0b", "Yahoo! Slurp", "DuckDuckBot/1.1",
		"Baiduspider", "YandexBot/3.0", "Sogou web spider/4.0", "360Spider", "Applebot/0.1", "PetalBot", "OAI-SearchBot/1.0",
	} {
		t.Run(userAgent, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl/", nil)
			request.Header.Set("User-Agent", userAgent)
			request.Header.Set("Accept-Language", "nl")
			request.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "en"})
			response := httptest.NewRecorder()
			if (&App{}).redirectVisitorLanguage(response, request) || response.Header().Get("Location") != "" || response.Body.Len() != 0 {
				t.Fatal("search crawler redirected or received alternate content")
			}
			if response.Header().Get("Cache-Control") != "private, no-store" {
				t.Error("crawler response can poison visitor language cache")
			}
		})
	}
}

func TestVisitorLanguageHeadAndRepeatedHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodHead, "https://www.ganwen.nl/", nil)
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Add("Accept-Language", "en;q=0.2")
	request.Header.Add("Accept-Language", "es-MX;q=0.9")
	response := httptest.NewRecorder()
	response.Header().Add("Vary", "Accept-Encoding, accept-language")
	response.Header().Add("Vary", "Cookie, ACCEPT-ENCODING")
	if !(&App{}).redirectVisitorLanguage(response, request) || response.Code != http.StatusFound || response.Header().Get("Location") != "/es/" {
		t.Fatalf("HEAD negotiation: %d %v", response.Code, response.Header())
	}
	if response.Body.Len() != 0 {
		t.Error("HEAD response contains a body")
	}
	vary := strings.ToLower(response.Header().Get("Vary"))
	for _, field := range []string{"accept-encoding", "accept-language", "cookie"} {
		if strings.Count(vary, field) != 1 {
			t.Errorf("Vary did not preserve and deduplicate %s: %q", field, vary)
		}
	}
}

func TestVisitorLanguagePreservesWildcardVary(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl/", nil)
	response := httptest.NewRecorder()
	response.Header().Set("Vary", "*")
	(&App{}).redirectVisitorLanguage(response, request)
	if response.Header().Get("Vary") != "*" {
		t.Error("wildcard Vary was narrowed")
	}
}
