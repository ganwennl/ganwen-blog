package blog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const independentEvidenceTestSource = `<!doctype html><div id="lang-zh"><p>Original Chinese</p></div><div id="lang-nl"><p>Original Dutch</p></div>`

func TestIndependentEvidenceAcceptedLanguage(t *testing.T) {
	for _, test := range []struct{ header, language string }{
		{"", "nl"}, {"en-US,en;q=0.9", "nl"}, {"nl-NL", "nl"}, {"de;q=1,zh-CN;q=0.8", "nl"},
		{"zh-CN", "zh"}, {"zh-SG", "zh"}, {"zh-Hans", "zh"}, {"ZH-hans-cn", "zh"},
		{"zh-TW", "nl"}, {"zh-HK", "nl"}, {"zh-MO", "nl"}, {"zh-Hant", "nl"}, {"zh", "nl"},
		{"zh-Hans-TW", "zh"}, {"zh-Hans-HK", "zh"}, {"zh-Hans-MO", "zh"}, {"zh-Hant-CN", "nl"}, {"zh-TW,zh-CN;q=0.9", "nl"},
		{"zh-CN;q=0.9,en;q=0.8", "zh"}, {"zh-CN;q=0.3,en;q=0.8", "nl"},
		{"en;q=0.8,zh-CN;q=0.8", "nl"}, {"zh-CN;q=0.8,en;q=0.8", "zh"},
		{"en;q=0,zh-CN;q=0.7", "zh"}, {"zh-CN;q=0", "nl"},
		{"*;q=1,zh-CN;q=0.9", "nl"}, {"invalid_tag,zh-CN", "zh"},
		{"zh-CN;q=NaN,en", "nl"}, {"zh-CN;q=Inf,en", "nl"}, {"zh-CN;q=2,en", "nl"},
		{"zh-CN;q=-1,en", "nl"}, {"zh-CN;q=no,en", "nl"}, {"zh-CN;foo=1,en", "nl"},
		{"zh-CN;q=1;q=1,en", "nl"}, {"en;q=bad,zh-SG;q=0.5", "zh"}, {"/nl/,zh-CN", "zh"},
	} {
		t.Run(test.header, func(t *testing.T) {
			if got := independentEvidenceAcceptedLanguage(test.header); got != test.language {
				t.Fatalf("got %s, want %s", got, test.language)
			}
		})
	}
}

func TestIndependentEvidencePageIdentity(t *testing.T) {
	for _, test := range []struct {
		name, kind, path, source string
		want                     bool
	}{
		{"original", "page", "/bewijs.html", independentEvidenceTestSource, true},
		{"renamed", "page", "/private-evidence.html", independentEvidenceTestSource, true},
		{"normal page", "page", "", independentEvidenceTestSource, false},
		{"ordinary independent page", "page", "/other.html", `<p>Other page</p>`, false},
		{"ordinary article", "post", "/other.html", independentEvidenceTestSource, false},
		{"missing original language", "page", "/other.html", `<div id="lang-zh"></div>`, false},
		{"script text", "page", "/other.html", `<script>const text = '<div id="lang-zh"></div><div id="lang-nl"></div>';</script>`, false},
		{"comment text", "page", "/other.html", `<!-- <div id="lang-zh"></div><div id="lang-nl"></div> -->`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := Post{Type: test.kind, PublicPath: test.path, SourceHTML: test.source}
			if got := independentEvidencePage(p); got != test.want {
				t.Fatalf("got %t, want %t", got, test.want)
			}
		})
	}
}

func independentEvidenceTestPost(t *testing.T, a *App) Post {
	t.Helper()
	p := Post{Type: "page", Status: "publish", Unlisted: true, PublicPath: "/bewijs.html", Title: "Evidence Chinese", ContentHTML: "<p>Edited Chinese evidence</p>", SourceHTML: independentEvidenceTestSource}
	p.Translations = map[string]Translation{"nl": {Title: "Bewijs", ContentHTML: "<p>Nederlandse bewijsinhoud</p>", Status: "publish", SourceHash: SourceHash(p)}}
	if err := a.Store.NewPost(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func independentEvidenceTestRequest(a *App, method, target, header, cookie string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "http://localhost"+target, nil)
	if header != "" {
		r.Header.Set("Accept-Language", header)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: cookie})
	}
	a.handlePublic(w, r)
	return w
}

