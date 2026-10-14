package blog

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func publicTestApp(t *testing.T) *App {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "public-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	settings := SiteSettings{Title: "甘文", BaseURL: "https://www.ganwen.nl", PostsPerPage: 2, Categories: []Term{{ID: 13, Name: "意境", Slug: "reflections", Taxonomy: "category", URL: "https://www.ganwen.nl/?cat=13"}}, Tags: []Term{{ID: 23, Name: "标签 & 内容", Slug: "标签", Taxonomy: "post_tag", URL: "https://www.ganwen.nl/?tag=%E6%A0%87%E7%AD%BE"}}}
	if err := s.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	posts := []Post{
		{ID: 1447, Type: "post", Status: "publish", Title: "保留原文 & 图片", ContentHTML: `<p>原文 &amp; 内容。</p><img src="/wp-content/uploads/photo.jpg" alt="照片">`, CanonicalURL: "https://www.ganwen.nl/?p=1447", PublishedAt: "2025-01-30T06:43:26+08:00", ModifiedAt: "2026-01-01T00:00:00+08:00", Author: "甘", AuthorID: 1, Sticky: true, Format: "standard", Categories: settings.Categories, Tags: settings.Tags},
		{ID: 1875, Type: "post", Status: "publish", Title: "最新文章", ContentHTML: "<p>最新内容</p>", CanonicalURL: "https://www.ganwen.nl/?p=1875", PublishedAt: "2026-09-16T05:08:32+08:00", AuthorID: 1, Categories: settings.Categories, Format: "video"},
		{ID: 10, Type: "post", Status: "publish", Title: "旧文", ContentHTML: "<p>旧文内容</p>", PublishedAt: "2012-07-15T16:57:11+08:00", AuthorID: 1, Categories: settings.Categories, Format: "aside"},
		{ID: 2, Type: "page", Status: "publish", Title: "關於甘文", ContentHTML: "<p>关于</p>", CanonicalURL: "https://www.ganwen.nl/?page_id=2", PublishedAt: "2012-06-15T16:13:23+08:00"},
		{ID: 100, Type: "post", Status: "private", Title: "不能公开", ContentHTML: "<p>私密资料</p>", PublishedAt: "2026-10-01T00:00:00+08:00"},
		{ID: 200, Type: "attachment", Status: "publish", Title: "附件", ContentHTML: `<p><a href="/wp-content/uploads/photo.jpg">原图</a></p>`, CanonicalURL: "https://www.ganwen.nl/?attachment_id=200"},
	}
	posts[0].Translations = map[string]Translation{
		"en": {Title: "Original & image", ContentHTML: "<p>Original &amp; content.</p>", SourceHash: SourceHash(posts[0]), Status: "publish"},
		"nl": {Title: "Nederlandse vertaling", ContentHTML: "<p>Nederlands</p>", SourceHash: SourceHash(posts[0]), Status: "review"},
	}
	posts[1].Translations = map[string]Translation{"en": {Title: "Outdated English", ContentHTML: "<p>old</p>", SourceHash: "outdated", Status: "publish"}}
	for i := range posts {
		if err := s.saveLocked(context.Background(), &posts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return &App{Store: s, Config: Config{MediaDir: t.TempDir()}}
}

func publicTestRequest(a *App, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://localhost"+target, nil)
	r.Header.Set("Accept-Language", "nl,en;q=0.9")
	if target == "/" {
		r.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "zh"})
	}
	a.handlePublic(w, r)
	return w
}

func TestVisitorLanguagePublicIntegration(t *testing.T) {
	a := publicTestApp(t)
	for _, test := range []struct {
		path, cookie, agent, location, language string
		status                                  int
	}{
		{"/", "", "Mozilla/5.0", "/nl/", "", 302},
		{"/", "zh", "Mozilla/5.0", "", "zh-Hans", 200},
		{"/", "es", "Mozilla/5.0", "/es/", "", 302},
		{"/", "en", "Googlebot", "", "zh-Hans", 200},
		{"/?p=1447", "nl", "Mozilla/5.0", "", "zh-Hans", 200},
		{"/index.php?p=1447", "nl", "Mozilla/5.0", "", "zh-Hans", 200},
		{"/en/?p=1447", "nl", "Mozilla/5.0", "", "en", 200},
		{"/?cat=13", "nl", "Mozilla/5.0", "", "zh-Hans", 200},
	} {
		t.Run(test.path+"/"+test.cookie+"/"+test.agent, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl"+test.path, nil)
			r.Header.Set("Accept-Language", "nl-NL,en;q=0.8")
			r.Header.Set("User-Agent", test.agent)
			if test.cookie != "" {
				r.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: test.cookie})
			}
			w := httptest.NewRecorder()
			a.handlePublic(w, r)
			if w.Code != test.status || w.Header().Get("Location") != test.location {
				t.Fatalf("got %d %q, want %d %q", w.Code, w.Header().Get("Location"), test.status, test.location)
			}
			if test.language != "" && !strings.Contains(w.Body.String(), `<html lang="`+test.language+`"`) {
				t.Fatal("Original or explicitly selected page language changed")
			}
		})
	}
}

