package blog

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type adminTestClient struct {
	t      *testing.T
	app    *App
	server *httptest.Server
	client *http.Client
	csrf   string
}

func TestAdminEditorFourLanguagesPreserveHTMLAndMedia(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	raw := "<!-- wp:video -->\n<figure class='wp-block-video' data-original='yes'><video controls preload='metadata' poster='/wp-content/uploads/2026/10/poster.jpg'><source src='/wp-content/uploads/2026/10/clip.mp4' type='video/mp4'></video></figure>\n<!-- /wp:video -->\n<p style='text-align: center;'>原文 &amp; 图片 <img src='/wp-content/uploads/2026/10/photo.png' alt='原图'></p>"
	p := Post{Title: "可视化编辑回归", ContentHTML: raw, SourceHTML: raw, Status: "private"}
	if err := c.app.Store.NewPost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	p.Title = "只修改标题"
	code, body := c.request("POST", "post", p, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("title save failed: %d %s", code, body)
	}
	p, _ = c.app.Store.GetPost(ctx, p.ID)
	if p.ContentHTML != raw || p.SourceHTML != raw {
		t.Fatal("title-only save rewrote original HTML/media")
	}
	for _, lang := range []string{"en", "nl", "es"} {
		translation := Translation{Title: "Edited " + lang, ContentHTML: raw + "\n<h3>" + lang + "</h3>", SourceHash: SourceHash(p), Status: "publish"}
		code, body = c.request("POST", "translation", map[string]any{"id": p.ID, "version": p.Version, "language": lang, "translation": translation}, c.server.URL, c.csrf)
		if code != 200 {
			t.Fatalf("%s rich media save failed: %d %s", lang, code, body)
		}
		p, _ = c.app.Store.GetPost(ctx, p.ID)
		if p.Translations[lang].ContentHTML != translation.ContentHTML || p.ContentHTML != raw || p.SourceHTML != raw || p.Status != "private" {
			t.Fatalf("%s rich HTML/media or privacy changed", lang)
		}
	}
	for _, lang := range []string{"zh", "en", "nl", "es"} {
		response, err := c.client.Get(c.server.URL + "/admin/preview?id=" + fmtID(p.ID) + "&lang=" + lang)
		if err != nil {
			t.Fatal(err)
		}
		preview, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || !bytes.Contains(preview, []byte(raw)) || !strings.Contains(response.Header.Get("Content-Security-Policy"), "sandbox") {
			t.Fatalf("%s preview lost rich media", lang)
		}
	}
	response, err := c.client.Get(c.server.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	ui, _ := io.ReadAll(response.Body)
	response.Body.Close()
	for _, control := range []string{`data-argument="h2"`, `data-argument="h3"`, `data-argument="blockquote"`, `data-command="bold"`, `data-command="italic"`, `data-command="insertUnorderedList"`, `data-command="insertOrderedList"`, `data-command="undo"`, `data-command="redo"`, `data-command="justifyCenter"`, `data-command="unlink"`, `id="insertLink"`, `id="insertImage"`, `id="insertVideo"`, `id="sourceMode"`, `id="visualMode"`, `id="previewButton"`, `sandbox="allow-same-origin"`} {
		if !bytes.Contains(ui, []byte(control)) {
			t.Fatalf("editor control missing: %s", control)
		}
	}
}

func TestAdminAvatarSettingsLifecycleAndCompatibility(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	original := SiteSettings{Title: "头像设置", BaseURL: "https://www.ganwen.nl", PostsPerPage: 5, Categories: []Term{{ID: 17, Name: "技术"}}, Tags: []Term{{ID: 8, Name: "甘文"}}}
	if err := c.app.Store.SaveSettings(ctx, original); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"/wp-content/uploads/2026/10/avatar.png", "/wp-content/uploads/2026/10/avatar.jpg?v=2", "https://images.example/avatar.webp?signature=abc&size=80", "https://images.example/avatar.avif"} {
		input := original
		input.AvatarURL = value
		input.BaseURL = "https://changed.example"
		code, raw := c.request("POST", "settings", input, c.server.URL, c.csrf)
		var saved SiteSettings
		if code != 200 || json.Unmarshal(raw, &saved) != nil || saved.AvatarURL != value || saved.BaseURL != original.BaseURL || !reflect.DeepEqual(saved.Categories, original.Categories) || !reflect.DeepEqual(saved.Tags, original.Tags) {
			t.Fatalf("avatar save lost settings: %d %s", code, raw)
		}
	}
	before, _ := c.app.Store.Settings(ctx)
	legacy := map[string]any{"title": "旧客户端", "posts_per_page": 5}
	code, _ := c.request("POST", "settings", legacy, c.server.URL, c.csrf)
	stored, err := c.app.Store.Settings(ctx)
	if code != 200 || err != nil || stored.AvatarURL != before.AvatarURL {
		t.Fatal("omitted avatar field cleared existing avatar")
	}
	legacy["avatar_url"] = nil
	code, _ = c.request("POST", "settings", legacy, c.server.URL, c.csrf)
	stored, _ = c.app.Store.Settings(ctx)
	if code != 200 || stored.AvatarURL != before.AvatarURL {
		t.Fatal("null avatar field cleared existing avatar")
	}
	legacy["avatar_url"] = ""
	code, _ = c.request("POST", "settings", legacy, c.server.URL, c.csrf)
	stored, _ = c.app.Store.Settings(ctx)
	if code != 200 || stored.AvatarURL != "" {
		t.Fatal("explicit avatar removal did not persist")
	}
}

