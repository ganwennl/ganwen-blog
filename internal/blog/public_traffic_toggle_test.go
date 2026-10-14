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

func trafficBool(value bool) *bool { return &value }

func TestPublicVirtualViewsOffUsesRealTotalsAndPopularity(t *testing.T) {
	app, _ := analyticsFixture(t)
	enableArticleSidebar(t, app)
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: 7})
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: "/?p=42", PostID: 42, PV: 4})
	ctx := context.Background()
	settings, _ := app.Store.Settings(ctx)
	before, _ := app.Store.DatabaseSnapshot(ctx)
	settings.PublicVirtualViewsEnabled = trafficBool(false)
	if err := app.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	view := sidebarView(t, app, "zh")
	if view.SidebarStats != (publicSidebarStats{TodayViews: 7, YearViews: 7, HistoricalViews: 7}) || len(view.SidebarPopular) != 1 || view.SidebarPopular[0].Views != 4 {
		t.Fatalf("disabled virtual display retained simulated counts: %+v %+v", view.SidebarStats, view.SidebarPopular)
	}
	settings.PublicVirtualViewsEnabled = trafficBool(true)
	if err := app.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	virtual := sidebarView(t, app, "zh")
	if virtual.SidebarStats == view.SidebarStats || virtual.SidebarPopular[0].Views == 4 {
		t.Fatal("explicit enable did not restore display calculation")
	}
	settings.PublicStatsEnabled = trafficBool(false)
	if err := app.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	w := publicTestRequest(app, "/?p=42")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `id="sidebar-stats-heading"`) || !strings.Contains(w.Body.String(), `id="sidebar-popular-heading"`) {
		t.Fatal("statistics visibility affected the wrong component")
	}
	after, _ := app.Store.DatabaseSnapshot(ctx)
	if !reflect.DeepEqual(before.Analytics, after.Analytics) {
		t.Fatal("display flags changed real analytics")
	}
}

func TestAdminTrafficFlagsStrictPersistenceAndLegacyClient(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	legacy, _ := c.app.Store.Settings(ctx)
	code, raw := c.request(http.MethodGet, "settings", nil, "", "")
	var saved SiteSettings
	if code != 200 || json.Unmarshal(raw, &saved) != nil || !publicVirtualViewsEnabled(saved) || !publicStatsEnabled(saved) {
		t.Fatal("legacy GET lost existing display defaults")
	}
	stored, _ := c.app.Store.Settings(ctx)
	if !reflect.DeepEqual(stored, legacy) {
		t.Fatal("GET changed legacy persisted settings")
	}
	input := map[string]any{"title": "统计开关", "posts_per_page": 10, "public_virtual_views_enabled": false, "public_stats_enabled": false, "public_daily_views": 2500}
	for _, csrf := range []string{"", "invalid"} {
		if code, _ := c.request(http.MethodPost, "settings", input, c.server.URL, csrf); code != 403 {
			t.Fatal("new flags bypassed CSRF", code)
		}
	}
	if code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf); code != 200 {
		t.Fatalf("save failed: %d %s", code, raw)
	}
	reopened, err := OpenStore(filepath.Join(c.app.Config.DataDir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err = reopened.Settings(ctx)
	_ = reopened.Close()
	if err != nil || publicVirtualViewsEnabled(stored) || publicStatsEnabled(stored) || *stored.PublicDailyViews != 2500 {
		t.Fatal("flags did not survive database reopen")
	}
	if code, raw = c.request(http.MethodPost, "settings", SiteSettings{Title: "旧客户端", PostsPerPage: 10}, c.server.URL, c.csrf); code != 200 || json.Unmarshal(raw, &saved) != nil || publicVirtualViewsEnabled(saved) || publicStatsEnabled(saved) {
		t.Fatal("omitted flags reset stored false")
	}
	before, _ := c.app.Store.GetValue(ctx, "site")
	for _, field := range []string{"public_virtual_views_enabled", "public_stats_enabled"} {
		for _, value := range []string{"null", `"false"`, "0", "[]", "{}"} {
			bad := map[string]any{"title": "不能保存", "posts_per_page": 10, field: json.RawMessage(value)}
			if code, _ := c.request(http.MethodPost, "settings", bad, c.server.URL, c.csrf); code != 400 {
				t.Fatal("accepted invalid boolean", field, value, code)
			}
			if after, _ := c.app.Store.GetValue(ctx, "site"); after != before {
				t.Fatal("invalid update partially saved")
			}
			// The daily setting must not bypass validation of a second field.
			if err := validatePublicTrafficSettingsJSON([]byte(`{"public_daily_views":1000,"` + field + `":` + value + `}`)); err == nil {
				t.Fatal("backup accepted invalid flag", field)
			}
		}
	}
}

func TestTrafficFlagsBackupRestoreRoundTripAndAtomicRejection(t *testing.T) {
	ctx := context.Background()
	for _, value := range []*bool{nil, trafficBool(false), trafficBool(true)} {
		store, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
		if err != nil {
			t.Fatal(err)
		}
		settings := SiteSettings{Title: "Restored", BaseURL: "https://restore.example", PostsPerPage: 10, PublicVirtualViewsEnabled: value, PublicStatsEnabled: value}
		raw, _ := json.Marshal(settings)
		backup := DatabaseBackup{Format: "ganwen-blog-database", Version: 1, Values: []DatabaseValue{{Key: "site", Value: string(raw)}}}
		if err := store.RestoreDatabase(ctx, backup); err != nil {
			t.Fatal(err)
		}
		got, err := store.Settings(ctx)
		_ = store.Close()
		if err != nil || !reflect.DeepEqual(got, settings) {
			t.Fatal("restore changed explicit or legacy flags", err)
		}
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, _ := store.DatabaseSnapshot(ctx)
	backup := DatabaseBackup{Format: "ganwen-blog-database", Version: 1, Posts: []Post{{ID: 42, Title: "Do not partially restore"}}, Values: []DatabaseValue{{Key: "site", Value: `{"public_daily_views":1000,"public_stats_enabled":null}`}}}
	if err := store.RestoreDatabase(ctx, backup); err == nil {
		t.Fatal("restore accepted malformed flag")
	}
	after, _ := store.DatabaseSnapshot(ctx)
	if !reflect.DeepEqual(before.Posts, after.Posts) || !reflect.DeepEqual(before.Values, after.Values) {
		t.Fatal("invalid flag partially restored data")
	}
}