func TestIndependentEvidenceLanguagePublicRoutes(t *testing.T) {
	a := publicTestApp(t)
	p := independentEvidenceTestPost(t, a)
	id := strconv.FormatInt(p.ID, 10)
	for _, test := range []struct {
		name, target, header, cookie, language, location string
		status                                           int
	}{
		{"empty default", "/bewijs.html", "", "", "", "/nl/bewijs.html", 302},
		{"English system", "/bewijs.html", "en-US", "", "", "/nl/bewijs.html", 302},
		{"old Chinese blog cookie", "/bewijs.html", "nl-NL", "zh", "", "/nl/bewijs.html", 302},
		{"Simplified CN", "/bewijs.html", "zh-CN,en;q=0.9", "nl", "zh-Hans", "", 200},
		{"Simplified SG", "/bewijs.html", "zh-SG", "", "zh-Hans", "", 200},
		{"Simplified script", "/bewijs.html", "zh-Hans", "", "zh-Hans", "", 200},
		{"Traditional system", "/bewijs.html", "zh-TW,zh-CN;q=0.9", "zh", "", "/nl/bewijs.html", 302},
		{"manual Chinese", "/bewijs.html?evidence_lang=zh", "en-US", "nl", "zh-Hans", "", 200},
		{"manual Chinese empty system", "/bewijs.html?evidence_lang=zh", "", "", "zh-Hans", "", 200},
		{"manual Dutch", "/nl/bewijs.html", "zh-CN", "zh", "nl", "", 200},
		{"explicit Dutch beats query", "/nl/bewijs.html?evidence_lang=zh", "zh-CN", "zh", "nl", "", 200},
		{"preserve benign query", "/bewijs.html?ref=1", "en", "", "", "/nl/bewijs.html?ref=1", 302},
		{"invalid override", "/bewijs.html?evidence_lang=other", "en", "", "", "/nl/bewijs.html?evidence_lang=other", 302},
		{"invalid ID query", "/bewijs.html?page_id=2", "en", "", "", "", 404},
		{"preview remains closed", "/bewijs.html?preview=true", "en", "", "", "", 404},
		{"page alias", "/?page_id=" + id, "en", "zh", "", "/nl/bewijs.html", 302},
		{"index alias with query", "/index.php?page_id=" + id + "&ref=1", "zh-TW", "", "", "/nl/bewijs.html?ref=1", 302},
		{"post ID alias", "/?p=" + id, "en", "", "", "/nl/bewijs.html", 302},
		{"manual Chinese alias", "/?page_id=" + id + "&evidence_lang=zh", "en", "nl", "zh-Hans", "", 200},
		{"explicit Dutch alias", "/nl/?page_id=" + id, "zh-CN", "zh", "nl", "", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := independentEvidenceTestRequest(a, http.MethodGet, test.target, test.header, test.cookie)
			if w.Code != test.status || w.Header().Get("Location") != test.location {
				t.Fatalf("status %d, location %q", w.Code, w.Header().Get("Location"))
			}
			if test.status != 404 && w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
				t.Fatal("hidden page lost noindex")
			}
			if test.status != 404 && strings.HasPrefix(test.target, "/bewijs.html") {
				if w.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(w.Header().Get("Vary"), "Accept-Language") {
					t.Fatal("negotiated response is cacheable")
				}
			}
			if test.language != "" && !strings.Contains(w.Body.String(), `<html lang="`+test.language+`"`) {
				t.Fatal("rendered wrong language")
			}
		})
	}
	w := independentEvidenceTestRequest(a, http.MethodGet, "/nl/bewijs.html", "en", "")
	if !strings.Contains(w.Body.String(), `href="/bewijs.html?evidence_lang=zh" lang="zh"`) || !strings.Contains(w.Body.String(), `href="/nl/bewijs.html" lang="nl"`) {
		t.Fatal("manual language switch does not retain explicit selection")
	}
	if !strings.Contains(w.Body.String(), `<link rel="alternate" hreflang="zh-Hans" href="https://www.ganwen.nl/bewijs.html">`) || strings.Contains(w.Body.String(), `hreflang="zh-Hans" href="https://www.ganwen.nl/bewijs.html?`) {
		t.Fatal("manual selection leaked into language canonical")
	}
	for _, test := range []struct {
		header string
		status int
	}{{"", 302}, {"zh-CN", 200}} {
		w := independentEvidenceTestRequest(a, http.MethodHead, "/bewijs.html", test.header, "")
		if w.Code != test.status || w.Body.Len() != 0 || w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Fatal("HEAD changed evidence language or visibility")
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://localhost/bewijs.html", nil)
	r.Header.Add("Accept-Language", "en;q=0.3")
	r.Header.Add("Accept-Language", "zh-CN;q=0.9")
	w = httptest.NewRecorder()
	a.handlePublic(w, r)
	if w.Code != 200 {
		t.Fatal("multiple language headers lost highest preference")
	}
}

func TestIndependentEvidenceLanguageAddressAndOtherPages(t *testing.T) {
	a := publicTestApp(t)
	p := independentEvidenceTestPost(t, a)
	if err := a.Store.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		p.PublicPath, p.ContentHTML = "/renamed/evidence.html", "<p>New editable body without original markers</p>"
		translation := p.Translations["nl"]
		translation.SourceHash = SourceHash(*p)
		p.Translations["nl"] = translation
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := independentEvidenceTestRequest(a, http.MethodGet, "/renamed/evidence.html", "en", "zh")
	if w.Code != 302 || w.Header().Get("Location") != "/nl/renamed/evidence.html" {
		t.Fatal("address/body edit changed evidence identity")
	}
	w = independentEvidenceTestRequest(a, http.MethodGet, "/nl/renamed/evidence.html", "en", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `href="/renamed/evidence.html?evidence_lang=zh" lang="zh"`) {
		t.Fatal("manual Chinese URL did not follow address edit")
	}
	for _, target := range []string{"/?page_id=2", "/?p=1447"} {
		w = independentEvidenceTestRequest(a, http.MethodGet, target, "en", "nl")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `<html lang="zh-Hans"`) {
			t.Fatalf("old query route changed: %s", target)
		}
	}
	normal := Post{Type: "page", Status: "publish", PublicPath: "/normal.html", Title: "Normal", ContentHTML: "<p>Ordinary page</p>"}
	if err := a.Store.NewPost(context.Background(), &normal); err != nil {
		t.Fatal(err)
	}
	w = independentEvidenceTestRequest(a, http.MethodGet, normal.PublicPath, "en", "nl")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `<html lang="zh-Hans"`) || strings.Contains(w.Body.String(), "evidence_lang") {
		t.Fatal("ordinary independent page language changed")
	}
	if err := a.Store.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		translation := p.Translations["nl"]
		translation.Status = "review"
		p.Translations["nl"] = translation
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w = independentEvidenceTestRequest(a, http.MethodGet, "/renamed/evidence.html", "en", "")
	if w.Code != 200 || w.Header().Get("Location") != "" {
		t.Fatal("unavailable Dutch translation redirected to a missing page")
	}
}