func TestAdminAvatarSettingsRejectUnsafeURLs(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	original := SiteSettings{Title: "原头像", BaseURL: "https://www.ganwen.nl", PostsPerPage: 5, AvatarURL: "/wp-content/uploads/2026/10/original.png"}
	if err := c.app.Store.SaveSettings(ctx, original); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"javascript:alert(1)", "data:image/png;base64,AAA", "file:///tmp/avatar.jpg", "//evil.example/avatar.jpg", "http://example.com/avatar.jpg", "https://user:password@example.com/avatar.jpg", "https:///avatar.jpg", "https://example.com/avatar.svg", "https://example.com/avatar.mp4", "https://example.com/avatar.jpg.php", "https://example.com/avatar.jpg#", "https://example.com/avatar.jpg?x=%0aHeader:yes", "https://example.com\\@evil.example/avatar.jpg", "https://example.com/a/../avatar.jpg", "https://example.com/" + strings.Repeat("a", 2048) + ".png",
		"/avatar.jpg", "/wp-content/uploads/../avatar.jpg", "/wp-content/uploads//avatar.jpg", "/wp-content/uploads/%2e%2e/avatar.jpg", "/wp-content/uploads/a%2f..%2f..%2favatar.jpg", "/wp-content/uploads/a%5cb.jpg", "/wp-content/uploads/avatar.html", "/wp-content/uploads/avatar.mp4", "/wp-content/uploads/avatar.svg", "/wp-content/uploads/avatar.jpg?x=%0d%0aHeader:yes",
	} {
		input := SiteSettings{Title: "不应覆盖", PostsPerPage: 8, AvatarURL: value}
		code, raw := c.request("POST", "settings", input, c.server.URL, c.csrf)
		if code != 400 {
			t.Fatalf("unsafe avatar accepted %q: %d %s", value, code, raw)
		}
		stored, err := c.app.Store.Settings(ctx)
		if err != nil || !reflect.DeepEqual(stored, original) {
			t.Fatal("invalid avatar changed original settings")
		}
	}
}

func TestAdminAvatarUploadUsesImageValidation(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	if code, _ := c.uploadTo("upload?purpose=avatar", "fake.png", []byte("<svg onload='alert(1)'></svg>"), c.csrf); code != 400 {
		t.Fatal("avatar upload accepted fake image")
	}
	box := func(kind string, data []byte) []byte {
		b := make([]byte, 8+len(data))
		binary.BigEndian.PutUint32(b, uint32(len(b)))
		copy(b[4:8], kind)
		copy(b[8:], data)
		return b
	}
	movieHeader := make([]byte, 100)
	binary.BigEndian.PutUint32(movieHeader[12:16], 1000)
	track := box("trak", append(box("tkhd", make([]byte, 84)), box("mdia", make([]byte, 8))...))
	movie := box("moov", append(box("mvhd", movieHeader), track...))
	mp4 := append(box("ftyp", []byte("isom\x00\x00\x00\x00isom")), movie...)
	mp4 = append(mp4, box("mdat", []byte{0})...)
	if code, raw := c.upload("valid-structure.mp4", mp4, c.csrf); code != 201 {
		t.Fatalf("video fixture was not accepted by normal upload: %d %s", code, raw)
	}
	if code, _ := c.uploadTo("upload?purpose=avatar", "valid-structure.mp4", mp4, c.csrf); code != 400 {
		t.Fatal("avatar upload accepted a structurally valid video")
	}
	code, raw := c.uploadTo("upload?purpose=avatar", "avatar.png", pngData.Bytes(), c.csrf)
	var upload struct {
		URL string `json:"url"`
	}
	if code != 201 || json.Unmarshal(raw, &upload) != nil || !validAvatarURL(upload.URL) {
		t.Fatalf("valid avatar image upload failed: %d %s", code, raw)
	}
	settings, _ := c.app.Store.Settings(context.Background())
	settings.AvatarURL = upload.URL
	code, _ = c.request("POST", "settings", settings, c.server.URL, c.csrf)
	stored, _ := c.app.Store.Settings(context.Background())
	if code != 200 || stored.AvatarURL != upload.URL {
		t.Fatal("uploaded avatar did not save through settings API")
	}
}

