package blog

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// analyticsFixture publishes "2026-01-01" in WordPress's default UTC+8 zone.
var sidebarFixtureStartedAt = time.Date(2025, 12, 31, 16, 0, 0, 0, time.UTC)

func sidebarAggregate(t *testing.T, app *App, row AnalyticsAggregate) {
	t.Helper()
	if row.Date == "" {
		row.Date = analyticsTestDate.Format("2006-01-02")
	}
	_, err := app.Store.db.ExecContext(context.Background(), `INSERT INTO analytics_aggregates(id_hash,date,kind,label,post_id,pv,uv) VALUES(?,?,?,?,?,?,?)`,
		analyticsAggregateID(row), row.Date, row.Kind, row.Label, row.PostID, row.PV, row.UV)
	if err != nil {
		t.Fatal(err)
	}
}

func sidebarView(t *testing.T, app *App, lang string) publicView {
	t.Helper()
	ctx := context.Background()
	posts, err := app.Store.ListPosts(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := app.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view := newPublicView(settings, lang)
	app.populatePublicSidebar(ctx, &view, settings, posts)
	return view
}

func sidebarSave(t *testing.T, app *App, post Post) {
	t.Helper()
	if err := app.Store.saveLocked(context.Background(), &post); err != nil {
		t.Fatal(err)
	}
}

func TestPublicSidebarAggregateWindowsAndLanguageMerging(t *testing.T) {
	app, _ := analyticsFixture(t)
	post, err := app.Store.GetPost(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	post.Title, post.ContentHTML = "文章 &amp; 内容", "<p>正文</p>"
	post.Translations = map[string]Translation{"en": {Title: "Article &amp; content", ContentHTML: "<p>Content</p>", Status: "publish", SourceHash: SourceHash(post)}}
	sidebarSave(t, app, post)
	for _, row := range []AnalyticsAggregate{
		{Kind: "total", Label: "all", PV: 7},
		{Date: analyticsTestDate.AddDate(0, 0, -29).Format("2006-01-02"), Kind: "total", Label: "all", PV: 11},
		{Date: analyticsTestDate.AddDate(0, 0, -30).Format("2006-01-02"), Kind: "total", Label: "all", PV: 17},
		{Date: analyticsTestDate.AddDate(0, 0, -364).Format("2006-01-02"), Kind: "total", Label: "all", PV: 13},
		{Date: analyticsTestDate.AddDate(0, 0, -365).Format("2006-01-02"), Kind: "total", Label: "all", PV: 999},
		{Date: analyticsTestDate.AddDate(0, 0, 1).Format("2006-01-02"), Kind: "total", Label: "all", PV: 1000},
		{Kind: "page", Label: "/?p=42", PostID: 42, PV: 4},
		{Kind: "page", Label: "/en/?p=42", PostID: 42, PV: 6},
		{Date: analyticsTestDate.AddDate(0, 0, -29).Format("2006-01-02"), Kind: "page", Label: "/index.php?p=42", PostID: 42, PV: 3},
		{Date: analyticsTestDate.AddDate(0, 0, -30).Format("2006-01-02"), Kind: "page", Label: "/?p=42", PostID: 42, PV: 100},
		{Date: analyticsTestDate.AddDate(0, 0, 1).Format("2006-01-02"), Kind: "page", Label: "/?p=42", PostID: 42, PV: 1000},
		{Kind: "page", Label: "/?page_id=43", PostID: 43, PV: 400},
		{Kind: "page", Label: "/?p=44", PostID: 44, PV: 500},
		{Kind: "page", Label: "/?p=45", PostID: 45, PV: 250},
		{Kind: "page", Label: "/?attachment_id=46", PostID: 46, PV: 300},
		{Kind: "page", Label: "/?p=999", PostID: 999, PV: 600},
		{Kind: "page", Label: "/", PV: 10000},
		{Kind: "browser", Label: "Other", PV: 10000},
	} {
		sidebarAggregate(t, app, row)
	}
	counts, err := app.Store.publicSidebarAnalytics(context.Background(), analyticsTestDate)
	if err != nil || counts.stats != (publicSidebarStats{TodayViews: 7, YearViews: 48, HistoricalViews: 1047}) || len(counts.popular) == 0 || counts.popular[len(counts.popular)-1].views != 13 {
		t.Fatalf("raw UTC windows/history must exclude future totals and non-total kinds: %+v, %v", counts, err)
	}
	for _, lang := range []string{"zh", "en"} {
		view := sidebarView(t, app, lang)
		if view.SidebarStats != publicSidebarDisplayStats(publicSidebarStats{TodayViews: 7, YearViews: 48, HistoricalViews: 1047}, analyticsTestDate, sidebarFixtureStartedAt) {
			t.Fatalf("%s totals %+v", lang, view.SidebarStats)
		}
		if len(view.SidebarPopular) != 1 || view.SidebarPopular[0].Views != 5226 {
			t.Fatalf("%s ranking %+v", lang, view.SidebarPopular)
		}
		wantTitle, wantURL := "文章 & 内容", "https://www.ganwen.nl/?p=42"
		if lang == "en" {
			wantTitle, wantURL = "Article & content", "https://www.ganwen.nl/en/?p=42"
		}
		if got := view.SidebarPopular[0]; got.Title != wantTitle || got.URL != wantURL {
			t.Fatalf("%s localization %+v", lang, got)
		}
	}
}

func TestPublicSidebarVisibilityBeforeLimit(t *testing.T) {
	app, _ := analyticsFixture(t)
	for id := int64(100); id < 120; id++ {
		post := Post{ID: id, Type: "post", Status: "publish", Title: fmt.Sprintf("原文 %d", id), ContentHTML: "<p>原文</p>"}
		post.Translations = map[string]Translation{"en": {Title: fmt.Sprintf("Article %d", id), ContentHTML: "<p>Content</p>", Status: "publish", SourceHash: SourceHash(post)}}
		if id < 110 {
			translation := post.Translations["en"]
			switch id % 4 {
			case 0:
				translation.SourceHash = "stale"
			case 1:
				translation.Status = "review"
			case 2:
				translation.Title = ""
			case 3:
				translation.ContentHTML = ""
			}
			post.Translations["en"] = translation
		}
		sidebarSave(t, app, post)
		sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: fmt.Sprintf("/?p=%d", id), PostID: id, PV: 1000 - id})
	}
	for _, id := range []int64{43, 44, 45, 46, 999} {
		sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: fmt.Sprintf("historical-%d", id), PostID: id, PV: 10000})
	}
	zero := Post{ID: 120, Type: "post", Status: "publish", Title: "Zero views", ContentHTML: "<p>zero</p>"}
	zero.Translations = map[string]Translation{"en": {Title: zero.Title, ContentHTML: zero.ContentHTML, Status: "publish", SourceHash: SourceHash(zero)}}
	sidebarSave(t, app, zero)
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: "/?p=120", PostID: 120})
	view := sidebarView(t, app, "en")
	if len(view.SidebarPopular) != 8 {
		t.Fatalf("filtering before limit: %+v", view.SidebarPopular)
	}
	wantViews := [...]int64{5877, 4616, 4076, 3413, 2919, 2601, 2193, 2033}
	for i, got := range view.SidebarPopular {
		id := int64(110 + i)
		if got.Title != fmt.Sprintf("Article %d", id) || got.Views != wantViews[i] {
			t.Fatalf("rank %d: %+v", i, got)
		}
	}
	// SQL publication status must also fence a still-published JSON document.
	if _, err := app.Store.db.ExecContext(context.Background(), "UPDATE posts SET status='private' WHERE id=110"); err != nil {
		t.Fatal(err)
	}
	app.Store.postsMu.Lock()
	app.Store.posts = nil
	app.Store.postsMu.Unlock()
	view = sidebarView(t, app, "en")
	if len(view.SidebarPopular) != 8 || view.SidebarPopular[0].Title != "Article 111" || view.SidebarPopular[7].Title != "Article 118" {
		t.Fatalf("SQL status fence: %+v", view.SidebarPopular)
	}
}

