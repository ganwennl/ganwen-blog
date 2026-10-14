package blog

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func publicTrafficViewsPointer(value int64) *int64 { return &value }

func TestPublicDailyDisplayTargetDefaultsAndExplicitZero(t *testing.T) {
	for _, test := range []struct {
		name  string
		value *int64
		want  int64
	}{
		{"legacy nil", nil, 1000},
		{"disabled", publicTrafficViewsPointer(0), 0},
		{"default", publicTrafficViewsPointer(1000), 1000},
		{"larger", publicTrafficViewsPointer(10000), 10000},
		{"maximum", publicTrafficViewsPointer(1000000), 1000000},
		{"negative import", publicTrafficViewsPointer(-1), 1000},
		{"over-limit import", publicTrafficViewsPointer(1000001), 1000},
		{"overflow-scale import", publicTrafficViewsPointer(math.MaxInt64), 1000},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := SiteSettings{PublicDailyViews: test.value}
			before, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			if got := publicDailyDisplayTarget(settings); got != test.want {
				t.Fatalf("display target %d, want %d", got, test.want)
			}
			after, _ := json.Marshal(settings)
			if string(before) != string(after) {
				t.Fatal("reading display target changed persisted settings")
			}
		})
	}
	legacy, _ := json.Marshal(SiteSettings{})
	zero, _ := json.Marshal(SiteSettings{PublicDailyViews: publicTrafficViewsPointer(0)})
	if strings.Contains(string(legacy), "public_daily_views") || !strings.Contains(string(zero), `"public_daily_views":0`) {
		t.Fatal("wire format confused missing settings with an explicit zero")
	}
}

func TestAdminPublicDailyViewsDefaultsPersistenceAndOldClient(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	original, err := c.app.Store.Settings(ctx)
	if err != nil || original.PublicDailyViews != nil {
		t.Fatal("legacy fixture unexpectedly has a configured display target")
	}
	code, raw := c.request(http.MethodGet, "settings", nil, "", "")
	var saved SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.PublicDailyViews == nil || *saved.PublicDailyViews != 1000 {
		t.Fatalf("legacy GET omitted effective default: %d %s", code, raw)
	}
	stored, err := c.app.Store.Settings(ctx)
	if err != nil || !reflect.DeepEqual(stored, original) {
		t.Fatal("GET persisted the display default into legacy settings")
	}
	for _, value := range []int64{1000, 10000, 0, publicDailyViewsMax} {
		input := map[string]any{"title": "展示浏览量设置", "posts_per_page": 8, "public_daily_views": value}
		code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
		if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.PublicDailyViews == nil || *saved.PublicDailyViews != value {
			t.Fatalf("saving %d failed: %d %s", value, code, raw)
		}
		reopened, err := OpenStore(filepath.Join(c.app.Config.DataDir, "blog.db"))
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := reopened.Settings(ctx)
		_ = reopened.Close()
		if err != nil || persisted.PublicDailyViews == nil || *persisted.PublicDailyViews != value {
			t.Fatalf("%d did not survive reopening storage: %+v %v", value, persisted, err)
		}
		// A typed old client omits the pointer field and must retain an explicit 0.
		legacy := SiteSettings{Title: "旧客户端更新标题", PostsPerPage: 8}
		code, raw = c.request(http.MethodPost, "settings", legacy, c.server.URL, c.csrf)
		if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.PublicDailyViews == nil || *saved.PublicDailyViews != value {
			t.Fatalf("old client reset %d: %d %s", value, code, raw)
		}
		code, raw = c.request(http.MethodGet, "settings", nil, "", "")
		if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.PublicDailyViews == nil || *saved.PublicDailyViews != value {
			t.Fatalf("GET reset saved %d: %d %s", value, code, raw)
		}
	}
}

func TestAdminPublicDailyViewsRequireAuthenticationAndCSRF(t *testing.T) {
	c := adminTestSetup(t, true)
	input := map[string]any{"title": "展示设置", "posts_per_page": 8, "public_daily_views": 10000}
	if code, _ := c.request(http.MethodGet, "settings", nil, "", ""); code != http.StatusUnauthorized {
		t.Fatal("anonymous client read display settings", code)
	}
	if code, _ := c.request(http.MethodPost, "settings", input, c.server.URL, ""); code != http.StatusUnauthorized {
		t.Fatal("anonymous client saved display settings", code)
	}
	c.login()
	for _, test := range []struct{ origin, csrf string }{{"", c.csrf}, {"https://evil.example", c.csrf}, {c.server.URL, "wrong"}} {
		if code, _ := c.request(http.MethodPost, "settings", input, test.origin, test.csrf); code != http.StatusForbidden {
			t.Fatal("display settings bypassed CSRF", code)
		}
	}
	stored, err := c.app.Store.Settings(context.Background())
	if err != nil || stored.PublicDailyViews != nil {
		t.Fatal("rejected authorization changed settings")
	}
}