func TestAdminContactSettingsSaveAndCompatibility(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	original := SiteSettings{
		Title: "原站", BaseURL: "https://www.ganwen.nl", PostsPerPage: 5,
		Categories: []Term{{ID: 17, Name: "技术", Taxonomy: "category"}},
		Tags:       []Term{{ID: 8, Name: "甘文", Taxonomy: "post_tag"}},
	}
	if err := c.app.Store.SaveSettings(ctx, original); err != nil {
		t.Fatal(err)
	}
	code, raw := c.request("GET", "settings", nil, "", "")
	var old SiteSettings
	if code != 200 || json.Unmarshal(raw, &old) != nil || len(old.ContactLinks) != 0 {
		t.Fatalf("legacy settings without contact links failed: %d %s", code, raw)
	}
	input := SiteSettings{
		Title: "新名称", Description: "站点介绍", BaseURL: "https://changed.example", PostsPerPage: 8, HeaderImage: "/image.jpg",
		Categories: []Term{{ID: 999, Name: "替换分类"}}, Tags: []Term{{ID: 999, Name: "替换标签"}},
		ContactLinks: []ContactLink{
			{Kind: "x", Label: "  " + strings.Repeat("联", 80) + "  ", URL: "  https://x.com/ganwen_nl  "},
			{Kind: "email", Label: "邮箱", URL: "mailto:ganwen94+blog@gmail.com"},
			{Kind: "telegram", URL: "https://t.me/example"},
			{Kind: "github", URL: "https://github.com/example"},
			{Kind: "mastodon", URL: "https://mastodon.example/@example"},
			{Kind: "website", URL: "https://example.com/"},
		},
	}
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	var saved SiteSettings
	if code != 200 || json.Unmarshal(raw, &saved) != nil {
		t.Fatalf("contact save failed: %d %s", code, raw)
	}
	if len(saved.ContactLinks) != 6 || saved.ContactLinks[0].URL != "https://x.com/ganwen_nl" || saved.ContactLinks[0].Label != strings.Repeat("联", 80) || saved.ContactLinks[1].URL != input.ContactLinks[1].URL {
		t.Fatalf("contact links lost or not normalized: %+v", saved.ContactLinks)
	}
	if saved.BaseURL != original.BaseURL || !reflect.DeepEqual(saved.Categories, original.Categories) || !reflect.DeepEqual(saved.Tags, original.Tags) {
		t.Fatal("contact settings changed migrated URL/categories/tags")
	}
	stored, err := c.app.Store.Settings(ctx)
	if err != nil || !reflect.DeepEqual(stored, saved) {
		t.Fatalf("contact links did not persist: %v", err)
	}
	code, raw = c.request("GET", "settings", nil, "", "")
	var roundTrip SiteSettings
	if code != 200 || json.Unmarshal(raw, &roundTrip) != nil || !reflect.DeepEqual(roundTrip.ContactLinks, saved.ContactLinks) {
		t.Fatal("settings API did not return persisted contacts")
	}
	// Old clients that omit this new field must not remove existing contacts.
	legacy := map[string]any{"title": "旧客户端更新", "description": "", "posts_per_page": 7, "header_image": ""}
	code, _ = c.request("POST", "settings", legacy, c.server.URL, c.csrf)
	stored, err = c.app.Store.Settings(ctx)
	if code != 200 || err != nil || !reflect.DeepEqual(stored.ContactLinks, saved.ContactLinks) {
		t.Fatal("omitted contact field erased existing contacts")
	}
	input.ContactLinks = []ContactLink{{Kind: "x", Label: "不显示", URL: "   "}, {Kind: "email", URL: ""}}
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	stored, err = c.app.Store.Settings(ctx)
	if code != 200 || err != nil || len(stored.ContactLinks) != 0 {
		t.Fatalf("empty addresses not hidden: %d %s %v", code, raw, err)
	}
	// The browser editor deliberately sends [] to remove every contact.
	input.ContactLinks = []ContactLink{}
	code, _ = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatal("empty contact list rejected")
	}
	response, err := c.client.Get(c.server.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	ui, _ := io.ReadAll(response.Body)
	response.Body.Close()
	for _, text := range []string{"联系小组件", "contactFields", "collectContactLinks", "kind:'x'", "kind:'email'", "kind:'telegram'", "kind:'github'", "kind:'mastodon'", "kind:'website'"} {
		if !strings.Contains(string(ui), text) {
			t.Fatalf("contact editor missing %q", text)
		}
	}
}

func TestAdminContactSettingsRejectUnsafeLinks(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	original := SiteSettings{Title: "原站", BaseURL: "https://www.ganwen.nl", PostsPerPage: 5, ContactLinks: []ContactLink{{Kind: "x", URL: "https://x.com/ganwen_nl"}}}
	if err := c.app.Store.SaveSettings(ctx, original); err != nil {
		t.Fatal(err)
	}
	link := func(kind, rawURL string) []ContactLink {
		return []ContactLink{{Kind: kind, Label: "联系", URL: rawURL}}
	}
	tests := []struct {
		name  string
		links []ContactLink
	}{
		{"javascript", link("x", "javascript:alert(1)")},
		{"data", link("website", "data:text/html,<script>alert(1)</script>")},
		{"http", link("x", "http://x.com/ganwen_nl")},
		{"relative", link("website", "/about")},
		{"protocol relative", link("website", "//example.com/")},
		{"no host", link("website", "https:///about")},
		{"opaque https", link("website", "https:example.com")},
		{"userinfo", link("x", "https://ganwen_nl:secret@evil.example/")},
		{"backslash", link("website", "https://example.com\\@evil.example/")},
		{"bad port", link("website", "https://example.com:65536/")},
		{"zero port", link("website", "https://example.com:0/")},
		{"url control", link("website", "https://example.com/\r\nX-Test:yes")},
		{"encoded url control", link("website", "https://example.com/%0D%0AX-Test:yes")},
		{"mail for other kind", link("github", "mailto:ganwen94@gmail.com")},
		{"https for email", link("email", "https://example.com/")},
		{"bare email", link("email", "ganwen94@gmail.com")},
		{"invalid mailbox", link("email", "mailto:not-an-email")},
		{"mail subject", link("email", "mailto:ganwen94@gmail.com?subject=test")},
		{"mail bcc", link("email", "mailto:ganwen94@gmail.com?bcc=evil@example.com")},
		{"mail empty query", link("email", "mailto:ganwen94@gmail.com?")},
		{"mail fragment", link("email", "mailto:ganwen94@gmail.com#fragment")},
		{"mail empty fragment", link("email", "mailto:ganwen94@gmail.com#")},
		{"mail double", link("email", "mailto:ganwen94@gmail.com,evil@example.com")},
		{"mail semicolon", link("email", "mailto:ganwen94@gmail.com;evil@example.com")},
		{"mail display name", link("email", "mailto:User%20%3Cganwen94@gmail.com%3E")},
		{"mail comments", link("email", "mailto:ganwen94(comment)@gmail.com")},
		{"mail line injection", link("email", "mailto:ganwen94@gmail.com%0D%0ABcc:evil@example.com")},
		{"mail host", link("email", "mailto://ganwen94@gmail.com")},
		{"unknown kind", link("unsupported", "https://example.com/")},
		{"duplicate address", []ContactLink{{Kind: "x", URL: "https://x.com/one"}, {Kind: "website", URL: "https://X.COM:443/one#about"}}},
		{"duplicate email", []ContactLink{{Kind: "email", URL: "mailto:user@gmail.com"}, {Kind: "email", URL: "mailto:USER%40GMAIL.COM"}}},
		{"label too long", []ContactLink{{Kind: "x", Label: strings.Repeat("联", 81), URL: "https://x.com/ganwen_nl"}}},
		{"label control", []ContactLink{{Kind: "x", Label: "联系\n方式", URL: "https://x.com/ganwen_nl"}}},
		{"url too long", link("website", "https://example.com/"+strings.Repeat("a", 2048))},
		{"too many", make([]ContactLink, 13)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := SiteSettings{Title: "不该保存", PostsPerPage: 9, ContactLinks: test.links}
			code, raw := c.request("POST", "settings", input, c.server.URL, c.csrf)
			if code != 400 {
				t.Fatalf("unsafe contact accepted: %d %s", code, raw)
			}
			stored, err := c.app.Store.Settings(ctx)
			if err != nil || !reflect.DeepEqual(stored, original) {
				t.Fatalf("failed validation changed existing settings: %v", err)
			}
		})
	}
	input := SiteSettings{Title: "未经授权", PostsPerPage: 9, ContactLinks: link("x", "https://x.com/changed")}
	code, _ := c.request("POST", "settings", input, c.server.URL, "wrong-csrf")
	if code != 403 {
		t.Fatalf("contact settings accepted without CSRF: %d", code)
	}
	stored, err := c.app.Store.Settings(ctx)
	if err != nil || !reflect.DeepEqual(stored, original) {
		t.Fatal("CSRF failure changed contact settings")
	}
}

