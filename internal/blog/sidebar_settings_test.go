package blog

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSidebarModulesDefaultsExplicitEmptyAndSafeImportedSettings(t *testing.T) {
	want := []string{"search", "popular", "stats", "archives", "categories", "tags"}
	got := publicSidebarModules(SiteSettings{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy modules: %v", got)
	}
	got[0] = "changed"
	if !reflect.DeepEqual(publicSidebarModules(SiteSettings{}), want) {
		t.Fatal("public modules returned shared default storage")
	}
	empty := []string{}
	got = publicSidebarModules(SiteSettings{SidebarModules: &empty})
	if got == nil || len(got) != 0 {
		t.Fatalf("explicit empty modules were replaced by defaults: %v", got)
	}
	input := []string{"tags", "stats", "unknown", "tags", "search"}
	hidden := false
	settings := SiteSettings{SidebarModules: &input, PublicStatsEnabled: &hidden}
	got = publicSidebarModules(settings)
	if !reflect.DeepEqual(got, []string{"search", "tags"}) {
		t.Fatalf("unsafe or hidden imported module rendered: %v", got)
	}
	got[0] = "changed"
	if input[4] != "search" {
		t.Fatal("public module storage aliases saved settings")
	}
	settings.SidebarModules = nil
	if !reflect.DeepEqual(publicSidebarModules(settings), []string{"search", "popular", "archives", "categories", "tags"}) {
		t.Fatal("legacy hidden statistics were enabled by defaults")
	}
}

func TestAdminSidebarDefaultsPersistenceAndIsolatedUpdates(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	original, err := c.app.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	code, raw := c.request(http.MethodGet, "sidebar", nil, "", "")
	var response sidebarSettingsResponse
	if code != http.StatusOK || json.Unmarshal(raw, &response) != nil || len(response.Modules) != 6 || response.ArticleSidebarEnabled || response.SidebarAds.Enabled || response.SidebarAds.IntervalSeconds != 8 || response.SidebarAds.Items == nil {
		t.Fatalf("invalid default sidebar response: %d %s", code, raw)
	}
	afterRead, err := c.app.Store.Settings(ctx)
	if err != nil || !reflect.DeepEqual(original, afterRead) {
		t.Fatal("reading defaults changed stored site settings", err)
	}
	original.SidebarAds = sidebarAdsFixture()
	if err := c.app.Store.SaveSettings(ctx, original); err != nil {
		t.Fatal(err)
	}
	if err := c.app.Store.SetValue(ctx, "translation-config-test", "private-test-value"); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"modules": []string{"tags", "archives", "search"}, "article_sidebar_enabled": true, "sidebar_ads": map[string]any{"enabled": false}}
	code, raw = c.request(http.MethodPut, "sidebar", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &response) != nil || !reflect.DeepEqual(response.Modules, []string{"search", "archives", "tags"}) || !response.ArticleSidebarEnabled || response.SidebarAds.Enabled || !reflect.DeepEqual(response.SidebarAds.Items, original.SidebarAds.Items) {
		t.Fatalf("sidebar save failed: %d %s", code, raw)
	}
	want := original
	wantModules := []string{"search", "archives", "tags"}
	want.SidebarModules = &wantModules
	want.ArticleSidebarEnabled = true
	want.SidebarAds.Enabled = false
	hidden := false
	want.PublicStatsEnabled = &hidden
	reopened, err := OpenStore(filepath.Join(c.app.Config.DataDir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.Settings(ctx)
	if err != nil || !reflect.DeepEqual(stored, want) {
		t.Fatal("sidebar did not persist or changed other site settings", err)
	}
	if private, err := reopened.GetValue(ctx, "translation-config-test"); err != nil || private != "private-test-value" {
		t.Fatal("sidebar save changed private configuration", err)
	}
	code, raw = c.request(http.MethodPost, "settings", map[string]any{"title": "旧客户端标题", "posts_per_page": 10}, c.server.URL, c.csrf)
	var legacy SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &legacy) != nil || !reflect.DeepEqual(legacy.SidebarModules, want.SidebarModules) || !legacy.ArticleSidebarEnabled || legacy.SidebarAds.Enabled || publicStatsEnabled(legacy) {
		t.Fatalf("legacy omitted fields reset sidebar settings: %d %s", code, raw)
	}
	input["modules"] = []string{}
	code, raw = c.request(http.MethodPut, "sidebar", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &response) != nil || response.Modules == nil || len(response.Modules) != 0 {
		t.Fatalf("explicit empty sidebar was not saved: %d %s", code, raw)
	}
	stored, err = reopened.Settings(ctx)
	if err != nil || stored.SidebarModules == nil || len(*stored.SidebarModules) != 0 {
		t.Fatal("explicit empty modules did not survive storage", err)
	}
}

func TestAdminSidebarRejectsMalformedUpdatesAtomically(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	before, err := c.app.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]any{
		"null body":            json.RawMessage(`null`),
		"missing fields":       map[string]any{},
		"null modules":         map[string]any{"modules": nil, "article_sidebar_enabled": false, "sidebar_ads": map[string]any{}},
		"unknown module":       map[string]any{"modules": []string{"ads"}, "article_sidebar_enabled": false, "sidebar_ads": map[string]any{}},
		"duplicate module":     map[string]any{"modules": []string{"search", "search"}, "article_sidebar_enabled": false, "sidebar_ads": map[string]any{}},
		"wrong modules type":   map[string]any{"modules": "search", "article_sidebar_enabled": false, "sidebar_ads": map[string]any{}},
		"null article switch":  map[string]any{"modules": []string{}, "article_sidebar_enabled": nil, "sidebar_ads": map[string]any{}},
		"wrong article switch": map[string]any{"modules": []string{}, "article_sidebar_enabled": "false", "sidebar_ads": map[string]any{}},
		"null ads":             map[string]any{"modules": []string{}, "article_sidebar_enabled": true, "sidebar_ads": nil},
		"invalid ads":          map[string]any{"modules": []string{}, "article_sidebar_enabled": true, "sidebar_ads": map[string]any{"items": []SidebarAd{{Title: "unsafe", ImageURL: "javascript:alert(1)", TargetURL: "https://example.com/"}}}},
		"unrelated field":      map[string]any{"modules": []string{}, "article_sidebar_enabled": true, "sidebar_ads": map[string]any{}, "base_url": "https://other.example/"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, raw := c.request(http.MethodPut, "sidebar", payload, c.server.URL, c.csrf); code != http.StatusBadRequest {
				t.Fatalf("invalid sidebar returned HTTP %d %s", code, raw)
			}
			after, err := c.app.Store.Settings(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("invalid sidebar changed stored settings", err)
			}
		})
	}
	for _, value := range []any{nil, []string{"unknown"}, []string{"search", "search"}, "search"} {
		payload := map[string]any{"title": "不应保存", "posts_per_page": 10, "sidebar_modules": value}
		if code, raw := c.request(http.MethodPost, "settings", payload, c.server.URL, c.csrf); code != http.StatusBadRequest {
			t.Fatalf("legacy API bypassed module validation: %d %s", code, raw)
		}
		after, err := c.app.Store.Settings(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("invalid legacy modules changed stored settings", err)
		}
	}
}