func TestPublicSidebarCacheExpirationAndLivePublication(t *testing.T) {
	app, _ := analyticsFixture(t)
	now := analyticsTestDate
	app.Analytics.now = func() time.Time { return now }
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: 7})
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: "/?p=42", PostID: 42, PV: 5})
	first := sidebarView(t, app, "zh")
	if first.SidebarStats != publicSidebarDisplayStats(publicSidebarStats{TodayViews: 7, YearViews: 7, HistoricalViews: 7}, now, sidebarFixtureStartedAt) || len(first.SidebarPopular) != 1 || first.SidebarPopular[0].Views != 5218 {
		t.Fatalf("initial %+v %+v", first.SidebarStats, first.SidebarPopular)
	}
	if _, err := app.Store.db.ExecContext(context.Background(), "UPDATE analytics_aggregates SET pv=21 WHERE kind='total'"); err != nil {
		t.Fatal(err)
	}
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: "/en/?p=42", PostID: 42, PV: 9})
	now = now.Add(10 * time.Second)
	post, err := app.Store.GetPost(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	post.Title, post.CanonicalURL = "更名文章", "https://www.ganwen.nl/index.php?p=42"
	sidebarSave(t, app, post)
	view := sidebarView(t, app, "zh")
	if view.SidebarStats != publicSidebarDisplayStats(publicSidebarStats{TodayViews: 7, YearViews: 7, HistoricalViews: 7}, now, sidebarFixtureStartedAt) || len(view.SidebarPopular) != 1 || view.SidebarPopular[0] != (publicPopular{Title: post.Title, URL: post.CanonicalURL, Views: 5218}) {
		t.Fatalf("cached counts/current metadata: %+v %+v", view.SidebarStats, view.SidebarPopular)
	}
	post.Status = "private"
	sidebarSave(t, app, post)
	if hidden := sidebarView(t, app, "zh"); len(hidden.SidebarPopular) != 0 {
		t.Fatalf("warm cache exposed withdrawn post %+v", hidden.SidebarPopular)
	}
	post.Status = "publish"
	post.Translations = map[string]Translation{"en": {Title: "New article", ContentHTML: "<p>Content</p>", Status: "publish", SourceHash: "stale"}}
	sidebarSave(t, app, post)
	if hidden := sidebarView(t, app, "en"); len(hidden.SidebarPopular) != 0 {
		t.Fatalf("warm cache exposed stale translation %+v", hidden.SidebarPopular)
	}
	translation := post.Translations["en"]
	translation.SourceHash = SourceHash(post)
	post.Translations["en"] = translation
	sidebarSave(t, app, post)
	if current := sidebarView(t, app, "en"); len(current.SidebarPopular) != 1 || current.SidebarPopular[0].Title != "New article" || current.SidebarPopular[0].Views != 5218 {
		t.Fatalf("translation not immediately visible %+v", current.SidebarPopular)
	}
	now = analyticsTestDate.Add(time.Minute)
	view = sidebarView(t, app, "zh")
	if view.SidebarStats != publicSidebarDisplayStats(publicSidebarStats{TodayViews: 21, YearViews: 21, HistoricalViews: 21}, now, sidebarFixtureStartedAt) || len(view.SidebarPopular) != 1 || view.SidebarPopular[0].Views != 5227 {
		t.Fatalf("expired cache %+v %+v", view.SidebarStats, view.SidebarPopular)
	}
}