func TestPublicLegacyRoutingAndSEO(t *testing.T) {
	a := publicTestApp(t)
	for _, target := range []string{"/?p=1447", "/index.php?p=1447", "/?page_id=2", "/?attachment_id=200", "/?cat=13", "/?category_name=reflections", "/?tag=%E6%A0%87%E7%AD%BE", "/?author=1", "/?m=202501", "/?post_format=video", "/?paged=2"} {
		t.Run(target, func(t *testing.T) {
			if w := publicTestRequest(a, target); w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	w := publicTestRequest(a, "/?p=1447")
	body := w.Body.String()
	for _, want := range []string{`<html lang="zh-Hans"`, `<link rel="canonical" href="https://www.ganwen.nl/?p=1447">`, `href="https://www.ganwen.nl/en/?p=1447"`, `2025-01-30T06:43:26&#43;08:00`, `保留原文 &amp; 图片 | 甘文`, `/wp-content/uploads/photo.jpg`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, `hreflang="nl"`) {
		t.Error("review translation linked")
	}
	w = publicTestRequest(a, "/")
	if strings.Index(w.Body.String(), "保留原文") > strings.Index(w.Body.String(), "最新文章") {
		t.Error("sticky article not first")
	}
	if strings.Contains(w.Body.String(), "附件") || strings.Contains(w.Body.String(), "不能公开") {
		t.Error("attachment/private post exposed on home")
	}
}

func TestPublicTranslationCannotFallbackOrExposeStale(t *testing.T) {
	a := publicTestApp(t)
	w := publicTestRequest(a, "/en/?p=1447")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `<html lang="en"`) || !strings.Contains(w.Body.String(), `rel="canonical" href="https://www.ganwen.nl/en/?p=1447"`) {
		t.Fatalf("valid English route: %d %s", w.Code, w.Body.String())
	}
	for _, target := range []string{"/nl/?p=1447", "/en/?p=1875", "/nl/?p=2"} {
		if w := publicTestRequest(a, target); w.Code != 404 {
			t.Errorf("%s exposed invalid translation: %d", target, w.Code)
		}
	}
	w = publicTestRequest(a, "/en/")
	if strings.Contains(w.Body.String(), "最新文章") || strings.Contains(w.Body.String(), "Outdated English") {
		t.Error("home exposed stale translation or Chinese fallback")
	}
}

func TestPublic404AndPreviewSearchRobots(t *testing.T) {
	a := publicTestApp(t)
	for _, target := range []string{"/?p=100", "/?p=999999", "/?p=no", "/?p=-1", "/?p=1447&page_id=2", "/?page_id=1447", "/?cat=999", "/?author=999", "/?m=202613", "/?paged=99999", "/?paged=9223372036854775807", "/missing", "/bewijs.html", "/wp-login.php", "/?preview=true"} {
		if w := publicTestRequest(a, target); w.Code != 404 {
			t.Errorf("%s => %d", target, w.Code)
		}
	}
	w := publicTestRequest(a, "/?s=原文")
	if w.Code != 200 || w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Errorf("search status/robots: %d %q", w.Code, w.Header().Get("X-Robots-Tag"))
	}
	a.Config.Preview = true
	w = publicTestRequest(a, "/?p=1447")
	if w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Error("preview indexable")
	}
	w = publicTestRequest(a, "/robots.txt")
	if !strings.Contains(w.Body.String(), "Disallow: /\n") {
		t.Error("preview robots not blocked")
	}
}

