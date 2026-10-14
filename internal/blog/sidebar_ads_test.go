package blog

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sidebarAdsFixture() SidebarAdsConfig {
	return SidebarAdsConfig{Enabled: true, IntervalSeconds: 8, Items: []SidebarAd{
		{Title: "本地广告", ImageURL: "/wp-content/uploads/2026/10/ad.png", TargetURL: "https://www.ganwen.nl/?p=1447", Description: "广告说明第一行\n第二行 <纯文本>", ButtonLabel: "了解详情", Sponsor: "示例推广方"},
		{Title: "另一条广告", ImageURL: "https://images.example/ad.webp?signature=abc", TargetURL: "https://example.com/offer?campaign=blog#details"},
	}}
}

func TestAdminSidebarAdsDefaultsAndPersistentPartialUpdates(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	original, err := c.app.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code, raw := c.request(http.MethodGet, "settings", nil, "", "")
	var saved SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.SidebarAds.Enabled || saved.SidebarAds.IntervalSeconds != 8 || len(saved.SidebarAds.Items) != 0 {
		t.Fatal("old settings must default to a disabled, empty advertisement slot")
	}
	stored, err := c.app.Store.Settings(ctx)
	if err != nil || !reflect.DeepEqual(stored, original) {
		t.Fatal("reading new advertisement defaults changed old stored settings")
	}
	input := map[string]any{"title": "广告设置", "posts_per_page": 8, "sidebar_ads": sidebarAdsFixture()}
	code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved.SidebarAds, sidebarAdsFixture()) {
		t.Fatalf("advertisement save failed: %d %s", code, raw)
	}
	// Reopening the real SQLite store proves that the configuration outlives the
	// request and is included in the existing site setting rather than UI state.
	reopened, err := OpenStore(filepath.Join(c.app.Config.DataDir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Settings(ctx)
	if err != nil || !reflect.DeepEqual(persisted.SidebarAds, saved.SidebarAds) {
		t.Fatal("advertisement settings did not survive a store reopen")
	}
	delete(input, "sidebar_ads")
	input["title"] = "旧客户端修改标题"
	code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved.SidebarAds, sidebarAdsFixture()) {
		t.Fatal("old client omitted advertisements but reset saved configuration")
	}
	input["sidebar_ads"] = map[string]any{"enabled": false}
	code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.SidebarAds.Enabled || len(saved.SidebarAds.Items) != 2 || saved.SidebarAds.IntervalSeconds != 8 {
		t.Fatal("explicitly disabling advertisements did not preserve their configuration")
	}
	input["sidebar_ads"] = map[string]any{"interval_seconds": 12}
	code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.SidebarAds.Enabled || saved.SidebarAds.IntervalSeconds != 12 || len(saved.SidebarAds.Items) != 2 {
		t.Fatal("partial interval update erased advertisement items or re-enabled them")
	}
	input["sidebar_ads"] = map[string]any{"enabled": false, "items": sidebarAdsFixture().Items}
	code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.SidebarAds.IntervalSeconds != 12 || !reflect.DeepEqual(saved.SidebarAds.Items, sidebarAdsFixture().Items) {
		t.Fatal("refresh-only advertisement client lost saved text or the legacy interval")
	}
	input["sidebar_ads"] = map[string]any{"items": []SidebarAd{}}
	code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.SidebarAds.Enabled || len(saved.SidebarAds.Items) != 0 || saved.SidebarAds.IntervalSeconds != 12 {
		t.Fatal("explicit empty advertisement list did not clear saved items")
	}
	code, raw = c.request(http.MethodGet, "settings", nil, "", "")
	var reread SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &reread) != nil || !reflect.DeepEqual(reread.SidebarAds, saved.SidebarAds) {
		t.Fatal("advertisement setting reread lost the explicit update")
	}
}