func TestPublicSidebarUTCDaySwitchAndConcurrentReads(t *testing.T) {
	app, _ := analyticsFixture(t)
	now := time.Date(2026, 10, 8, 23, 59, 50, 0, time.UTC).In(time.FixedZone("UTC+8", 8*60*60))
	app.Analytics.now = func() time.Time { return now }
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: 8})
	sidebarAggregate(t, app, AnalyticsAggregate{Date: "2026-10-09", Kind: "total", Label: "all", PV: 3})
	if got := sidebarView(t, app, "zh").SidebarStats; got != publicSidebarDisplayStats(publicSidebarStats{TodayViews: 8, YearViews: 8, HistoricalViews: 8}, now, sidebarFixtureStartedAt) {
		t.Fatalf("UTC before midnight %+v", got)
	}
	now = now.Add(20 * time.Second)
	if got := sidebarView(t, app, "zh").SidebarStats; got != publicSidebarDisplayStats(publicSidebarStats{TodayViews: 3, YearViews: 11, HistoricalViews: 11}, now, sidebarFixtureStartedAt) {
		t.Fatalf("day changed within TTL %+v", got)
	}
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Go(func() {
			counts := app.Analytics.publicSidebarCounts(context.Background())
			if counts.stats != (publicSidebarStats{TodayViews: 3, YearViews: 11, HistoricalViews: 11}) {
				t.Errorf("concurrent cache %+v", counts.stats)
			}
		})
	}
	group.Wait()
}