func TestAdminContactSettingsKeepsMultipleAccountsAndManualChoices(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	input := SiteSettings{Title: "联系设置", PostsPerPage: 10, ContactLinks: []ContactLink{
		{Kind: "email", Label: "gmail", URL: "mailto:ganwen94@gmail.com"},
		{Kind: "email", Label: "生活邮箱", URL: "mailto:ganwen@outlook.com"},
		{Kind: "github", Label: "甘文", URL: "https://github.com/ganwen"},
		{Kind: "website", URL: "https://example.com/"},
		{Kind: "website", URL: "https://other.example/"},
		{Kind: "instagram", URL: "https://instagram.com/ganwen"},
		{Kind: "youtube", URL: "https://youtube.com/@ganwen"},
		{Kind: "facebook", URL: "https://facebook.com/ganwen"},
		{Kind: "linkedin", URL: "https://linkedin.com/in/ganwen"},
		{Kind: "mastodon", Label: "我的实例", URL: "https://custom.social/@ganwen", KindManual: true},
		{Kind: "website", Label: "我的页面", URL: "https://github.com/ganwen/pages", KindManual: true},
	}}
	code, raw := c.request("POST", "settings", input, c.server.URL, c.csrf)
	var saved SiteSettings
	if code != 200 || json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved.ContactLinks, input.ContactLinks) {
		t.Fatalf("multiple contacts or explicit choices changed: %d %s", code, raw)
	}
	code, raw = c.request("GET", "settings", nil, "", "")
	var reloaded SiteSettings
	if code != 200 || json.Unmarshal(raw, &reloaded) != nil || !reflect.DeepEqual(reloaded.ContactLinks, input.ContactLinks) {
		t.Fatal("contact addresses, custom names or explicit types did not survive reload")
	}
	stored, err := c.app.Store.Settings(context.Background())
	if err != nil || !reflect.DeepEqual(stored.ContactLinks, input.ContactLinks) {
		t.Fatal("saved contacts or manual-choice flags were not persisted")
	}
}

func TestAdminSpanishSaveAndPreview(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	p := Post{Title: "中文原文", ContentHTML: "<p>原始正文</p>", Status: "publish"}
	if err := c.app.Store.NewPost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	translation := Translation{Title: "Título especial", ContentHTML: "<p>Contenido en español.</p>", SourceHash: SourceHash(p), Status: "publish"}
	code, _ := c.request("POST", "translation", map[string]any{"id": p.ID, "version": p.Version, "language": "es", "translation": translation}, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("Spanish save status %d", code)
	}
	stored, err := c.app.Store.GetPost(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Translations["es"].Status != "publish" || stored.Translations["es"].Title != translation.Title || stored.CanonicalURL != p.CanonicalURL || stored.Title != p.Title {
		t.Fatal("Spanish edit changed original or failed to save")
	}
	response, err := c.client.Get(c.server.URL + "/admin/preview?id=" + fmtID(p.ID) + "&lang=es")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(raw), `<html lang="es"`) || !strings.Contains(string(raw), "Título especial") || !strings.Contains(string(raw), "noindex,nofollow") {
		t.Fatal("Spanish preview language/body/robots incorrect")
	}
	response, err = c.client.Get(c.server.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(raw), `data-lang="es">Español`) || !strings.Contains(string(raw), "const editorLanguages=") {
		t.Fatal("Spanish backend editor/status options missing")
	}
	code, _ = c.request("POST", "translation", map[string]any{"id": p.ID, "version": stored.Version, "language": "de", "translation": translation}, c.server.URL, c.csrf)
	if code != 400 {
		t.Fatal("unsupported admin translation language accepted")
	}
}