func TestAdminSidebarAdsTypedLegacySettingsOmitZeroConfig(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	baseline := SiteSettings{Title: "原站", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, SidebarAds: sidebarAdsFixture()}
	if err := c.app.Store.SaveSettings(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	input := SiteSettings{Title: "旧客户端修改标题", PostsPerPage: 8}
	wire, err := json.Marshal(input)
	if err != nil || strings.Contains(string(wire), "sidebar_ads") {
		t.Fatal("zero-valued typed settings must omit the new advertisement field")
	}
	code, raw := c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	var saved SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || !reflect.DeepEqual(saved.SidebarAds, baseline.SidebarAds) {
		t.Fatalf("typed old client reset advertisement configuration: %d %s", code, raw)
	}
	// New clients round-trip the default API representation as a valid object.
	if err := c.app.Store.SaveSettings(ctx, SiteSettings{Title: "无广告", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8}); err != nil {
		t.Fatal(err)
	}
	code, raw = c.request(http.MethodGet, "settings", nil, "", "")
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.SidebarAds.Items == nil {
		t.Fatal("default settings API must return an empty advertisement array")
	}
	if code, body := c.request(http.MethodPost, "settings", saved, c.server.URL, c.csrf); code != http.StatusOK {
		t.Fatalf("typed default advertisement round-trip failed: %d %s", code, body)
	}
}

func TestAdminSidebarAdsRejectUnsafeAndMalformedUpdatesAtomically(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	baseline := SiteSettings{Title: "现有设置", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, SidebarAds: sidebarAdsFixture()}
	if err := c.app.Store.SaveSettings(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		patch any
	}{
		{"null config", nil}, {"wrong config type", false},
		{"null switch", map[string]any{"enabled": nil}},
		{"wrong switch type", map[string]any{"enabled": "yes"}},
		{"null items", map[string]any{"items": nil}},
		{"wrong items type", map[string]any{"items": "advertisement"}},
		{"short interval", map[string]any{"interval_seconds": 2}},
		{"long interval", map[string]any{"interval_seconds": 121}},
		{"zero explicit interval", map[string]any{"interval_seconds": 0}},
		{"fractional interval", map[string]any{"interval_seconds": 3.5}},
		{"null interval", map[string]any{"interval_seconds": nil}},
		{"unknown field", map[string]any{"advertisement_code": "anything"}},
	}
	for _, raw := range []string{"javascript:alert(1)", "data:image/png;base64,AA", "//evil.example/a.png", "http://example.com/a.png", "https://user:pass@example.com/a.png", "https://example.com/a.svg", "/wp-content/uploads/../a.png", "/wp-content/uploads/%2e%2e/a.png", "https://example.com/a.png?x=%0aHeader:yes"} {
		ad := sidebarAdsFixture().Items[0]
		ad.ImageURL = raw
		tests = append(tests, struct {
			name  string
			patch any
		}{"image " + raw, map[string]any{"items": []SidebarAd{ad}}})
	}
	for _, raw := range []string{"javascript:alert(1)", "data:text/html,ad", "//evil.example/", "http://example.com/", "https://user:pass@example.com/", "https:///offer", "https://example.com:99999/", "https://example.com/offer?x=%0d%0aHeader:yes", "https://example.com\\@evil.example/"} {
		ad := sidebarAdsFixture().Items[0]
		ad.TargetURL = raw
		tests = append(tests, struct {
			name  string
			patch any
		}{"target " + raw, map[string]any{"items": []SidebarAd{ad}}})
	}
	for _, title := range []string{"", " \t ", "标题\n换行", strings.Repeat("文", 121)} {
		ad := sidebarAdsFixture().Items[0]
		ad.Title = title
		tests = append(tests, struct {
			name  string
			patch any
		}{"invalid title " + title, map[string]any{"items": []SidebarAd{ad}}})
	}
	for _, test := range []struct {
		name  string
		value string
		set   func(*SidebarAd, string)
	}{
		{"long description", strings.Repeat("文", 501), func(ad *SidebarAd, value string) { ad.Description = value }},
		{"description control", "说明\x00文字", func(ad *SidebarAd, value string) { ad.Description = value }},
		{"long button", strings.Repeat("文", 41), func(ad *SidebarAd, value string) { ad.ButtonLabel = value }},
		{"button newline", "点击\n查看", func(ad *SidebarAd, value string) { ad.ButtonLabel = value }},
		{"long sponsor", strings.Repeat("文", 81), func(ad *SidebarAd, value string) { ad.Sponsor = value }},
		{"sponsor control", "推广\x7f方", func(ad *SidebarAd, value string) { ad.Sponsor = value }},
	} {
		ad := sidebarAdsFixture().Items[0]
		test.set(&ad, test.value)
		tests = append(tests, struct {
			name  string
			patch any
		}{test.name, map[string]any{"items": []SidebarAd{ad}}})
	}
	tooMany := make([]SidebarAd, 13)
	for i := range tooMany {
		tooMany[i] = sidebarAdsFixture().Items[0]
	}
	tests = append(tests, struct {
		name  string
		patch any
	}{"too many items", map[string]any{"items": tooMany}})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"title": "不该写入", "posts_per_page": 20, "sidebar_ads": test.patch}
			if code, _ := c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
				t.Fatalf("invalid advertisement update status %d", code)
			}
			stored, err := c.app.Store.Settings(ctx)
			if err != nil || !reflect.DeepEqual(stored, baseline) {
				t.Fatal("invalid advertisement update changed stored site settings")
			}
		})
	}
}