func TestPublicWWWRedirectPreservesQuery(t *testing.T) {
	a := publicTestApp(t)
	w := httptest.NewRecorder()
	a.handlePublic(w, httptest.NewRequest("GET", "https://ganwen.nl/?p=1447", nil))
	if w.Code != 301 || w.Header().Get("Location") != "https://www.ganwen.nl/?p=1447" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestPublicSitemapPrivacyAndXML(t *testing.T) {
	a := publicTestApp(t)
	for _, target := range []string{"/?sitemap=index", "/sitemap.xml", "/wp-sitemap.xml", "/?sitemap=posts-post-1", "/?sitemap=posts&sitemap-subtype=post&paged=1", "/?sitemap=posts-page-1", "/?sitemap=taxonomies-category-1", "/?sitemap=taxonomies-post_tag-1", "/?sitemap=taxonomies-post_format-1", "/?sitemap=users-1"} {
		t.Run(target, func(t *testing.T) {
			w := publicTestRequest(a, target)
			if w.Code != 200 {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			decoder := xml.NewDecoder(strings.NewReader(w.Body.String()))
			for {
				_, err := decoder.Token()
				if err != nil {
					if err.Error() != "EOF" {
						t.Fatal(err)
					}
					break
				}
			}
			for _, private := range []string{"不能公开", "私密资料", "?p=100", "Outdated English", "Nederlandse vertaling", "/nl/?p=1447"} {
				if strings.Contains(w.Body.String(), private) {
					t.Errorf("exposed %q", private)
				}
			}
		})
	}
	w := publicTestRequest(a, "/?sitemap=posts-post-1")
	if !strings.Contains(w.Body.String(), "https://www.ganwen.nl/en/?p=1447") {
		t.Error("published translation not in sitemap")
	}
}

func TestPublicRetiredFeedsReturnGoneWithoutDatabase(t *testing.T) {
	// Feed retirement must not load posts or depend on the database being available.
	a := &App{}
	for _, lang := range publicLanguages() {
		for _, route := range []string{"/", "/index.php"} {
			for _, query := range []string{
				"feed=rss2", "feed=comments-rss2", "cat=13&feed=rss2",
				"tag=blog&feed=rss2", "p=1447&feed=rss2", "p=1447&feed=comments-rss2",
				"page_id=2&feed=rss2", "p=100&feed=rss2", "p=999999&feed=rss2",
			} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					target := publicLanguagePrefix(lang) + route + "?" + query
					t.Run(method+target, func(t *testing.T) {
						w := httptest.NewRecorder()
						r := httptest.NewRequest(method, "https://ganwen.nl"+target, nil)
						r.Header.Set("Accept-Language", "nl")
						a.handlePublic(w, r)
						if w.Code != http.StatusGone || w.Header().Get("X-Robots-Tag") != "noindex" || w.Header().Get("Location") != "" {
							t.Fatalf("retired feed response = %d %v", w.Code, w.Header())
						}
						if w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
							t.Fatalf("retired feed still has an RSS content type: %v", w.Header())
						}
						if method == http.MethodHead {
							if w.Body.Len() != 0 {
								t.Fatal("HEAD feed response includes a body")
							}
						} else if expected, ok := publicMessage(lang, "RSS 已停用"); !ok || w.Body.String() != expected+"\n" {
							t.Fatalf("retired feed unexpectedly exposed content: %q", w.Body.String())
						}
					})
				}
			}
		}
	}
}

func TestPublicFeedRetirementPreservesOtherRoutes(t *testing.T) {
	a := publicTestApp(t)
	for _, target := range []string{"/?p=1447", "/?cat=13", "/?sitemap=index", "/sitemap.xml", "/wp-sitemap.xml", "/robots.txt", "/?sitemap=index&feed=rss2", "/?robots=1&feed=rss2"} {
		if w := publicTestRequest(a, target); w.Code != http.StatusOK {
			t.Errorf("%s returned %d", target, w.Code)
		}
	}
	for _, target := range []string{"/?feed=atom", "/?feed=", "/missing?feed=rss2"} {
		if w := publicTestRequest(a, target); w.Code != http.StatusNotFound {
			t.Errorf("unsupported route %s returned %d", target, w.Code)
		}
	}
}