func TestPublicSidebarErrorsFailClosedWithoutBreakingPage(t *testing.T) {
	app, _ := analyticsFixture(t)
	now := analyticsTestDate
	app.Analytics.now = func() time.Time { return now }
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: 7})
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: "/?p=42", PostID: 42, PV: 5})
	_ = sidebarView(t, app, "zh")
	if _, err := app.Store.db.ExecContext(context.Background(), "DROP TABLE analytics_aggregates"); err != nil {
		t.Fatal(err)
	}
	if warm := sidebarView(t, app, "zh"); warm.SidebarStats != publicSidebarDisplayStats(publicSidebarStats{TodayViews: 7, YearViews: 7, HistoricalViews: 7}, now, sidebarFixtureStartedAt) || len(warm.SidebarPopular) != 1 {
		t.Fatal("warm cache unexpectedly queried the unavailable analytics table")
	}
	now = now.Add(time.Minute)
	view := sidebarView(t, app, "zh")
	if view.SidebarStats != publicSidebarDisplayStats(publicSidebarStats{}, now, sidebarFixtureStartedAt) || len(view.SidebarPopular) != 0 {
		t.Fatalf("SQL error retained stale result %+v %+v", view.SidebarStats, view.SidebarPopular)
	}
	if app.Analytics.sidebarCache.day == "" || !app.Analytics.sidebarCache.expires.After(now) {
		t.Fatal("failure was not cached")
	}
	w := publicTestRequest(app, "/?p=42")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-cache" || !strings.Contains(w.Header().Get("Vary"), "Cookie") {
		t.Fatalf("analytics failure broke page/cache policy: %d %+v", w.Code, w.Header())
	}
}

func TestPublicSidebarCanceledReadDoesNotPoisonCache(t *testing.T) {
	app, _ := analyticsFixture(t)
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: 7})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if counts := app.Analytics.publicSidebarCounts(ctx); counts.stats != (publicSidebarStats{}) || len(counts.popular) != 0 {
		t.Fatalf("canceled query returned counts %+v", counts)
	}
	if app.Analytics.sidebarCache.day != "" {
		t.Fatal("request cancellation poisoned shared cache")
	}
	if got := sidebarView(t, app, "zh").SidebarStats.TodayViews; got != publicSidebarDisplayStats(publicSidebarStats{TodayViews: 7, YearViews: 7, HistoricalViews: 7}, analyticsTestDate, sidebarFixtureStartedAt).TodayViews {
		t.Fatalf("valid request did not recover: %d", got)
	}
}

func TestPublicSidebarNoRecordedViewsAndRenderedRanking(t *testing.T) {
	app, _ := analyticsFixture(t)
	enableArticleSidebar(t, app)
	view := sidebarView(t, app, "zh")
	if view.SidebarStats != publicSidebarDisplayStats(publicSidebarStats{}, analyticsTestDate, sidebarFixtureStartedAt) || len(view.SidebarPopular) != 0 {
		t.Fatalf("empty analytics fabricated popularity %+v %+v", view.SidebarStats, view.SidebarPopular)
	}
	w := publicTestRequest(app, "/?p=42")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "暂无浏览记录") || !strings.Contains(w.Body.String(), fmt.Sprintf("<bdi>%d</bdi>", view.SidebarStats.TodayViews)) {
		t.Fatalf("empty sidebar rendering %d %s", w.Code, w.Body.String())
	}
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: "/?p=42", PostID: 42, PV: 12})
	app.Analytics.sidebarMu.Lock()
	app.Analytics.sidebarCache = publicSidebarCache{}
	app.Analytics.sidebarMu.Unlock()
	w = publicTestRequest(app, "/?p=42")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `class="popular-rank" aria-hidden="true">1</span>`) || !strings.Contains(w.Body.String(), "<bdi>5225</bdi>") {
		t.Fatalf("ranking not rendered %d %s", w.Code, w.Body.String())
	}
}

