package blog

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestContactNoticeTranslationsPersistPreserveAndExpire(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	source := "  联系前请说明来意。\n请勿发送广告。  "
	original := SiteSettings{Title: "甘文", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, ContactNoticeEnabled: true, ContactNoticeText: source,
		Categories: []Term{{ID: 17, Name: "技术"}}, ContactNoticeTranslations: map[string]ContactNoticeTranslation{
			"en": {Text: "Please explain why you are contacting me.\nNo advertising.", SourceHash: contactNoticeSourceHash(source)},
		}}
	if err := c.app.Store.SaveSettings(ctx, original); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"title": "甘文", "posts_per_page": 8}
	code, raw := c.request("POST", "settings", input, c.server.URL, c.csrf)
	var saved SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved, original) {
		t.Fatalf("legacy settings save discarded translations or unrelated settings: %d %s", code, raw)
	}
	input["contact_notice_text"] = strings.TrimSpace(source)
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || publicContactNoticeText(saved, "en") != original.ContactNoticeTranslations["en"].Text {
		t.Fatalf("trimming source invalidated a matching translation: %d %s", code, raw)
	}
	input["contact_notice_text"] = "修改后的联系提示"
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || publicContactNoticeText(saved, "en") != "修改后的联系提示" || saved.ContactNoticeTranslations["en"].Text != original.ContactNoticeTranslations["en"].Text {
		t.Fatalf("stale translation must remain editable but not be shown: %d %s", code, raw)
	}
	input["contact_notice_translations"] = map[string]ContactNoticeTranslation{"en": {Text: "Updated contact notice"}}
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || publicContactNoticeText(saved, "en") != "Updated contact notice" || saved.ContactNoticeTranslations["en"].SourceHash != contactNoticeSourceHash("修改后的联系提示") {
		t.Fatalf("manual translation did not bind to current source: %d %s", code, raw)
	}
	reopened, err := OpenStore(filepath.Join(c.app.Config.DataDir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Settings(ctx)
	_ = reopened.Close()
	if err != nil || !reflect.DeepEqual(persisted, saved) {
		t.Fatal("notice translations did not persist")
	}
	delete(input, "contact_notice_translations")
	input["contact_notice_enabled"] = false
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	saved = SiteSettings{}
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.ContactNoticeEnabled || len(saved.ContactNoticeTranslations) != 1 {
		t.Fatalf("disable must preserve translations: %d %s", code, raw)
	}
	input["contact_notice_translations"] = map[string]ContactNoticeTranslation{}
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	saved = SiteSettings{}
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || len(saved.ContactNoticeTranslations) != 0 {
		t.Fatalf("explicit empty translation map did not clear translations: %d %s", code, raw)
	}
}

func TestContactNoticeTranslationsValidateAPIStoreAndImport(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	before, _ := c.app.Store.GetValue(ctx, "site")
	badMaps := []map[string]ContactNoticeTranslation{
		{"zh": {Text: "不能覆盖原文"}}, {"xx": {Text: "unsupported"}},
		{"en": {Text: "bad\x00"}}, {"en": {Text: strings.Repeat("😀", contactNoticeMaxRunes+1)}},
		{"en": {Text: "bad hash", SourceHash: "wrong"}},
	}
	for _, translations := range badMaps {
		input := map[string]any{"title": "不能保存", "posts_per_page": 8, "contact_notice_text": "原文", "contact_notice_translations": translations}
		if code, _ := c.request("POST", "settings", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
			t.Fatalf("invalid translations accepted by API: %d", code)
		}
		settings := SiteSettings{Title: "不能保存", BaseURL: "https://www.ganwen.nl", ContactNoticeText: "原文", ContactNoticeTranslations: translations}
		if err := c.app.Store.SaveSettings(ctx, settings); err == nil {
			t.Fatal("invalid translations accepted by Store")
		}
		raw, _ := json.Marshal(settings)
		if err := validateContactNoticeSettingsJSON(raw); err == nil {
			t.Fatal("invalid translations accepted by import validator")
		}
	}
	for _, bad := range []any{nil, []string{"text"}, 123, "text"} {
		input := map[string]any{"title": "不能保存", "posts_per_page": 8, "contact_notice_translations": bad}
		if code, _ := c.request("POST", "settings", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
			t.Fatalf("invalid translation JSON accepted: %d", code)
		}
	}
	if after, _ := c.app.Store.GetValue(ctx, "site"); after != before {
		t.Fatal("rejected translation update changed settings")
	}
	for _, invalid := range []string{`null`, `{"xx":{"text":"wrong"}}`, `{"en":{"text":"bad","source_hash":"wrong"}}`} {
		store, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
		if err != nil {
			t.Fatal(err)
		}
		backup := DatabaseBackup{Format: "ganwen-blog-database", Version: 1,
			Posts:  []Post{{ID: 42, Type: "post", Status: "publish", Title: "不能部分导入"}},
			Values: []DatabaseValue{{Key: "site", Value: `{"contact_notice_text":"原文","contact_notice_translations":` + invalid + `}`}},
		}
		if err := store.RestoreDatabase(ctx, backup); err == nil {
			t.Fatal("invalid notice translation restored")
		}
		posts, err := store.ListPosts(ctx, true)
		_ = store.Close()
		if err != nil || len(posts) != 0 {
			t.Fatal("invalid restore partially wrote posts")
		}
	}
}

func TestPublicContactNoticeUsesLanguageAndEscapesTranslations(t *testing.T) {
	a := publicTestApp(t)
	settings, err := a.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.ContactLinks = []ContactLink{{Kind: "email", URL: "mailto:test@example.com"}}
	settings.ContactNoticeEnabled = true
	settings.ContactNoticeText = "中文联系提示 <script>alert(1)</script>"
	settings.ContactNoticeTranslations = map[string]ContactNoticeTranslation{}
	for _, lang := range translationLanguages() {
		settings.ContactNoticeTranslations[lang] = ContactNoticeTranslation{Text: lang + " <img src=x onerror=alert(1)>\nNo advertising.", SourceHash: contactNoticeSourceHash(settings.ContactNoticeText)}
	}
	if err := a.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	for _, lang := range publicLanguages() {
		want := settings.ContactNoticeText
		path := "/"
		if lang != "zh" {
			want = settings.ContactNoticeTranslations[lang].Text
			path = "/" + lang + "/"
		}
		routes := []string{path}
		if lang == "zh" || lang == "en" {
			routes = append(routes, path+"?p=1447")
		}
		for _, route := range routes {
			w := publicTestRequest(a, route)
			if w.Code != http.StatusOK || publicMetadataOneText(t, w.Body.String(), "div", "contact-notice-text") != want {
				t.Fatalf("wrong contact notice language in %s: %d", route, w.Code)
			}
			if strings.Contains(w.Body.String(), "<script>alert(1)</script>") || strings.Contains(w.Body.String(), "<img src=x onerror=alert(1)>") {
				t.Fatal("notice translation rendered executable HTML")
			}
		}
	}
	for _, translation := range []ContactNoticeTranslation{
		{Text: "outdated", SourceHash: contactNoticeSourceHash("旧原文")},
		{Text: "missing hash"}, {Text: " \n", SourceHash: contactNoticeSourceHash(settings.ContactNoticeText)},
		{Text: "invalid\x00", SourceHash: contactNoticeSourceHash(settings.ContactNoticeText)},
	} {
		settings.ContactNoticeTranslations["en"] = translation
		if got := publicContactNoticeText(settings, "en"); got != settings.ContactNoticeText {
			t.Fatalf("invalid/stale translation bypassed fallback: %q", got)
		}
	}
}

func TestContactNoticeTranslationUsesProviderAndRequiresExplicitSave(t *testing.T) {
	c := adminTestSetup(t, true)
	var calls atomic.Int32
	testTranslationServer(t, c.app.Translator, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		if r.Header.Get("X-Test-Translation-Target") != "English" {
			t.Error("contact notice did not use requested language")
		}
		respondTranslation(w, slots)
	})
	cfg := c.app.Translator.secretConfiguration()
	cfg.ExecutionMode = translationModeManual
	if err := c.app.Translator.Configure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	source := "  你好\n访问 https://example.com/?a=1&b=2  "
	input := map[string]any{"contact_notice_text": source, "language": "en"}
	if code, _ := c.request("POST", "contact-notice/translate", input, c.server.URL, c.csrf); code != http.StatusUnauthorized {
		t.Fatalf("anonymous translation accepted: %d", code)
	}
	c.login()
	if code, _ := c.request("POST", "contact-notice/translate", input, c.server.URL, "invalid"); code != http.StatusForbidden {
		t.Fatalf("translation bypassed CSRF: %d", code)
	}
	if code, _ := c.request("POST", "contact-notice/translate", input, "https://untrusted.example", c.csrf); code != http.StatusForbidden {
		t.Fatalf("translation bypassed origin check: %d", code)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized request contacted provider")
	}
	before, _ := c.app.Store.GetValue(context.Background(), "site")
	code, raw := c.request("POST", "contact-notice/translate", input, c.server.URL, c.csrf)
	var translated ContactNoticeTranslation
	if code != http.StatusOK || json.Unmarshal(raw, &translated) != nil || translated.Text != "Hello\nVisit https://example.com/?a=1&b=2" || translated.SourceHash != contactNoticeSourceHash(source) || calls.Load() != 1 {
		t.Fatalf("configured notice translation failed: %d %s", code, raw)
	}
	if after, _ := c.app.Store.GetValue(context.Background(), "site"); after != before {
		t.Fatal("translation changed settings before save")
	}
	jobs, err := c.app.Translator.Status(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatal("notice translation altered article queue")
	}
	for _, lang := range publicLanguages() {
		_ = newPublicView(SiteSettings{ContactNoticeText: source}, lang)
	}
	if calls.Load() != 1 {
		t.Fatal("page rendering invoked translation provider")
	}
	cfg.ExecutionMode = translationModePaused
	if err := c.app.Translator.Configure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if code, _ := c.request("POST", "contact-notice/translate", input, c.server.URL, c.csrf); code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatal("paused translation contacted provider")
	}
}

func TestContactNoticeTranslationFailureAndInvalidInputDoNotSave(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	var calls atomic.Int32
	testTranslationServer(t, c.app.Translator, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	before, _ := c.app.Store.GetValue(context.Background(), "site")
	for _, input := range []map[string]any{
		{"contact_notice_text": "原文", "language": "xx"}, {"language": "en"},
		{"contact_notice_text": "", "language": "en"}, {"contact_notice_text": strings.Repeat("文", contactNoticeMaxRunes+1), "language": "en"},
		{"contact_notice_text": "bad\x00", "language": "en"},
	} {
		if code, _ := c.request("POST", "contact-notice/translate", input, c.server.URL, c.csrf); code != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatal("invalid notice source contacted provider")
		}
	}
	if code, _ := c.request("POST", "contact-notice/translate", map[string]any{"contact_notice_text": "原文", "language": "en"}, c.server.URL, c.csrf); code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatal("provider failure was not reported")
	}
	if after, _ := c.app.Store.GetValue(context.Background(), "site"); after != before {
		t.Fatal("failed translation changed persisted settings")
	}
}