func adminTestSetup(t *testing.T, configured bool) *adminTestClient {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cfg := Config{DataDir: dir, MediaDir: filepath.Join(dir, "media"), BaseURL: "https://www.ganwen.nl", Preview: true}
	if configured {
		h, e := bcrypt.GenerateFromPassword([]byte("test-only-secret"), bcrypt.MinCost)
		if e != nil {
			t.Fatal(e)
		}
		cfg.AdminUser = "editor"
		cfg.AdminPasswordHash = string(h)
	}
	app, err := NewApp(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	c := &adminTestClient{t: t, app: app, server: server, client: &http.Client{Jar: jar}}
	code, body := c.request("GET", "session", nil, "", "")
	if code != 200 {
		t.Fatal(string(body))
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatal(err)
	}
	c.csrf = session.CSRF
	return c
}
func (c *adminTestClient) request(method, path string, value any, origin, csrf string) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	if value != nil {
		body, err := json.Marshal(value)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.server.URL+"/admin/api/"+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if value != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	res, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return res.StatusCode, body
}
func (c *adminTestClient) login() {
	c.t.Helper()
	code, b := c.request("POST", "login", map[string]string{"username": "editor", "password": "test-only-secret"}, c.server.URL, c.csrf)
	if code != 200 {
		c.t.Fatalf("login %d: %s", code, b)
	}
	var s struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		c.t.Fatal(err)
	}
	c.csrf = s.CSRF
}
func (c *adminTestClient) newPost(status string) Post {
	c.t.Helper()
	p := Post{Title: "ADMIN PRIVATE SENTINEL", ContentHTML: "<p>原样正文 <img src='/wp-content/uploads/old.jpg'></p>", SourceHTML: "<p>迁移归档</p>", Status: status}
	if err := c.app.Store.NewPost(context.Background(), &p); err != nil {
		c.t.Fatal(err)
	}
	return p
}