func TestSidebarAdsNormalizationAndStoreValidation(t *testing.T) {
	input := sidebarAdsFixture()
	input.IntervalSeconds = 0
	input.Items[0].Title = "  本地广告  "
	input.Items[0].ImageURL = " /wp-content/uploads/2026/10/ad.png "
	input.Items[0].Description = "  广告说明\n第二行  "
	input.Items[0].ButtonLabel = "  查看详情  "
	input.Items[0].Sponsor = "  推广方  "
	actual, err := normalizedSidebarAds(input)
	if err != nil || actual.IntervalSeconds != 8 || actual.Items[0].Title != "本地广告" || actual.Items[0].ImageURL != "/wp-content/uploads/2026/10/ad.png" || actual.Items[0].Description != "广告说明\n第二行" || actual.Items[0].ButtonLabel != "查看详情" || actual.Items[0].Sponsor != "推广方" || input.Items[0].Title != "  本地广告  " || input.Items[0].Description != "  广告说明\n第二行  " {
		t.Fatal("normalization lost defaults, validation, or mutated the original items")
	}
	for _, interval := range []int{3, 8, 120} {
		input.IntervalSeconds = interval
		if _, err := normalizedSidebarAds(input); err != nil {
			t.Fatalf("valid interval %d rejected", interval)
		}
	}
	c := adminTestSetup(t, true)
	ctx := context.Background()
	baseline := SiteSettings{Title: "原站", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, SidebarAds: sidebarAdsFixture()}
	if err := c.app.Store.SaveSettings(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	bad := baseline
	bad.SidebarAds = sidebarAdsFixture()
	bad.SidebarAds.Items[0].TargetURL = "javascript:alert(1)"
	if err := c.app.Store.SaveSettings(ctx, bad); err == nil {
		t.Fatal("direct settings storage bypassed advertisement URL validation")
	}
	stored, err := c.app.Store.Settings(ctx)
	if err != nil || !reflect.DeepEqual(stored, baseline) {
		t.Fatal("failed direct settings write changed saved configuration")
	}
}

func TestSidebarAdOptionalTextBoundsAndOldItems(t *testing.T) {
	input := sidebarAdsFixture()
	input.Items[0].Description = strings.Repeat("文", 500)
	input.Items[0].ButtonLabel = strings.Repeat("文", 40)
	input.Items[0].Sponsor = strings.Repeat("文", 80)
	if _, err := normalizedSidebarAds(input); err != nil {
		t.Fatalf("optional advertisement text at its rune limit was rejected: %v", err)
	}
	input.Items[0].Description = "第一行\r\n第二行\t文字 <tag>"
	input.Items[0].ButtonLabel = ""
	input.Items[0].Sponsor = ""
	if _, err := normalizedSidebarAds(input); err != nil {
		t.Fatalf("multiline plain text or empty optional fields were rejected: %v", err)
	}
	// Older clients can still send the original three required item fields; the
	// items array retains its established replacement semantics.
	legacy := json.RawMessage(`{"items":[{"title":"旧广告","image_url":"/wp-content/uploads/ad.png","target_url":"https://example.com/"}]}`)
	actual, err := patchedSidebarAds(sidebarAdsFixture(), legacy)
	if err != nil || !actual.Enabled || actual.IntervalSeconds != 8 || len(actual.Items) != 1 || actual.Items[0].Title != "旧广告" || actual.Items[0].Description != "" || actual.Items[0].ButtonLabel != "" || actual.Items[0].Sponsor != "" {
		t.Fatal("older advertisement item payload no longer works as a list replacement")
	}
}

func TestAdminSidebarAdsRequireAuthenticationAndCSRF(t *testing.T) {
	c := adminTestSetup(t, true)
	input := map[string]any{"title": "广告", "posts_per_page": 8, "sidebar_ads": sidebarAdsFixture()}
	if code, _ := c.request(http.MethodPost, "settings", input, c.server.URL, ""); code != http.StatusUnauthorized {
		t.Fatal("anonymous client saved advertisement settings")
	}
	c.login()
	if code, _ := c.request(http.MethodPost, "settings", input, c.server.URL, "wrong-csrf"); code != http.StatusForbidden {
		t.Fatal("advertisement settings bypassed CSRF protection")
	}
}
