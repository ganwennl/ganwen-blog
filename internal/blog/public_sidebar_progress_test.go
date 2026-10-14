package blog

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPublicSidebarDailyGrowthWholeDayStableAndMonotonic(t *testing.T) {
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for _, target := range []int64{0, 1000, 10000} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			if got := publicSidebarDisplayDailyGrowth(day, target); got != 0 {
				t.Fatalf("UTC midnight contains a synthetic base: %d", got)
			}
			var previous int64
			for second := 0; second < 86400; second++ {
				stamp := day.Add(time.Duration(second) * time.Second)
				got := publicSidebarDisplayDailyGrowth(stamp, target)
				if got < previous || got > target || got-previous > 1 {
					t.Fatalf("second %d grew discontinuously: %d -> %d, target %d", second, previous, got, target)
				}
				previous = got
			}
			wantLast := max(int64(0), target-1)
			if previous != wantLast || publicSidebarDisplayDailyGrowth(day.Add(24*time.Hour), target) != 0 {
				t.Fatalf("day did not reach its goal before resetting: last=%d goal=%d", previous, target)
			}
			stamp := day.Add(13*time.Hour + 17*time.Minute + 53*time.Second)
			saved := publicSidebarDisplayDailyGrowth(stamp, target)
			for repeat := 0; repeat < 10; repeat++ {
				_ = publicSidebarDisplayDailyGrowth(stamp.AddDate(0, 0, repeat+1), target)
				if got := publicSidebarDisplayDailyGrowth(stamp, target); got != saved {
					t.Fatalf("other dates/refresh changed the saved pace: %d -> %d", saved, got)
				}
				if got := publicSidebarDisplayDailyGrowth(stamp.In(time.FixedZone("UTC+8", 8*60*60)), target); got != saved {
					t.Fatalf("timezone changed the same instant: %d -> %d", saved, got)
				}
			}
		})
	}
}

func TestPublicSidebarDailyGrowthVariesByDateAndRemainsLinearWithinIntervals(t *testing.T) {
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	for _, target := range []int64{1000, 10000} {
		patterns := make(map[string]bool)
		for date := 0; date < 4; date++ {
			midnight := day.AddDate(0, 0, date)
			var amounts []int64
			seenRates := make(map[int64]bool)
			for interval := 0; interval < 96; interval++ {
				begin := midnight.Add(time.Duration(interval) * 15 * time.Minute)
				start := publicSidebarDisplayDailyGrowth(begin, target)
				end := target
				if interval < 95 {
					end = publicSidebarDisplayDailyGrowth(begin.Add(15*time.Minute), target)
				}
				half := publicSidebarDisplayDailyGrowth(begin.Add(450*time.Second), target)
				if deviation := 2*half - start - end; deviation < -2 || deviation > 2 {
					t.Fatalf("date %d interval %d is nonlinear: %d/%d/%d", date, interval, start, half, end)
				}
				amounts = append(amounts, end-start)
				seenRates[end-start] = true
			}
			if len(seenRates) < 2 {
				t.Fatalf("target %d date %d has a constant rate", target, date)
			}
			patterns[fmt.Sprint(amounts)] = true
		}
		if len(patterns) < 2 {
			t.Fatalf("target %d reused the same daily speed pattern", target)
		}
	}
}

func sidebarProgressRenderedStats(t *testing.T, app *App, target, layout string) publicSidebarStats {
	t.Helper()
	_, sidebar := publicSidebarLayoutRender(t, app, target, layout)
	lists := publicSidebarLayoutDescendants(sidebar, "dl", "site-stats")
	if len(lists) != 1 {
		t.Fatal("missing rendered site statistics")
	}
	fields := publicSidebarLayoutDescendants(lists[0], "dd", "")
	if len(fields) != 4 {
		t.Fatalf("missing article/today/year/history fields: %d", len(fields))
	}
	values := make([]int64, 3)
	for index := range values {
		value, err := strconv.ParseInt(strings.TrimSpace(publicMetadataText(fields[index+1])), 10, 64)
		if err != nil || value < 0 {
			t.Fatalf("invalid rendered count: %q", publicMetadataText(fields[index+1]))
		}
		values[index] = value
	}
	return publicSidebarStats{TodayViews: values[0], YearViews: values[1], HistoricalViews: values[2]}
}