func TestPublicSanitizerPreservesMediaAndRejectsExecution(t *testing.T) {
	input := `<script>alert(1)</script><p onclick="bad()">文本</p><img src="http://www.ganwen.cn/wp-content/uploads/a.jpg" onerror="bad()" srcset="http://www.ganwen.cn/wp-content/uploads/a-300.jpg 300w, /a.jpg 900w"><a href="javascript:alert(1)" target="_blank" rel="opener">链接</a><video controls><source src="/video.mov" type="video/quicktime"></video><iframe src="https://www.ganwen.nl/?p=1653&amp;embed=true"></iframe><iframe src="https://evil.example/test"></iframe><iframe src="https://www.youtube.com/embed/abc"></iframe>`
	out := string(safePublicHTML(input, "https://www.ganwen.nl"))
	for _, bad := range []string{"<script", "onclick", "onerror", "javascript:", "rel=\"opener\"", "evil.example", "<html", "<body"} {
		if strings.Contains(out, bad) {
			t.Errorf("unsafe %q: %s", bad, out)
		}
	}
	for _, want := range []string{`https://www.ganwen.nl/wp-content/uploads/a.jpg`, `/a.jpg 900w`, `<video controls`, `/video.mov`, `href="https://www.ganwen.nl/?p=1653"`, `www.youtube.com/embed/abc`} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q: %s", want, out)
		}
	}
}