func TestPublicSidebarStartDateIgnoresNonPublicAndInvalidArticles(t *testing.T) {
	baseline := Post{ID: 42, Type: "post", Status: "publish", PublishedAt: "2026-01-01"}
	for _, test := range []struct {
		name string
		post Post
	}{
		{"private", Post{Type: "post", Status: "private", PublishedAt: "2000-01-01"}},
		{"draft", Post{Type: "post", Status: "draft", PublishedAt: "2000-01-01"}},
		{"scheduled status", Post{Type: "post", Status: "future", PublishedAt: "2000-01-01"}},
		{"page", Post{Type: "page", Status: "publish", PublishedAt: "2000-01-01"}},
		{"attachment", Post{Type: "attachment", Status: "publish", PublishedAt: "2000-01-01"}},
		{"future date", Post{Type: "post", Status: "publish", PublishedAt: "2027-01-01"}},
		{"invalid date", Post{Type: "post", Status: "publish", PublishedAt: "2000-99-99"}},
		{"placeholder year", Post{Type: "post", Status: "publish", PublishedAt: "0001-01-01"}},
		{"empty date", Post{Type: "post", Status: "publish"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := publicSidebarStartDate([]Post{test.post}, analyticsTestDate); !got.IsZero() {
				t.Fatalf("excluded article became history start: %s", got)
			}
			if got := publicSidebarStartDate([]Post{test.post, baseline}, analyticsTestDate); !got.Equal(sidebarFixtureStartedAt) {
				t.Fatalf("excluded article changed global history start: %s", got)
			}
		})
	}
}

func TestPublicSidebarHistoryUsesGlobalOriginalArticlesAcrossRoutes(t *testing.T) {
	app, _ := analyticsFixture(t)
	enableArticleSidebar(t, app)
	ctx := context.Background()
	earliest, err := app.Store.GetPost(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	// This original article has no English translation and never matches search.
	earliest.PublishedAt = "2018-05-12"
	sidebarSave(t, app, earliest)
	article := Post{ID: 47, Type: "post", Status: "publish", Title: "needle-history", ContentHTML: "<p>needle-history</p>", PublishedAt: "2025-01-01"}
	article.Translations = map[string]Translation{"en": {Title: article.Title, ContentHTML: article.ContentHTML, Status: "publish", SourceHash: SourceHash(article)}}
	sidebarSave(t, app, article)
	startedAt := time.Date(2018, 5, 11, 16, 0, 0, 0, time.UTC)
	want := publicSidebarDisplayStats(publicSidebarStats{}, analyticsTestDate, startedAt).HistoricalViews
	for _, lang := range []string{"zh", "en"} {
		if got := sidebarView(t, app, lang).SidebarStats.HistoricalViews; got != want {
			t.Fatalf("%s history %d, want %d", lang, got, want)
		}
	}
	for _, target := range []string{"/", "/en/", "/?s=needle-history", "/en/?s=needle-history", "/?s=no-matching-history", "/?p=47", "/en/?p=47", "/?page_id=43"} {
		t.Run(target, func(t *testing.T) {
			w := publicTestRequest(app, target)
			if w.Code != http.StatusOK || strings.Count(w.Body.String(), fmt.Sprintf("<bdi>%d</bdi>", want)) != 1 {
				t.Fatalf("route did not keep global history %d: %d %s", want, w.Code, w.Body.String())
			}
		})
	}
}
