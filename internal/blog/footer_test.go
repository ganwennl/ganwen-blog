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

func TestFooterSettingsCompatibilityPersistenceAndVersions(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	source := "中文页脚\nhttps://example.com/"
	original := SiteSettings{Title: "甘文", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, FooterText: &source,
		Categories: []Term{{ID: 17, Name: "技术"}}, FooterTranslations: map[string]FooterTranslation{
			"en": {Text: "English footer", SourceHash: footerSourceHash(source)},
		}}
	if err := c.app.Store.SaveSettings(ctx, original); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"title": "甘文", "posts_per_page": 8}
	code, raw := c.request("POST", "settings", input, c.server.URL, c.csrf)
	var saved SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved, original) {
		t.Fatalf("old client cleared footer or unrelated settings: %d %s", code, raw)
	}
	input["footer_text"] = "已改写原文"
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || publicFooterText(saved, "en") != "已改写原文" || saved.FooterTranslations["en"].Text != "English footer" {
		t.Fatalf("outdated translation must be retained but not displayed: %d %s", code, raw)
	}
	input["footer_translations"] = map[string]FooterTranslation{"en": {Text: "Manually revised"}}
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || publicFooterText(saved, "en") != "Manually revised" || saved.FooterTranslations["en"].SourceHash != footerSourceHash("已改写原文") {
		t.Fatalf("manual translation did not bind to submitted source: %d %s", code, raw)
	}
	reopened, err := OpenStore(filepath.Join(c.app.Config.DataDir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Settings(ctx)
	_ = reopened.Close()
	if err != nil || !reflect.DeepEqual(persisted, saved) {
		t.Fatal("footer settings were not persisted")
	}
	delete(input, "footer_translations")
	input["footer_text"] = ""
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.FooterText == nil {
		t.Fatalf("explicit footer removal failed: %d %s", code, raw)
	}
	for _, lang := range publicLanguages() {
		if publicFooterText(saved, lang) != "" {
			t.Fatalf("removed footer appeared in %s", lang)
		}
	}
	input["footer_text"] = nil
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.FooterText != nil || publicFooterText(saved, "zh") != "© 甘文" || publicFooterText(saved, "en") != "© Ganwen" {
		t.Fatalf("reset to localized default failed: %d %s", code, raw)
	}
}

func TestFooterSettingsRejectInvalidTextAndTranslation(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	before, err := c.app.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{123, "text\x00", strings.Repeat("文", footerTextMaxRunes+1)} {
		input := map[string]any{"title": "标题", "posts_per_page": 8, "footer_text": bad}
		if code, _ := c.request("POST", "settings", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
			t.Fatalf("invalid source was accepted: %d", code)
		}
	}
	for _, bad := range []map[string]FooterTranslation{
		{"zh": {Text: "不能覆盖原文"}}, {"xx": {Text: "unsupported"}}, {"en": {Text: "bad\x00"}},
		{"en": {Text: "bad hash", SourceHash: "wrong"}}, {"en": {Text: strings.Repeat("a", footerTextMaxRunes+1)}},
	} {
		input := map[string]any{"title": "标题", "posts_per_page": 8, "footer_text": "原文", "footer_translations": bad}
		if code, _ := c.request("POST", "settings", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
			t.Fatalf("invalid translation was accepted: %d", code)
		}
	}
	after, err := c.app.Store.Settings(context.Background())
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("invalid footer input changed persisted settings")
	}
}

func TestPublicFooterUsesCurrentTranslationAndEscapesText(t *testing.T) {
	a := publicTestApp(t)
	settings, err := a.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source := "中文页脚 <script>alert('footer')</script>\n第二行"
	settings.FooterText = &source
	settings.FooterTranslations = map[string]FooterTranslation{
		"en": {Text: "English footer <img src=x onerror=alert(1)>\nSecond line", SourceHash: footerSourceHash(source)},
		"nl": {Text: "Stale footer", SourceHash: footerSourceHash("旧原文")},
	}
	if err := a.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, want string }{
		{"/", source}, {"/en/", settings.FooterTranslations["en"].Text},
		{"/en/?p=1447", settings.FooterTranslations["en"].Text}, {"/nl/", source},
	} {
		w := publicTestRequest(a, test.path)
		if w.Code != http.StatusOK || publicMetadataOneText(t, w.Body.String(), "span", "footer-text") != test.want {
			t.Fatalf("%s did not render the correct plain footer", test.path)
		}
		if strings.Contains(w.Body.String(), "<script>alert('footer')</script>") || strings.Contains(w.Body.String(), "<img src=x onerror=alert(1)>") || strings.Contains(w.Body.String(), "Stale footer") {
			t.Fatalf("%s rendered executable or stale footer", test.path)
		}
	}
}

func TestFooterTranslationUsesConfiguredProviderAndRequiresExplicitSave(t *testing.T) {
	c := adminTestSetup(t, true)
	var calls atomic.Int32
	testTranslationServer(t, c.app.Translator, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		if r.Header.Get("X-Test-Translation-Target") != "English" {
			t.Error("footer did not use requested translation language")
		}
		respondTranslation(w, slots)
	})
	cfg := c.app.Translator.secretConfiguration()
	cfg.ExecutionMode = translationModeManual
	if err := c.app.Translator.Configure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	source := "你好\n访问 https://example.com/?a=1&b=2"
	input := map[string]any{"footer_text": source, "language": "en"}
	if code, _ := c.request("POST", "footer/translate", input, c.server.URL, c.csrf); code != http.StatusUnauthorized {
		t.Fatalf("anonymous footer translation accepted: %d", code)
	}
	c.login()
	if code, _ := c.request("POST", "footer/translate", input, c.server.URL, "incorrect"); code != http.StatusForbidden {
		t.Fatalf("footer translation bypassed CSRF: %d", code)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized footer request contacted provider")
	}
	before, err := c.app.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	code, raw := c.request("POST", "footer/translate", input, c.server.URL, c.csrf)
	var translated FooterTranslation
	if code != http.StatusOK || json.Unmarshal(raw, &translated) != nil || translated.Text != "Hello\nVisit https://example.com/?a=1&b=2" || translated.SourceHash != footerSourceHash(source) || calls.Load() != 1 {
		t.Fatalf("manual configured footer translation failed: %d %s", code, raw)
	}
	after, err := c.app.Store.Settings(context.Background())
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("translation overwrote settings without explicit save")
	}
	jobs, err := c.app.Translator.Status(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatal("footer translation altered the article queue")
	}
	cfg.ExecutionMode = translationModePaused
	if err := c.app.Translator.Configure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if code, _ := c.request("POST", "footer/translate", input, c.server.URL, c.csrf); code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatal("paused footer translation contacted provider")
	}
}

func TestFooterTranslationFailureAndInvalidInputDoNotSave(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	var calls atomic.Int32
	testTranslationServer(t, c.app.Translator, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	before, err := c.app.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []map[string]any{
		{"footer_text": "原文", "language": "xx"}, {"language": "en"},
		{"footer_text": "", "language": "en"}, {"footer_text": strings.Repeat("文", footerTextMaxRunes+1), "language": "en"},
	} {
		if code, _ := c.request("POST", "footer/translate", input, c.server.URL, c.csrf); code != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatal("invalid footer input contacted provider")
		}
	}
	if code, _ := c.request("POST", "footer/translate", map[string]any{"footer_text": "原文", "language": "en"}, c.server.URL, c.csrf); code != http.StatusBadRequest || calls.Load() != 1 {
		t.Fatal("provider failure was not reported")
	}
	after, err := c.app.Store.Settings(context.Background())
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatal("failed translation changed persisted settings")
	}
}