func TestAdminSidebarStatisticsSwitchCompatibility(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	payload := map[string]any{"title": "统计开关兼容", "posts_per_page": 10, "sidebar_modules": []string{"search", "stats"}, "public_stats_enabled": false}
	code, raw := c.request(http.MethodPost, "settings", payload, c.server.URL, c.csrf)
	var saved SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || !publicStatsEnabled(saved) || !reflect.DeepEqual(publicSidebarModules(saved), []string{"search", "stats"}) {
		t.Fatalf("explicit modules must control the statistics selection: %d %s", code, raw)
	}
	delete(payload, "sidebar_modules")
	code, raw = c.request(http.MethodPost, "settings", payload, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || publicStatsEnabled(saved) || !reflect.DeepEqual(publicSidebarModules(saved), []string{"search"}) {
		t.Fatalf("legacy statistics disable was ignored: %d %s", code, raw)
	}
	payload["public_stats_enabled"] = true
	code, raw = c.request(http.MethodPost, "settings", payload, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || !publicStatsEnabled(saved) || !reflect.DeepEqual(publicSidebarModules(saved), []string{"search", "stats"}) {
		t.Fatalf("legacy statistics enable was ignored: %d %s", code, raw)
	}
}

func TestAdminSidebarAuthenticationCSRFOriginAndMethods(t *testing.T) {
	c := adminTestSetup(t, true)
	payload := map[string]any{"modules": []string{}, "article_sidebar_enabled": true, "sidebar_ads": map[string]any{}}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if code, _ := c.request(method, "sidebar", payload, c.server.URL, ""); code != http.StatusUnauthorized {
			t.Fatal("anonymous client accessed sidebar settings", method, code)
		}
	}
	c.login()
	if code, _ := c.request(http.MethodPut, "sidebar", payload, c.server.URL, "wrong"); code != http.StatusForbidden {
		t.Fatal("sidebar PUT bypassed CSRF", code)
	}
	if code, _ := c.request(http.MethodPut, "sidebar", payload, "https://other.example", c.csrf); code != http.StatusForbidden {
		t.Fatal("sidebar PUT bypassed origin check", code)
	}
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodHead} {
		if code, _ := c.request(method, "sidebar", payload, c.server.URL, c.csrf); code != http.StatusMethodNotAllowed {
			t.Fatal("sidebar accepted unsupported method", method, code)
		}
	}
	if code, _ := c.request(http.MethodPut, "settings", map[string]any{"title": "不应保存", "posts_per_page": 10}, c.server.URL, c.csrf); code != http.StatusMethodNotAllowed {
		t.Fatal("sidebar PUT exception extended to other routes", code)
	}
}