func TestEditorAlignmentSurvivesPublicSanitization(t *testing.T) {
	input := `<p class="wp-block-paragraph" style="text-align: center; background: url(https://bad.example/track)">居中</p><h2 align="right"><span style="font-weight: normal; font-style: italic; text-decoration: underline">标题</span></h2><p style="text-align: left !important">左</p><p style="text-align:expression(alert(1));color:red;font-weight:expression(alert(1))" onclick="alert(1)">无效</p>`
	out := string(safePublicHTML(input, "https://www.ganwen.nl"))
	for _, want := range []string{`class="wp-block-paragraph text-align-center"`, `class="text-align-right"`, `class="text-align-left"`, `class="text-weight-normal text-style-italic text-decoration-underline"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("Editor alignment lost %q: %s", want, out)
		}
	}
	for _, bad := range []string{`style=`, `align=`, `bad.example`, `expression`, `onclick`, `alert(`} {
		if strings.Contains(out, bad) {
			t.Fatalf("Unsafe formatting retained %q: %s", bad, out)
		}
	}
}

func TestPublicMediaSandboxAndMIME(t *testing.T) {
	a := publicTestApp(t)
	dir := filepath.Join(a.Config.MediaDir, "wp-content", "uploads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.heic"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Config.MediaDir, "unsafe.php"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	w := publicTestRequest(a, "/wp-content/uploads/sample.heic")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/heic" || w.Body.String() != "original" {
		t.Fatalf("media: %d %s", w.Code, w.Body.String())
	}
	for _, target := range []string{"/unsafe.php", "/wp-content/uploads/missing.jpg", "/../sample.heic", "/wp-content/uploads/..%2Fsample.heic", "/C:%5CWindows%5Csecret.jpg"} {
		if w := publicTestRequest(a, target); w.Code != 404 {
			t.Errorf("%s => %d", target, w.Code)
		}
	}
	if _, err := publicMediaPath(a.Config.MediaDir, "/../outside.jpg"); err == nil {
		t.Error("traversal allowed")
	}
}

func TestPreviewMediaAndArticleLinksStayLocal(t *testing.T) {
	input := `<img src="https://www.ganwen.nl/wp-content/uploads/a.jpg" srcset="http://ganwen.cn/wp-content/uploads/a-300.jpg 300w, https://example.com/a.jpg 900w"><video poster="https://ganwen.nl/poster.jpg"><source src="https://www.ganwen.nl/video.mov"></video><a href="https://www.ganwen.nl/?p=85">正文</a><iframe src="https://www.ganwen.nl/?p=1653&amp;embed=true"></iframe>`
	out := string(safePublicHTML(input, "https://www.ganwen.nl", true))
	for _, want := range []string{`src="/wp-content/uploads/a.jpg"`, `/wp-content/uploads/a-300.jpg 300w`, `https://example.com/a.jpg 900w`, `poster="/poster.jpg"`, `src="/video.mov"`, `href="/?p=85"`, `href="/?p=1653"`} {
		if !strings.Contains(out, want) {
			t.Errorf("preview lost %q: %s", want, out)
		}
	}
	if strings.Contains(out, "https://www.ganwen.nl") || strings.Contains(out, "http://ganwen.cn") {
		t.Errorf("preview requested live site: %s", out)
	}
}

func TestPublicFilterArchiveAndSourceHash(t *testing.T) {
	p := Post{ID: 1, Type: "post", Status: "publish", Title: "中文", ContentHTML: "<p>内容</p>", PublishedAt: "2025-03-01T00:00:00+08:00", Categories: []Term{{ID: 13, Slug: "reflections"}}, Tags: []Term{{Slug: "标签"}}, AuthorID: 1}
	p.Translations = map[string]Translation{"en": {Title: "English", ContentHTML: "<p>Content</p>", Status: "publish", SourceHash: SourceHash(p)}}
	for _, q := range []url.Values{{"cat": {"13"}}, {"tag": {"标签"}}, {"author": {"1"}}, {"m": {"202503"}}, {"s": {"Content"}}} {
		posts, ok := filterPublicPosts([]Post{p}, SiteSettings{}, q, "en")
		if !ok || len(posts) != 1 {
			t.Errorf("%v: %v %d", q, ok, len(posts))
		}
	}
	p.ContentHTML = "<p>编辑后的内容</p>"
	if _, ok := localizedPost(p, "en"); ok {
		t.Error("stale translation survived source edit")
	}
}

func TestPublicSpanishRoutesSEOAndPrivacy(t *testing.T) {
	a := publicTestApp(t)
	ctx := context.Background()
	for _, id := range []int64{1447, 2, 100} {
		if err := a.Store.UpdatePost(ctx, id, func(p *Post) error {
			if p.Translations == nil {
				p.Translations = map[string]Translation{}
			}
			p.Translations["es"] = Translation{Title: "Título en español", ContentHTML: "<p>Contenido completo en español.</p>", SourceHash: SourceHash(*p), Status: "publish"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Store.UpdatePost(ctx, 1875, func(p *Post) error {
		old := p.Translations["en"]
		old.Status = "review"
		p.Translations["en"] = old
		p.Translations["es"] = Translation{Title: "Pendiente de revisión", ContentHTML: "<p>Revisión</p>", SourceHash: SourceHash(*p), Status: "review"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/es/", "/es/?p=1447", "/es/index.php?p=1447", "/es/?page_id=2", "/es/?cat=13", "/es/?s=Contenido"} {
		if w := publicTestRequest(a, target); w.Code != 200 {
			t.Errorf("%s: status %d", target, w.Code)
		}
	}
	w := publicTestRequest(a, "/es/?p=1447")
	for _, want := range []string{`<html lang="es"`, `<link rel="canonical" href="https://www.ganwen.nl/es/?p=1447">`, `hreflang="es"`, `Español`, `Título en español`, `30 de enero de 2025`, `Inicio`, `Sobre mí`, `Administración`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("Spanish page missing %q", want)
		}
	}
	for _, target := range []string{"/es/?p=1875", "/es/?p=100", "/es/?p=10"} {
		if w := publicTestRequest(a, target); w.Code != 404 {
			t.Errorf("%s exposed unavailable/private Spanish: %d", target, w.Code)
		}
	}
	w = publicTestRequest(a, "/?p=1447")
	if !strings.Contains(w.Body.String(), `<html lang="zh-Hans"`) || !strings.Contains(w.Body.String(), `rel="canonical" href="https://www.ganwen.nl/?p=1447"`) {
		t.Fatal("Spanish addition changed original Chinese canonical")
	}
	w = publicTestRequest(a, "/?sitemap=posts-post-1")
	if !strings.Contains(w.Body.String(), "https://www.ganwen.nl/es/?p=1447") || strings.Contains(w.Body.String(), "/es/?p=1875") || strings.Contains(w.Body.String(), "/es/?p=100") {
		t.Fatal("Spanish sitemap exposed invalid translations or omitted published one")
	}
	p, err := a.Store.GetPost(ctx, 1447)
	if err != nil {
		t.Fatal(err)
	}
	p.Translations["es"] = Translation{Title: "Traducción antigua", ContentHTML: "<p>Antigua</p>", SourceHash: "outdated", Status: "publish"}
	if err = a.Store.saveLocked(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if w = publicTestRequest(a, "/es/?p=1447"); w.Code != 404 {
		t.Fatal("stale Spanish translation fell back or became public")
	}
}