func TestAdminPublicDailyViewsRejectBadUpdatesWithoutChangingAnalytics(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	baseline := SiteSettings{Title: "原始设置", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, PublicDailyViews: publicTrafficViewsPointer(10000)}
	if err := c.app.Store.SaveSettings(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	c.app.Analytics.now = func() time.Time { return analyticsTestDate }
	if ok, err := c.app.Store.RecordAnalytics(ctx, analyticsTestDate, "traffic-test-event", "traffic-test-visitor", nil); err != nil || !ok {
		t.Fatalf("record real analytics: %v %v", ok, err)
	}
	beforeSite, _ := c.app.Store.GetValue(ctx, "site")
	beforeReport := analyticsGetReport(t, c.app)
	beforeSnapshot, err := c.app.Store.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"null", `"1000"`, "1000.5", "1000.0", "1e3", "-1", "1000001", "9223372036854775808", "true", "[]", "{}"} {
		t.Run(value, func(t *testing.T) {
			input := map[string]any{"title": "不能保存的标题", "posts_per_page": 10, "public_daily_views": json.RawMessage(value)}
			if code, raw := c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
				t.Fatalf("accepted %s: %d %s", value, code, raw)
			}
			if after, err := c.app.Store.GetValue(ctx, "site"); err != nil || after != beforeSite {
				t.Fatal("bad value changed another field or the saved display target")
			}
		})
	}
	if after := analyticsGetReport(t, c.app); !reflect.DeepEqual(beforeReport, after) || after.PV != 1 || after.UVDailySum != 1 {
		t.Fatal("display setting update changed real analytics report")
	}
	afterSnapshot, err := c.app.Store.DatabaseSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(beforeSnapshot.Analytics, afterSnapshot.Analytics) {
		t.Fatal("display setting update changed real analytics rows")
	}
}

func TestSaveSettingsRejectsPublicDailyViewsOutsideRange(t *testing.T) {
	app, _ := analyticsFixture(t)
	ctx := context.Background()
	baseline := SiteSettings{Title: "原始设置", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, PublicDailyViews: publicTrafficViewsPointer(1000)}
	if err := app.Store.SaveSettings(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	before, _ := app.Store.GetValue(ctx, "site")
	for _, value := range []int64{-1, publicDailyViewsMax + 1, math.MaxInt64} {
		bad := baseline
		bad.PublicDailyViews = publicTrafficViewsPointer(value)
		if err := app.Store.SaveSettings(ctx, bad); err == nil {
			t.Fatalf("direct SaveSettings accepted %d", value)
		}
		if after, err := app.Store.GetValue(ctx, "site"); err != nil || before != after {
			t.Fatal("failed direct save changed settings")
		}
	}
	baseline.PublicDailyViews = nil
	if err := app.Store.SaveSettings(ctx, baseline); err != nil {
		t.Fatal("direct legacy settings save was rejected", err)
	}
}

func TestPublicDailyViewsBackupImportRejectsBadValuesBeforeWriting(t *testing.T) {
	for _, value := range []string{"null", `"1000"`, "1.5", "-1", "1000001", "9223372036854775808", "false", "[]", "{}"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenStore(filepath.Join(dir, "blog.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			before, err := store.DatabaseSnapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			backup := DatabaseBackup{Format: "ganwen-blog-database", Version: 1, Posts: []Post{{ID: 42, Type: "post", Status: "publish", Title: "不能部分导入"}}, Values: []DatabaseValue{{Key: "unrelated", Value: "preserve"}, {Key: "site", Value: `{"public_daily_views":` + value + `}`}}}
			if err := store.RestoreDatabase(ctx, backup); err == nil {
				t.Fatal("direct restore accepted invalid display setting")
			}
			raw, err := json.Marshal(backup)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "invalid-backup.json")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.ImportDatabaseFile(ctx, file, "", dir); err == nil {
				t.Fatal("file import accepted invalid display setting")
			}
			after, err := store.DatabaseSnapshot(ctx)
			if err != nil || !reflect.DeepEqual(before.Posts, after.Posts) || !reflect.DeepEqual(before.Values, after.Values) || !reflect.DeepEqual(before.Analytics, after.Analytics) {
				t.Fatal("rejected import partially wrote database rows")
			}
		})
	}
}

func TestPublicDailyViewsBackupImportPreservesLegacyAndExplicitValues(t *testing.T) {
	for _, value := range []*int64{nil, publicTrafficViewsPointer(0), publicTrafficViewsPointer(1000), publicTrafficViewsPointer(10000), publicTrafficViewsPointer(publicDailyViewsMax)} {
		dir := t.TempDir()
		store, err := OpenStore(filepath.Join(dir, "blog.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		settings := SiteSettings{Title: "导入设置", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, PublicDailyViews: value}
		site, _ := json.Marshal(settings)
		backup := DatabaseBackup{Format: "ganwen-blog-database", Version: 1, Values: []DatabaseValue{{Key: "site", Value: string(site)}}}
		raw, _ := json.Marshal(backup)
		file := filepath.Join(dir, "backup.json")
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := store.ImportDatabaseFile(context.Background(), file, "", dir); err != nil {
			t.Fatal("valid legacy/explicit display setting import rejected", err)
		}
		stored, err := store.Settings(context.Background())
		if err != nil || !reflect.DeepEqual(stored, settings) {
			t.Fatalf("import changed persisted configuration: %+v %v", stored, err)
		}
	}
	// Do not expand the old backup contract to validate unrelated site fields.
	for _, raw := range []string{`{"title":9,"public_daily_views":1000}`, `{"unknown_legacy_field":true}`, `legacy-site-value`} {
		if err := validatePublicTrafficSettingsJSON([]byte(raw)); err != nil {
			t.Fatalf("new display validation rejected unrelated old fields: %s %v", raw, err)
		}
	}
}