func TestPublicSidebarDailyTargetSettingsDriveSSRWithoutChangingRawCounts(t *testing.T) {
	app := publicSidebarLayoutApp(t)
	ctx := context.Background()
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: 7})
	sidebarAggregate(t, app, AnalyticsAggregate{Date: analyticsTestDate.AddDate(0, 0, -400).Format("2006-01-02"), Kind: "total", Label: "all", PV: 11})
	sidebarAggregate(t, app, AnalyticsAggregate{Date: analyticsTestDate.AddDate(0, 0, 1).Format("2006-01-02"), Kind: "total", Label: "all", PV: 99})
	midnight := analyticsTestDate.Truncate(24 * time.Hour)
	for _, target := range []int64{1000, 10000, 0} {
		settings, err := app.Store.Settings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		targetCopy := target
		settings.PublicDailyViews = &targetCopy
		if err := app.Store.SaveSettings(ctx, settings); err != nil {
			t.Fatal(err)
		}
		saved, err := app.Store.Settings(ctx)
		if err != nil || saved.PublicDailyViews == nil || *saved.PublicDailyViews != target {
			t.Fatalf("configured daily target did not persist: %+v, %v", saved.PublicDailyViews, err)
		}
		restarted, err := NewApp(app.Store, Config{DataDir: t.TempDir(), MediaDir: app.Config.MediaDir, BaseURL: "https://www.ganwen.nl", Preview: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, stamp := range []time.Time{midnight, midnight.Add(12*time.Hour + 34*time.Minute + 56*time.Second), midnight.Add(24*time.Hour - time.Second)} {
			for _, engine := range []*App{app, restarted} {
				engine.Analytics.now = func() time.Time { return stamp }
				for _, prefix := range []string{"", "/en", "/ar"} {
					for _, layout := range []string{"journal", "classic"} {
						for _, suffix := range []string{"/", "/?p=1447"} {
							got := sidebarProgressRenderedStats(t, engine, prefix+suffix, layout)
							wantToday := 7 + publicSidebarDisplayDailyGrowth(stamp, target)
							if got.TodayViews != wantToday || got.YearViews < 1007 || got.YearViews < got.TodayViews || got.HistoricalViews < got.YearViews {
								t.Fatalf("target %d %s/%s/%s did not drive SSR: %+v, today want %d", target, stamp, layout, prefix+suffix, got, wantToday)
							}
						}
					}
				}
			}
		}
		raw, err := app.Store.publicSidebarAnalytics(ctx, analyticsTestDate)
		if err != nil || raw.stats != (publicSidebarStats{TodayViews: 7, YearViews: 7, HistoricalViews: 18}) {
			t.Fatalf("display setting changed raw windows or included future totals: %+v, %v", raw.stats, err)
		}
		report, err := app.Store.AnalyticsReport(ctx, midnight, midnight)
		if err != nil || report.PV != 7 {
			t.Fatalf("display setting changed the real admin report: %+v, %v", report, err)
		}
	}
}

func TestPublicSidebarRequestUsesOneClockSnapshotAcrossUTCMidnight(t *testing.T) {
	app, _ := analyticsFixture(t)
	before := time.Date(2026, 10, 8, 23, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: 8})
	sidebarAggregate(t, app, AnalyticsAggregate{Date: "2026-10-09", Kind: "total", Label: "all", PV: 3})
	for _, phase := range []struct {
		now, other time.Time
		raw        publicSidebarStats
	}{
		{before, after, publicSidebarStats{TodayViews: 8, YearViews: 8, HistoricalViews: 8}},
		{after, before, publicSidebarStats{TodayViews: 3, YearViews: 11, HistoricalViews: 11}},
	} {
		calls := 0
		app.Analytics.now = func() time.Time {
			calls++
			if calls == 1 {
				return phase.now
			}
			return phase.other
		}
		view := sidebarView(t, app, "zh")
		if calls != 1 {
			t.Fatalf("one sidebar request sampled two clocks across midnight: %d", calls)
		}
		if app.Analytics.sidebarCache.day != phase.now.Format("2006-01-02") || app.Analytics.sidebarCache.stats != phase.raw {
			t.Fatalf("cache used a different clock: %+v, want %+v at %s", app.Analytics.sidebarCache, phase.raw, phase.now)
		}
		if want := publicSidebarDisplayStats(phase.raw, phase.now, sidebarFixtureStartedAt); view.SidebarStats != want {
			t.Fatalf("SSR used a different day than its raw counters: %+v, want %+v", view.SidebarStats, want)
		}
	}
}

func TestPublicSidebarExplicitClockDoesNotReadAmbientClock(t *testing.T) {
	app, _ := analyticsFixture(t)
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: 4})
	app.Analytics.now = func() time.Time {
		t.Fatal("explicit clock accessor read the ambient clock")
		return time.Time{}
	}
	got := app.Analytics.publicSidebarCountsAt(context.Background(), analyticsTestDate)
	if got.stats != (publicSidebarStats{TodayViews: 4, YearViews: 4, HistoricalViews: 4}) {
		t.Fatalf("explicit clock counts: %+v", got.stats)
	}
	calls := 0
	app.Analytics.now = func() time.Time { calls++; return analyticsTestDate }
	legacy := app.Analytics.publicSidebarCounts(context.Background())
	if calls != 1 || legacy.stats != got.stats {
		t.Fatalf("legacy accessor changed its clock/count behavior: %d, %+v", calls, legacy.stats)
	}
}

// Keep a real HTTP response check beside the rendered counter assertions.
func TestPublicSidebarMidnightIsAllowedInThePublicResponse(t *testing.T) {
	app := publicSidebarLayoutApp(t)
	app.Analytics.now = func() time.Time { return analyticsTestDate.Truncate(24 * time.Hour) }
	response := publicLayoutTestRequest(app, "/", "journal")
	if response.Code != http.StatusOK || sidebarProgressRenderedStats(t, app, "/", "journal").TodayViews != 0 {
		t.Fatalf("midnight zero was hidden or replaced: HTTP %d", response.Code)
	}
}