func TestAdminRequiresConfiguredCredentialsAndAuthorization(t *testing.T) {
	c := adminTestSetup(t, false)
	code, b := c.request("POST", "login", map[string]string{"username": "editor", "password": "test-only-secret"}, c.server.URL, c.csrf)
	if code != 503 {
		t.Fatalf("unconfigured login %d: %s", code, b)
	}
	if !strings.Contains(string(b), "暂时无法登录") || strings.Contains(string(b), "GBLOG_ADMIN") || strings.Contains(string(b), "密码哈希") {
		t.Fatal("unconfigured login exposed configuration details")
	}
	response, err := c.client.Get(c.server.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	ui := string(raw)
	start := strings.Index(ui, `<section id="login"`)
	if response.StatusCode != 200 || start < 0 || !strings.Contains(response.Header.Get("X-Robots-Tag"), "noindex") {
		t.Fatal("login page or robots response header missing")
	}
	end := strings.Index(ui[start:], "</section>")
	if end < 0 {
		t.Fatal("login section not closed")
	}
	loginUI := ui[start : start+end]
	for _, forbidden := range []string{"使用服务器配置", "不被搜索引擎收录", "密码哈希", "管理员账号尚未配置", "密钥只保存在服务器"} {
		if strings.Contains(ui, forbidden) {
			t.Fatalf("unneeded login or security copy retained: %s", forbidden)
		}
	}
	for _, required := range []string{"登录管理后台", `id="username"`, `id="password"`, `id="loginButton"`, `id="loginError"`} {
		if !strings.Contains(loginUI, required) {
			t.Fatalf("essential login field missing: %s", required)
		}
	}
	for _, path := range []string{"posts", "post?id=1", "settings", "queue", "translation-config", "media"} {
		if code, _ := c.request("GET", path, nil, "", ""); code != 401 {
			t.Errorf("%s anonymous status %d", path, code)
		}
	}
}
func TestAdminCSRFAndLogout(t *testing.T) {
	c := adminTestSetup(t, true)
	login := map[string]string{"username": "editor", "password": "test-only-secret"}
	for _, v := range []struct{ origin, csrf string }{{"", c.csrf}, {"https://evil.test", c.csrf}, {c.server.URL, "wrong"}} {
		if code, _ := c.request("POST", "login", login, v.origin, v.csrf); code != 403 {
			t.Errorf("login csrf status %d", code)
		}
	}
	c.login()
	p := c.newPost("draft")
	for _, v := range []struct{ origin, csrf string }{{"", c.csrf}, {"https://evil.test", c.csrf}, {c.server.URL, "wrong"}} {
		if code, _ := c.request("POST", "post", p, v.origin, v.csrf); code != 403 {
			t.Errorf("post csrf status %d", code)
		}
	}
	if code, b := c.request("POST", "logout", map[string]any{}, c.server.URL, c.csrf); code != 200 {
		t.Fatalf("logout %d: %s", code, b)
	}
	if code, _ := c.request("GET", "posts", nil, "", ""); code != 401 {
		t.Fatalf("logout retained authorization %d", code)
	}
}
func TestAdminSavePreservesCanonicalArchiveAndOptimisticVersion(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	p := c.newPost("draft")
	canonical, archive := p.CanonicalURL, p.SourceHTML
	p.CanonicalURL = "https://evil.test/rewrite"
	p.SourceHTML = "overwrite archive"
	p.Title = "Updated title"
	p.ContentHTML = "<div data-preserve=\"exact\">  <strong>保留</strong></div>"
	code, b := c.request("POST", "post", p, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("save %d: %s", code, b)
	}
	saved, err := c.app.Store.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CanonicalURL != canonical || saved.SourceHTML != archive || saved.ContentHTML != p.ContentHTML {
		t.Fatalf("immutable fields or html changed: %+v", saved)
	}
	if code, _ = c.request("POST", "post", p, c.server.URL, c.csrf); code != 409 {
		t.Fatalf("stale version status %d", code)
	}
}
func TestAdminDraftAndPrivateCannotBeViewedPublicly(t *testing.T) {
	for _, status := range []string{"draft", "private"} {
		t.Run(status, func(t *testing.T) {
			c := adminTestSetup(t, true)
			p := c.newPost(status)
			u, err := url.Parse(p.CanonicalURL)
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{u.RequestURI(), "/", "/en" + u.RequestURI(), "/nl" + u.RequestURI(), "/sitemap.xml"}
			for _, path := range paths {
				res, e := c.client.Get(c.server.URL + path)
				if e != nil {
					t.Fatal(e)
				}
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if strings.Contains(string(b), p.Title) {
					t.Errorf("private/draft exposed in %s", path)
				}
				if path == u.RequestURI() && res.StatusCode != 404 {
					t.Errorf("private/draft article status %d", res.StatusCode)
				}
			}
			c.login()
			res, err := c.client.Get(c.server.URL + "/admin/preview?id=" + fmtID(p.ID))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 || !strings.Contains(string(b), p.Title) || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") || !strings.Contains(res.Header.Get("X-Robots-Tag"), "noindex") {
				t.Fatal("authenticated isolated preview unavailable")
			}
		})
	}
}
func fmtID(id int64) string { b, _ := json.Marshal(id); return string(b) }
func TestAdminTranslationReviewAndSourceProtection(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	p := c.newPost("private")
	input := map[string]any{"id": p.ID, "version": p.Version, "language": "en", "translation": Translation{Title: "PRIVATE TRANSLATION SENTINEL", ContentHTML: "<p>Hello</p>", Status: "publish", SourceHash: "outdated"}}
	if code, _ := c.request("POST", "translation", input, c.server.URL, c.csrf); code != 409 {
		t.Fatalf("stale translation published %d", code)
	}
	input["translation"] = Translation{Title: "PRIVATE TRANSLATION SENTINEL", ContentHTML: "<p>Hello</p>", Status: "publish", SourceHash: SourceHash(p)}
	if code, b := c.request("POST", "translation", input, c.server.URL, c.csrf); code != 200 {
		t.Fatalf("reviewed translation %d: %s", code, b)
	}
	u, _ := url.Parse(p.CanonicalURL)
	res, err := c.client.Get(c.server.URL + "/en" + u.RequestURI())
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "PRIVATE TRANSLATION SENTINEL") {
		t.Fatal("private source translation exposed")
	}
}
func TestAdminSessionExpiryAndLoginRateLimit(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	for key, stored := range storedTestAdminSessions(t, c.app.Store) {
		stored.ExpiresAt = time.Now().Add(-time.Second).Unix()
		raw, _ := json.Marshal(stored)
		if err := c.app.Store.SetValue(context.Background(), key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if code, _ := c.request("GET", "posts", nil, "", ""); code != 401 {
		t.Fatal("expired session accepted")
	}
	code, body := c.request("GET", "session", nil, "", "")
	if code != 200 {
		t.Fatal(string(body))
	}
	var s struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(body, &s)
	c.csrf = s.CSRF
	for i := 0; i < 6; i++ {
		code, _ = c.request("POST", "login", map[string]string{"username": "editor", "password": "wrong"}, c.server.URL, c.csrf)
		if code != 401 {
			t.Fatalf("attempt %d status %d", i, code)
		}
	}
	if code, _ = c.request("POST", "login", map[string]string{"username": "editor", "password": "test-only-secret"}, c.server.URL, c.csrf); code != 429 {
		t.Fatal("login limit not enforced")
	}
}
func (c *adminTestClient) upload(name string, data []byte, csrf string) (int, []byte) {
	return c.uploadTo("upload", name, data, csrf)
}
func (c *adminTestClient) uploadTo(path, name string, data []byte, csrf string) (int, []byte) {
	c.t.Helper()
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	f, err := m.CreateFormFile("file", name)
	if err != nil {
		c.t.Fatal(err)
	}
	_, _ = f.Write(data)
	_ = m.Close()
	req, _ := http.NewRequest("POST", c.server.URL+"/admin/api/"+path, &b)
	req.Header.Set("Content-Type", m.FormDataContentType())
	req.Header.Set("Origin", c.server.URL)
	req.Header.Set("X-CSRF-Token", csrf)
	res, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}
func TestAdminUploadValidImageAndAttacks(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	var imageData bytes.Buffer
	_ = png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	if code, _ := c.upload("good.png", imageData.Bytes(), "wrong"); code != 403 {
		t.Fatal("upload bypassed csrf")
	}
	cases := []struct {
		name string
		data []byte
	}{{"script.svg", []byte("<svg onload='alert(1)'/>")}, {"script.html", []byte("<script>alert(1)</script>")}, {"script.php", imageData.Bytes()}, {"fake.png", []byte("<svg onload='alert(1)'/>")}, {"damaged.png", imageData.Bytes()[:20]}, {"fake.mp4", []byte("<html>fake</html>")}, {"fake.webp", []byte("RIFF0000WEBPxxxx")}}
	for _, test := range cases {
		if code, b := c.upload(test.name, test.data, c.csrf); code != 400 {
			t.Errorf("%s accepted: %d %s", test.name, code, b)
		}
	}
	code, b := c.upload("good.png", imageData.Bytes(), c.csrf)
	if code != 201 {
		t.Fatalf("valid upload %d: %s", code, b)
	}
	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.URL, "/wp-content/uploads/") || !strings.HasSuffix(result.URL, ".png") || strings.Contains(result.URL, "good.png") {
		t.Fatal("unrestricted upload path")
	}
	file := filepath.Join(c.app.Config.MediaDir, filepath.FromSlash(strings.TrimPrefix(result.URL, "/")))
	if bytesOnDisk, err := os.ReadFile(file); err != nil || !bytes.Equal(bytesOnDisk, imageData.Bytes()) {
		t.Fatal("image bytes not preserved")
	}
	if code, b = c.request("GET", "media", nil, "", ""); code != 200 || !bytes.Contains(b, []byte(result.URL)) {
		t.Fatal("uploaded media missing from inventory")
	}
}
func TestAdminCookieFlagsAndNoHashDisclosure(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	code, b := c.request("GET", "session", nil, "", "")
	if code != 200 || bytes.Contains(b, []byte(c.app.Config.AdminPasswordHash)) {
		t.Fatal("session leaked secret")
	}
	recorder := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://www.ganwen.nl/admin/", nil)
	setAdminCookie(recorder, r, Config{BaseURL: "https://www.ganwen.nl"}, sessionCookie, "token", 3600)
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/admin/" {
		t.Fatal("weak session cookie")
	}
}