func TestSidebarModulesRestoreRejectsMalformedValuesBeforeWriting(t *testing.T) {
	for _, value := range []string{`null`, `"search"`, `false`, `1`, `{}`, `["unknown"]`, `["search","search"]`, `["search",null]`} {
		t.Run(value, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			before, err := store.DatabaseSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			backup := DatabaseBackup{
				Format: "ganwen-blog-database", Version: 1,
				Posts: []Post{{ID: 42, Type: "post", Status: "publish", Title: "不应部分导入"}},
				Values: []DatabaseValue{
					{Key: "unrelated", Value: "must not be inserted"},
					{Key: "site", Value: `{"public_stats_enabled":true,"sidebar_modules":` + value + `}`},
				},
			}
			if err := store.RestoreDatabase(ctx, backup); err == nil {
				t.Fatal("restore accepted malformed sidebar modules")
			}
			after, err := store.DatabaseSnapshot(ctx)
			if err != nil || !reflect.DeepEqual(before.Posts, after.Posts) || !reflect.DeepEqual(before.Values, after.Values) || !reflect.DeepEqual(before.Jobs, after.Jobs) || !reflect.DeepEqual(before.Analytics, after.Analytics) {
				t.Fatal("rejected sidebar restore changed the empty target database", err)
			}
		})
	}
}

func TestSidebarModulesRestorePreservesLegacyEmptyAndExplicitSelections(t *testing.T) {
	ctx := context.Background()
	empty := []string{}
	selected := []string{"tags", "search", "archives"}
	for _, modules := range []*[]string{nil, &empty, &selected} {
		source, err := OpenStore(filepath.Join(t.TempDir(), "source.db"))
		if err != nil {
			t.Fatal(err)
		}
		settings := SiteSettings{Title: "恢复侧栏", BaseURL: "https://restore.example", PostsPerPage: 10, SidebarModules: modules}
		site, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		backup := DatabaseBackup{Format: "ganwen-blog-database", Version: 1, Values: []DatabaseValue{{Key: "site", Value: string(site)}}}
		if err := source.RestoreDatabase(ctx, backup); err != nil {
			t.Fatal("valid legacy or explicit sidebar restore was rejected", err)
		}
		snapshot, err := source.DatabaseSnapshot(ctx)
		_ = source.Close()
		if err != nil {
			t.Fatal(err)
		}
		target, err := OpenStore(filepath.Join(t.TempDir(), "target.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := target.RestoreDatabase(ctx, snapshot); err != nil {
			t.Fatal("sidebar snapshot could not be restored", err)
		}
		got, err := target.Settings(ctx)
		_ = target.Close()
		if err != nil || !reflect.DeepEqual(got, settings) {
			t.Fatalf("sidebar backup round-trip changed saved settings: %+v %v", got, err)
		}
	}
	for _, raw := range []string{`{"title":9}`, `{"unknown_legacy_field":true}`, `legacy-site-value`} {
		if err := validateSidebarModulesJSON([]byte(raw)); err != nil {
			t.Fatalf("module validation rejected unrelated legacy settings: %s %v", raw, err)
		}
	}
}
