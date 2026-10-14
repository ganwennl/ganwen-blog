package blog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var analyticsTestDate = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

const analyticsTestUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36"

func analyticsFixture(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "blog.db")
	s, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	app, err := NewApp(s, Config{DataDir: dir, BaseURL: "https://www.ganwen.nl", Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	app.Analytics.now = func() time.Time { return analyticsTestDate }
	for _, p := range []Post{{ID: 42, Type: "post", Status: "publish", Title: "公开文章", CanonicalURL: "https://www.ganwen.nl/?p=42", PublishedAt: "2026-01-01"}, {ID: 43, Type: "page", Status: "publish", Title: "公开页面", CanonicalURL: "https://www.ganwen.nl/?page_id=43"}, {ID: 44, Type: "post", Status: "private", Title: "PRIVATE_SENTINEL"}, {ID: 45, Type: "post", Status: "draft", Title: "DRAFT_SENTINEL"}, {ID: 46, Type: "attachment", Status: "publish"}} {
		if err = s.saveLocked(context.Background(), &p); err != nil {
			t.Fatal(err)
		}
	}
	return app, file
}
func analyticsEventRequest(app *App, payload any, adjust func(*http.Request)) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)
	r := httptest.NewRequest(http.MethodPost, "http://example.test/analytics/event", bytes.NewReader(body))
	r.Header.Set("Origin", "http://example.test")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", analyticsTestUA)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.RemoteAddr = "192.0.2.10:4567"
	if adjust != nil {
		adjust(r)
	}
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	return w
}
func analyticsPayloadFor(index int, path string) analyticsPayload {
	return analyticsPayload{EventID: fmt.Sprintf("%032x", index), VisitorID: "visitor_0123456789abcdefgh", SessionID: "session_0123456789abcdefgh", Path: path, Language: "zh"}
}
func analyticsGetReport(t *testing.T, app *App) AnalyticsReport {
	t.Helper()
	report, err := app.Store.AnalyticsReport(context.Background(), analyticsTestDate.Truncate(24*time.Hour).AddDate(0, 0, -6), app.Analytics.now().UTC().Truncate(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestAnalyticsCollectionDedupePrivacyAndPersistence(t *testing.T) {
	app, file := analyticsFixture(t)
	first := analyticsPayloadFor(1, "/?p=42")
	first.Referrer = "https://search.example/path?q=PRIVATE_REFERRER_SECRET#private"
	for _, p := range []analyticsPayload{first, first, analyticsPayloadFor(2, "/?p=42"), analyticsPayloadFor(3, "/?page_id=43"), analyticsPayloadFor(4, "/?s=PRIVATE_SEARCH_SECRET")} {
		w := analyticsEventRequest(app, p, nil)
		if w.Code != 204 || w.Header().Get("Cache-Control") != "no-store" || len(w.Body.Bytes()) != 0 || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("beacon %d %s", w.Code, w.Body.String())
		}
	}
	report := analyticsGetReport(t, app)
	if report.PV != 4 || report.UVDailySum != 1 || len(report.Trend) != 7 || len(report.TopPages) != 3 {
		t.Fatalf("wrong metrics: %+v", report)
	}
	if report.Trend[6].UV != 1 || report.Trend[6].PV != 4 {
		t.Fatal("daily count mismatch")
	}
	if analyticsSource("https://203.0.113.88/path?q=secret", "example.test") != "unknown" {
		t.Fatal("raw referrer IP retained")
	}
	backup, err := app.Store.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(backup)
	for _, private := range []string{first.VisitorID, first.EventID, first.SessionID, "192.0.2.10", analyticsTestUA, "PRIVATE_SEARCH_SECRET", "PRIVATE_REFERRER_SECRET"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("stored raw private data %q", private)
		}
	}
	if len(backup.Analytics.Events) != 4 || len(backup.Analytics.Visitors) != 1 {
		t.Fatal("dedupe rows not persisted")
	}
	second, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	other, err := NewApp(second, app.Config)
	if err != nil {
		t.Fatal(err)
	}
	other.Analytics.now = app.Analytics.now
	if w := analyticsEventRequest(other, first, nil); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if got := analyticsGetReport(t, other); got.PV != 4 || got.UVDailySum != 1 {
		t.Fatal("restart lost dedupe")
	}
	other.Analytics.now = func() time.Time { return analyticsTestDate.AddDate(0, 0, 1) }
	if w := analyticsEventRequest(other, analyticsPayloadFor(5, "/?p=42"), nil); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if got := analyticsGetReport(t, other); got.PV != 5 || got.UVDailySum != 2 {
		t.Fatal("cross-day UV is not daily anonymous sessions")
	}
	newBackup, _ := second.DatabaseSnapshot(context.Background())
	if newBackup.Analytics.Visitors[0].Hash == newBackup.Analytics.Visitors[1].Hash {
		t.Fatal("visitor linked across days")
	}
}

func TestAnalyticsInvalidOriginJSONPathsBotsAndRate(t *testing.T) {
	app, _ := analyticsFixture(t)
	for _, adjust := range []func(*http.Request){func(r *http.Request) { r.Header.Del("Origin") }, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, func(r *http.Request) { r.Header.Add("Origin", "http://example.test") }, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }} {
		if w := analyticsEventRequest(app, analyticsPayloadFor(1, "/"), adjust); w.Code != 403 {
			t.Fatalf("forged origin accepted %d", w.Code)
		}
	}
	for _, path := range []string{"/admin/", "/wp-admin/", "/?p=44", "/?p=45", "/?p=46", "/?p=99999", "/?page_id=42", "/?p=42&page_id=43", "/?p=42&p=42", "/?p=-1", "/robots.txt", "/sitemap.xml", "/?preview=true", "/?feed=rss2", "/?paged=99", "/?cat=99999", "/?attachment_id=46", "https://evil.example/", "//evil.example/", "/en/?p=42", "/wp-content/uploads/a.jpg", "/bewijs.html"} {
		p := analyticsPayloadFor(1, path)
		if strings.HasPrefix(path, "/en/") {
			p.Language = "en"
		}
		if w := analyticsEventRequest(app, p, nil); w.Code != 400 {
			t.Fatalf("invalid page %s accepted %d", path, w.Code)
		}
	}
	for _, ua := range []string{"Googlebot/2.1", "curl/8.0", "HeadlessChrome/130", ""} {
		if w := analyticsEventRequest(app, analyticsPayloadFor(1, "/"), func(r *http.Request) { r.Header.Set("User-Agent", ua) }); w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	for _, header := range []string{"DNT", "Sec-GPC"} {
		if w := analyticsEventRequest(app, analyticsPayloadFor(1, "/"), func(r *http.Request) { r.Header.Set(header, "1") }); w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	if got := analyticsGetReport(t, app); got.PV != 0 {
		t.Fatal("ignored event counted")
	}
	if w := analyticsEventRequest(app, map[string]any{"visitor_id": "short", "event_id": "short", "path": "/"}, nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := analyticsEventRequest(app, map[string]any{"event_id": strings.Repeat("A", 5000)}, nil); w.Code != 413 {
		t.Fatalf("oversize accepted %d", w.Code)
	}
	if w := analyticsEventRequest(app, map[string]any{"unrecognized": "field"}, nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := analyticsEventRequest(app, analyticsPayloadFor(1, "/"), func(r *http.Request) { r.Method = "GET" }); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := analyticsEventRequest(app, analyticsPayloadFor(1, "/"), func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); w.Code != 415 {
		t.Fatal(w.Code)
	}
	var limited bool
	for i := 100; i < 250; i++ {
		w := analyticsEventRequest(app, analyticsPayloadFor(i, "/"), nil)
		if w.Code == 429 {
			limited = true
			break
		}
		if w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	if !limited {
		t.Fatal("per-IP rate limit missing")
	}
	app.Analytics.now = func() time.Time { return analyticsTestDate.Add(time.Minute) }
	if w := analyticsEventRequest(app, analyticsPayloadFor(500, "/?s="), nil); w.Code != 204 {
		t.Fatalf("rate reset/search category failed %d", w.Code)
	}
}

func TestAnalyticsTrustedCloudflareAndAgentClassification(t *testing.T) {
	r := httptest.NewRequest("POST", "https://www.ganwen.nl/analytics/event", nil)
	r.RemoteAddr = "127.0.0.1:5432"
	r.Header.Set("X-Real-IP", "104.16.1.2")
	r.Header.Set("CF-Connecting-IP", "203.0.113.11")
	r.Header.Set("CF-IPCountry", "NL")
	if country, client := analyticsCloudflare(r, Config{}); country != "NL" || client != "203.0.113.11" {
		t.Fatalf("trusted CF rejected %s %s", country, client)
	}
	for _, change := range []func(*http.Request){func(r *http.Request) { r.RemoteAddr = "198.51.100.5:1000" }, func(r *http.Request) { r.Header.Set("X-Real-IP", "198.51.100.9") }, func(r *http.Request) { r.Header.Add("X-Real-IP", "104.16.1.2") }, func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "203.0.113.11, 203.0.113.12") }, func(r *http.Request) { r.Header.Set("CF-Connecting-IP", "127.0.0.1") }} {
		copy := r.Clone(r.Context())
		copy.Header = r.Header.Clone()
		change(copy)
		if country, client := analyticsCloudflare(copy, Config{}); country != "unknown" || client != "" {
			t.Fatal("spoofed CF trusted")
		}
	}
	if country, _ := analyticsCloudflare(r, Config{Preview: true}); country != "unknown" {
		t.Fatal("preview trusted forwarded headers")
	}
	for _, code := range []string{"XX", "T1", "Netherlands", "N1", ""} {
		r.Header.Set("CF-IPCountry", code)
		if country, _ := analyticsCloudflare(r, Config{}); country != "unknown" {
			t.Fatal("invalid country counted")
		}
	}
	r.Header.Set("CF-IPCountry", "ES")
	r.Header.Set("X-Real-IP", "2606:4700::1111")
	if country, _ := analyticsCloudflare(r, Config{}); country != "ES" {
		t.Fatal("IPv6 CF rejected")
	}
	if d, b, o := analyticsAgent(analyticsTestUA); d != "desktop" || b != "Chrome" || o != "Windows" {
		t.Fatal(d, b, o)
	}
	if d, b, o := analyticsAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Version/17 Safari/605"); d != "mobile" || b != "Safari" || o != "iOS" {
		t.Fatal(d, b, o)
	}
}

func TestAnalyticsPublishedTranslationsAndHTTPCloudflareDimensions(t *testing.T) {
	app, _ := analyticsFixture(t)
	ctx := context.Background()
	post, err := app.Store.GetPost(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	post.Translations = map[string]Translation{"en": {Title: "Public article", ContentHTML: "<p>Public content</p>", Status: "publish", SourceHash: SourceHash(post)}, "es": {Title: "Review article", ContentHTML: "<p>Review content</p>", Status: "review", SourceHash: SourceHash(post)}}
	if err = app.Store.saveLocked(ctx, &post); err != nil {
		t.Fatal(err)
	}
	p := analyticsPayloadFor(1, "/en/?p=42")
	p.Language = "en"
	if w := analyticsEventRequest(app, p, nil); w.Code != 204 {
		t.Fatal("published translation rejected", w.Code)
	}
	p = analyticsPayloadFor(2, "/es/?p=42")
	p.Language = "es"
	if w := analyticsEventRequest(app, p, nil); w.Code != 400 {
		t.Fatal("review translation counted", w.Code)
	}
	app.Config.Preview = false
	p = analyticsPayloadFor(3, "/?p=42")
	w := analyticsEventRequest(app, p, func(r *http.Request) {
		r.Host = "www.ganwen.nl"
		r.Header.Set("Origin", "https://www.ganwen.nl")
		r.RemoteAddr = "127.0.0.1:54321"
		r.Header.Set("X-Real-IP", "104.16.1.2")
		r.Header.Set("CF-Connecting-IP", "203.0.113.77")
		r.Header.Set("CF-IPCountry", "NL")
	})
	if w.Code != 204 {
		t.Fatal("trusted CF beacon failed", w.Code)
	}
	report := analyticsGetReport(t, app)
	if report.PV != 2 || len(report.Languages) != 2 || len(report.Countries) != 2 {
		t.Fatal("translated/country dimensions missing", report)
	}
}

func TestAnalyticsMultipleStoresDatabaseDedupe(t *testing.T) {
	app, file := analyticsFixture(t)
	other, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stores := []*Store{app.Store, other}
	ctx := context.Background()
	var wg sync.WaitGroup
	failures := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := stores[i%2].RecordAnalytics(ctx, analyticsTestDate, "same_event_0123456789", "same_visitor_0123456789", []AnalyticsAggregate{{Kind: "page", Label: "/?p=42", PostID: 42}})
			if err != nil {
				failures <- err
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if got := analyticsGetReport(t, app); got.PV != 1 || got.UVDailySum != 1 {
		t.Fatalf("duplicate event across stores %+v", got)
	}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := stores[i%2].RecordAnalytics(ctx, analyticsTestDate, fmt.Sprintf("unique_event_%032x", i), "same_visitor_0123456789", nil)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got := analyticsGetReport(t, app); got.PV != 17 || got.UVDailySum != 1 {
		t.Fatalf("duplicate UV across stores %+v", got)
	}
}

func TestAnalyticsExportRestoreLegacyAndOccupiedDestination(t *testing.T) {
	app, _ := analyticsFixture(t)
	if w := analyticsEventRequest(app, analyticsPayloadFor(1, "/?p=42"), nil); w.Code != 204 {
		t.Fatal(w.Code)
	}
	ctx := context.Background()
	before, err := app.Store.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "export.json")
	if err = app.Store.ExportDatabaseFile(ctx, file, app.Config.DataDir); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	restored, err := OpenStore(filepath.Join(dir, "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.ImportDatabaseFile(ctx, file, "", dir); err != nil {
		t.Fatal(err)
	}
	after, err := restored.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before.CreatedAt = after.CreatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("full logical restore lost analytics or private salt")
	}
	if inserted, err := restored.RecordAnalytics(ctx, analyticsTestDate, analyticsPayloadFor(1, "/").EventID, analyticsPayloadFor(1, "/").VisitorID, nil); err != nil || inserted {
		t.Fatal("restored event dedupe lost")
	}
	legacy := before
	legacy.Analytics = nil
	legacy.Values = nil
	old, err := OpenStore(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err = old.RestoreDatabase(ctx, legacy); err != nil {
		t.Fatal("legacy v1 cannot restore", err)
	}
	for _, table := range []string{"analytics_aggregates", "analytics_visitors", "analytics_events"} {
		occupied, err := OpenStore(filepath.Join(t.TempDir(), "occupied.db"))
		if err != nil {
			t.Fatal(err)
		}
		if table == "analytics_aggregates" {
			_, err = occupied.db.Exec("INSERT INTO analytics_aggregates VALUES(?,?,?,?,?,?,?)", strings.Repeat("a", 64), "2026-10-08", "total", "all", 0, 1, 1)
		} else {
			_, err = occupied.db.Exec("INSERT INTO "+table+" VALUES(?,?)", strings.Repeat("a", 64), "2026-10-08")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = occupied.RestoreDatabase(ctx, legacy); err == nil {
			t.Fatal("overwrote occupied ", table)
		}
		occupied.Close()
	}
	for _, mutate := range []func(*DatabaseBackup){func(b *DatabaseBackup) { b.Analytics.Aggregates[0].Kind = "raw_ip" }, func(b *DatabaseBackup) { b.Analytics.Events[0].Hash = "plain_event" }, func(b *DatabaseBackup) { b.Analytics.Aggregates[0].PV = -1 }, func(b *DatabaseBackup) {
		b.Analytics.Aggregates = append(b.Analytics.Aggregates, b.Analytics.Aggregates[0])
	}, func(b *DatabaseBackup) { b.Values = nil }} {
		raw, _ := json.Marshal(before)
		var invalid DatabaseBackup
		json.Unmarshal(raw, &invalid)
		mutate(&invalid)
		if err = validateDatabaseBackup(invalid); err == nil {
			t.Fatal("malformed analytics backup accepted")
		}
	}
	if raw, err := os.ReadFile(file); err != nil || !bytes.Contains(raw, []byte(`"analytics"`)) {
		t.Fatal("analytics absent from file export")
	}
}

func TestAnalyticsRetentionAndMySQLDialect(t *testing.T) {
	app, _ := analyticsFixture(t)
	ctx := context.Background()
	oldDate := analyticsTestDate.AddDate(0, 0, -400).Format(time.DateOnly)
	for i := 0; i < 305; i++ {
		hash := fmt.Sprintf("%064x", i)
		if _, err := app.Store.db.Exec("INSERT INTO analytics_events VALUES(?,?)", hash, oldDate); err != nil {
			t.Fatal(err)
		}
	}
	old := AnalyticsAggregate{Date: oldDate, Kind: "total", Label: "all", PV: 1, UV: 1}
	if _, err := app.Store.db.Exec("INSERT INTO analytics_aggregates VALUES(?,?,?,?,?,?,?)", analyticsAggregateID(old), old.Date, old.Kind, old.Label, 0, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.db.Exec("INSERT INTO analytics_visitors VALUES(?,?)", strings.Repeat("f", 64), oldDate); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.RecordAnalytics(ctx, analyticsTestDate, "fresh_event_0123456789", "fresh_visitor_0123456789", nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := app.Store.db.QueryRow("SELECT COUNT(*) FROM analytics_events WHERE date=?", oldDate).Scan(&n); err != nil || n != 5 {
		t.Fatalf("unbounded cleanup or retention failed %d %v", n, err)
	}
	for _, table := range []string{"analytics_visitors", "analytics_aggregates"} {
		if err := app.Store.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE date=?", oldDate).Scan(&n); err != nil || n != 0 {
			t.Fatal("expired visitor/aggregate retained", table, n, err)
		}
	}
	mysql := &Store{driver: "mysql"}
	for _, query := range append(mysql.analyticsSchema(), mysql.analyticsInsertIgnore("analytics_events"), mysql.analyticsUpsert()) {
		if strings.Contains(query, "ON CONFLICT") || strings.Contains(query, "AUTOINCREMENT") {
			t.Fatal("SQLite syntax leaked into MySQL")
		}
	}
	for _, table := range []string{"posts", "kv", "translation_jobs", "analytics_aggregates", "analytics_events", "analytics_visitors"} {
		if !blogDatabaseTable(table) {
			t.Fatal("legitimate table rejected", table)
		}
	}
	for _, table := range []string{"wp_posts", "users", "analytics_raw", "POSTS"} {
		if blogDatabaseTable(table) {
			t.Fatal("foreign table accepted", table)
		}
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "foreign.db")
	foreign, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.db.Exec("CREATE TABLE unrelated (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	foreign.Close()
	if opened, err := OpenStore(file); err == nil {
		opened.Close()
		t.Fatal("foreign SQLite accepted")
	}
	cfg, err := mysqlConnectionConfig("test:fake@tcp(127.0.0.1:3306)/ganwen_blog_test_analytics?clientFoundRows=true")
	if err != nil || cfg.ClientFoundRows {
		t.Fatal("dedupe affected-row semantics not enforced")
	}
	distribution := map[string]int64{}
	for i := 0; i < 200; i++ {
		distribution[fmt.Sprintf("site%d.example", i)] = 1
	}
	rows := sortAnalyticsDistribution(distribution)
	var sum int64
	for _, row := range rows {
		sum += row.PV
	}
	if len(rows) > 100 || sum != 200 {
		t.Fatal("distribution truncation lost counts")
	}
}

func TestAnalyticsAdminProbeAuthorizationAndCreatePage(t *testing.T) {
	c := adminTestSetup(t, true)
	pageInput := Post{Type: "page", Status: "draft", Title: "页面", ContentHTML: "<p>draft</p>"}
	if code, _ := c.request("POST", "create", pageInput, c.server.URL, c.csrf); code != 401 {
		t.Fatal("anonymous page creation accepted", code)
	}
	for _, path := range []string{"/admin/api/editor-session", "/admin/api/analytics?days=7"} {
		r := httptest.NewRequest("GET", "http://example.test"+path, nil)
		w := httptest.NewRecorder()
		c.app.Handler().ServeHTTP(w, r)
		if path == "/admin/api/editor-session" {
			if w.Code != 200 || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Vary") != "Cookie" || w.Body.String() != "{\"authenticated\":false}\n" {
				t.Fatalf("anonymous probe side effect %+v", w)
			}
		} else if w.Code != 401 {
			t.Fatal("stats unauthenticated", w.Code)
		}
	}
	c.login()
	code, body := c.request("GET", "editor-session", nil, "", "")
	if code != 200 || string(body) != "{\"authenticated\":true}\n" {
		t.Fatal(code, string(body))
	}
	code, body = c.request("GET", "analytics?days=7", nil, "", "")
	var report AnalyticsReport
	if code != 200 || json.Unmarshal(body, &report) != nil || report.PV != 0 || len(report.Trend) != 7 {
		t.Fatal(code, string(body))
	}
	for _, query := range []string{"days=1", "days=365", "start=2026-02-30&end=2026-10-08", "start=2026-10-08", "days=7&start=2026-10-01&end=2026-10-08"} {
		if _, _, err := analyticsRange(mustAnalyticsQuery(query), analyticsTestDate); err == nil {
			t.Fatal("bad range accepted", query)
		}
	}
	for _, query := range []string{"days=7", "days=30", "days=90", "start=2026-10-01&end=2026-10-08", "from=2026-10-01&to=2026-10-08"} {
		if _, _, err := analyticsRange(mustAnalyticsQuery(query), analyticsTestDate); err != nil {
			t.Fatal(err)
		}
	}
	page := Post{Type: "page", Status: "draft", Title: "新建页面", ContentHTML: "<p>页面</p>"}
	code, body = c.request("POST", "create", page, c.server.URL, c.csrf)
	var created struct {
		Post Post `json:"post"`
	}
	if code != 200 || json.Unmarshal(body, &created) != nil || created.Post.Type != "page" || !strings.Contains(created.Post.CanonicalURL, "page_id=") {
		t.Fatal("page creation failed", code, string(body))
	}
	page.Type = "attachment"
	if code, _ = c.request("POST", "create", page, c.server.URL, c.csrf); code != 400 {
		t.Fatal("invalid type created", code)
	}
	page.Type = "page"
	if code, _ = c.request("POST", "create", page, "https://evil.example", c.csrf); code != 403 {
		t.Fatal("create CSRF accepted", code)
	}
	for _, suffix := range []string{"", "?embed=1"} {
		r := httptest.NewRequest("GET", "http://example.test/admin/"+suffix, nil)
		w := httptest.NewRecorder()
		c.app.Handler().ServeHTTP(w, r)
		want := "frame-ancestors 'none'"
		if suffix != "" {
			want = "frame-ancestors 'self'"
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), want) {
			t.Fatal("embed CSP wrong")
		}
	}
	r := httptest.NewRequest("POST", c.server.URL+"/analytics/event", strings.NewReader("{}"))
	r.Header.Set("Origin", c.server.URL)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", analyticsTestUA)
	u, _ := url.Parse(c.server.URL + "/admin/api/editor-session")
	for _, cookie := range c.client.Jar.Cookies(u) {
		if cookie.Name == adminCookieName(sessionCookie, r, c.app.Config) {
			r.AddCookie(cookie)
		}
	}
	w := httptest.NewRecorder()
	c.app.Handler().ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal("admin event not excluded", w.Code)
	}
}
func mustAnalyticsQuery(raw string) url.Values { q, _ := url.ParseQuery(raw); return q }