func TestAdminTranslationProviderConfigurationAndSecretIsolation(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	c.app.Translator.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	code, body := c.request("POST", "translation-config", map[string]any{"provider": "deepseek", "api_key": "admin-deepseek-test-key", "auto_publish": false}, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("DeepSeek configuration failed: %d %s", code, body)
	}
	code, body = c.request("GET", "translation-config", nil, "", "")
	var saved TranslationConfig
	if code != 200 || json.Unmarshal(body, &saved) != nil || saved.Provider != "deepseek" || saved.Endpoint != translationDeepSeekEndpoint || saved.Model != translationDeepSeekModel || !saved.HasAPIKey || saved.APIKey != "" || bytes.Contains(body, []byte("admin-deepseek-test-key")) {
		t.Fatal("admin configuration lost provider defaults or revealed the key")
	}
	code, _ = c.request("POST", "translation-config", map[string]any{"provider": "openai", "model": "test-model"}, c.server.URL, c.csrf)
	if code != 400 || c.app.Translator.secretConfiguration().Provider != "deepseek" || c.app.Translator.secretConfiguration().APIKey != "admin-deepseek-test-key" {
		t.Fatal("admin service switch reused or destroyed the existing secret")
	}
	saved.Model = "deepseek-v4-pro"
	code, _ = c.request("POST", "translation-config", saved, c.server.URL, c.csrf)
	if code != 200 || c.app.Translator.secretConfiguration().Model != "deepseek-v4-pro" || c.app.Translator.secretConfiguration().APIKey != "admin-deepseek-test-key" {
		t.Fatal("admin model change lost its current service key")
	}
}

func TestAdminTranslationPausedRequestsExplainModeAndDoNotQueue(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	p := c.newPost("publish")
	code, body := c.request("POST", "translation-config", map[string]any{"provider": "deepseek", "api_key": "paused-test-only-key", "execution_mode": translationModePaused}, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("pause configuration: %d %s", code, body)
	}
	want := "外部翻译已暂停。当前为仅人工翻译模式；可手动编辑译文，或在翻译设置中配置所需服务并启用手动或自动模式。"
	for _, force := range []bool{false, true} {
		code, body = c.request("POST", "translate", map[string]any{"id": p.ID, "force": force}, c.server.URL, c.csrf)
		var result map[string]string
		if code != 400 || json.Unmarshal(body, &result) != nil || len(result) != 1 || result["error"] != want {
			t.Fatalf("paused request force=%v: %d %s", force, code, body)
		}
	}
	code, body = c.request("GET", "queue", nil, "", "")
	var jobs []TranslationJob
	if code != 200 || json.Unmarshal(body, &jobs) != nil || len(jobs) != 0 {
		t.Fatal("paused API request created translation jobs")
	}
	stored, err := c.app.Store.GetPost(context.Background(), p.ID)
	if err != nil || !reflect.DeepEqual(stored, p) {
		t.Fatal("paused API request changed the article")
	}
	configuration, err := c.app.Translator.Configuration(context.Background())
	if err != nil || configuration.ExecutionMode != translationModePaused || !configuration.HasAPIKey {
		t.Fatal("paused API request changed the configured mode or secret")
	}
}

func TestAdminTranslationQueueErrorsKeepSafeMessages(t *testing.T) {
	for _, unavailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "unavailable-service", false: "ordinary-queue-error"}[unavailable], func(t *testing.T) {
			c := adminTestSetup(t, true)
			c.login()
			p := c.newPost("publish")
			wantCode, want := 503, "翻译服务尚未启用"
			if unavailable {
				c.app.Translator = nil
			} else {
				if err := c.app.Store.UpdatePost(context.Background(), p.ID, func(p *Post) error {
					p.ContentHTML = "<p>中文" + strings.Repeat("x", translationMaxInput) + "</p>"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				wantCode, want = 400, "翻译服务未配置或当前无法加入队列"
			}
			code, body := c.request("POST", "translate", map[string]any{"id": p.ID, "force": false}, c.server.URL, c.csrf)
			var result map[string]string
			if code != wantCode || json.Unmarshal(body, &result) != nil || len(result) != 1 || result["error"] != want {
				t.Fatalf("ordinary error changed or exposed details: %d %s", code, body)
			}
		})
	}
}

func TestAdminTranslationManualClickQueuesEveryLanguage(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	c.app.Translator.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	code, body := c.request("POST", "translation-config", map[string]any{"provider": "deepseek", "api_key": "manual-test-only-key", "execution_mode": translationModeManual}, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("manual configuration: %d %s", code, body)
	}
	p := c.newPost("publish")
	p.Title = "保存不主动翻译"
	code, body = c.request("POST", "post", p, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("manual source save: %d %s", code, body)
	}
	jobs, err := c.app.Translator.Status(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatal("manual source save enqueued automatic requests")
	}
	for _, force := range []bool{false, true} {
		code, body = c.request("POST", "translate", map[string]any{"id": p.ID, "force": force}, c.server.URL, c.csrf)
		if code != 200 {
			t.Fatalf("explicit manual request: %d %s", code, body)
		}
	}
	jobs, err = c.app.Translator.Status(context.Background())
	if err != nil || len(jobs) != len(translationLanguages()) {
		t.Fatal("explicit manual requests did not enqueue exactly ten languages")
	}
	for _, job := range jobs {
		if job.PostID != p.ID || job.Status != "queued" || !isTranslationLanguage(job.Language) {
			t.Fatal("manual request queued an incorrect job")
		}
	}
}

func TestAdminConcurrentLoginReservation(t *testing.T) {
	auth := newAdminAuth(Config{Preview: true})
	r := httptest.NewRequest("POST", "http://example.test/admin/api/login", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if auth.rateAllowed(r) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 6 {
		t.Fatalf("concurrent limit admitted %d attempts", allowed.Load())
	}
}

func adminProxyAttempt(t *testing.T, admin *Admin, peer string, forwarded []string, password string) int {
	t.Helper()
	mux := http.NewServeMux()
	admin.Register(mux)
	challenge := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl/admin/api/session", nil)
	challenge.RemoteAddr = peer
	for _, v := range forwarded {
		challenge.Header.Add("X-Real-IP", v)
	}
	challengeResponse := httptest.NewRecorder()
	mux.ServeHTTP(challengeResponse, challenge)
	if challengeResponse.Code != 200 {
		t.Fatalf("login challenge failed: %d", challengeResponse.Code)
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(challengeResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"username": "editor", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "https://www.ganwen.nl/admin/api/login", bytes.NewReader(data))
	login.RemoteAddr = peer
	login.Header.Set("Origin", "https://www.ganwen.nl")
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("X-CSRF-Token", session.CSRF)
	for _, v := range forwarded {
		login.Header.Add("X-Real-IP", v)
	}
	for _, cookie := range challengeResponse.Result().Cookies() {
		login.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, login)
	return response.Code
}

func adminProxySetup(t *testing.T, preview bool) *Admin {
	t.Helper()
	c := adminTestSetup(t, true)
	c.app.Config.Preview = preview
	return NewAdmin(c.app)
}

func TestAdminProductionProxyRateLimitsIsolateClients(t *testing.T) {
	for _, peer := range []string{"127.0.0.1:9000", "[::1]:9000"} {
		t.Run(peer, func(t *testing.T) {
			admin := adminProxySetup(t, false)
			for i := 0; i < 6; i++ {
				if code := adminProxyAttempt(t, admin, peer, []string{"203.0.113.10"}, "wrong"); code != 401 {
					t.Fatalf("client A attempt %d: %d", i, code)
				}
			}
			if code := adminProxyAttempt(t, admin, peer, []string{"203.0.113.10"}, "wrong"); code != 429 {
				t.Fatalf("client A not limited: %d", code)
			}
			if code := adminProxyAttempt(t, admin, peer, []string{"203.0.113.11"}, "wrong"); code != 401 {
				t.Fatalf("client A blocked client B: %d", code)
			}
			if code := adminProxyAttempt(t, admin, peer, []string{"203.0.113.11"}, "test-only-secret"); code != 200 {
				t.Fatalf("client B cannot log in: %d", code)
			}
			if code := adminProxyAttempt(t, admin, peer, []string{"203.0.113.10"}, "wrong"); code != 429 {
				t.Fatalf("client B reset client A limit: %d", code)
			}
			if code := adminProxyAttempt(t, admin, peer, []string{"203.0.113.11"}, "wrong"); code != 401 {
				t.Fatalf("client B successful login did not reset own limit: %d", code)
			}
		})
	}
}

func TestAdminProxyHeadersCannotBypassDirectOrPreviewLimits(t *testing.T) {
	cases := []struct {
		name    string
		preview bool
		peer    string
	}{{"preview-loopback", true, "127.0.0.1:9000"}, {"production-direct-ipv4", false, "198.51.100.8:9000"}, {"production-direct-ipv6", false, "[2001:db8::8]:9000"}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			admin := adminProxySetup(t, test.preview)
			for i := 0; i < 7; i++ {
				head := []string{"203.0.113." + fmtID(int64(i+10))}
				code := adminProxyAttempt(t, admin, test.peer, head, "wrong")
				want := 401
				if i == 6 {
					want = 429
				}
				if code != want {
					t.Fatalf("forged client %d got %d, want %d", i, code, want)
				}
			}
		})
	}
}

func TestAdminProductionProxyRejectsInvalidOrRepeatedIPHeaders(t *testing.T) {
	cases := []struct {
		name   string
		header func(int) []string
	}{
		{"repeated", func(i int) []string { return []string{"203.0.113.10", "203.0.113." + fmtID(int64(i+20))} }},
		{"list", func(i int) []string { return []string{"203.0.113.10, 203.0.113." + fmtID(int64(i+20))} }},
		{"ip-and-port", func(i int) []string { return []string{"203.0.113." + fmtID(int64(i+20)) + ":443"} }},
		{"invalid", func(i int) []string { return []string{"not-an-ip-" + fmtID(int64(i))} }},
		{"ipv6-zone", func(i int) []string { return []string{"fe80::1%zone" + fmtID(int64(i))} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			admin := adminProxySetup(t, false)
			for i := 0; i < 7; i++ {
				code := adminProxyAttempt(t, admin, "127.0.0.1:9000", test.header(i), "wrong")
				want := 401
				if i == 6 {
					want = 429
				}
				if code != want {
					t.Fatalf("invalid header %d got %d, want %d", i, code, want)
				}
			}
		})
	}
}

func TestAdminProductionProxyCanonicalizesIPv6ClientAddress(t *testing.T) {
	admin := adminProxySetup(t, false)
	for i := 0; i < 6; i++ {
		if code := adminProxyAttempt(t, admin, "[::1]:9000", []string{"2001:db8::abcd"}, "wrong"); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := adminProxyAttempt(t, admin, "[::1]:9000", []string{"2001:0DB8:0:0:0:0:0:ABCD"}, "wrong"); code != 429 {
		t.Fatalf("same IPv6 address bypassed limit: %d", code)
	}
	if code := adminProxyAttempt(t, admin, "[::1]:9000", []string{"2001:db8::abce"}, "wrong"); code != 401 {
		t.Fatalf("independent IPv6 address was blocked: %d", code)
	}
}
