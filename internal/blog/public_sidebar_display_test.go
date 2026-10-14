package blog

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"
)

var publicSidebarHistoryTestStart = time.Date(2012, 7, 15, 8, 57, 11, 0, time.UTC)

func TestPublicSidebarSyntheticDisplayTimeAndBounds(t *testing.T) {
	midnight := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name                string
		now                 time.Time
		raw                 publicSidebarStats
		target, today, year int64
	}{
		{"midnight starts empty", midnight, publicSidebarStats{}, 1000, 0, 364000},
		{"midnight keeps real visits", midnight, publicSidebarStats{TodayViews: 7, YearViews: 7, HistoricalViews: 7}, 1000, 7, 364007},
		{"same UTC midnight from offset", midnight.In(time.FixedZone("UTC+8", 8*60*60)), publicSidebarStats{}, 1000, 0, 364000},
		{"last second", midnight.Add(24*time.Hour - time.Second), publicSidebarStats{TodayViews: 7, YearViews: 10, HistoricalViews: 10}, 1000, 1006, 365009},
		{"ten thousand last second", midnight.Add(24*time.Hour - time.Second), publicSidebarStats{}, 10000, 9999, 3649999},
		{"zero target keeps real visits", midnight.Add(12 * time.Hour), publicSidebarStats{TodayViews: 7, YearViews: 10, HistoricalViews: 15}, 0, 7, 1017},
		{"negative raw counts", midnight, publicSidebarStats{TodayViews: -5, YearViews: -100, HistoricalViews: -1}, 1000, 0, 364000},
		{"all real totals saturate safely", midnight.Add(12 * time.Hour), publicSidebarStats{TodayViews: math.MaxInt64, YearViews: math.MaxInt64, HistoricalViews: math.MaxInt64}, 1000, math.MaxInt64, math.MaxInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := test.raw
			got := publicSidebarDisplayStats(raw, test.now, publicSidebarHistoryTestStart, test.target)
			if got.TodayViews != test.today || got.YearViews != test.year || raw != test.raw {
				t.Fatalf("display %+v, want today=%d year=%d; raw %+v", got, test.today, test.year, raw)
			}
			if got.TodayViews < 0 || got.YearViews < 1007 || got.YearViews < got.TodayViews || got.HistoricalViews < got.YearViews {
				t.Fatalf("display bounds/order violated %+v", got)
			}
			if test.target == 1000 && publicSidebarDisplayStats(raw, test.now, publicSidebarHistoryTestStart) != got {
				t.Fatal("omitted daily target did not retain the 1000 default")
			}
		})
	}
	for _, test := range []struct{ base, value, want int64 }{
		{0, -1, 0}, {1000, -1, 1000}, {1000, 5, 1005}, {1000, math.MaxInt64, math.MaxInt64}, {730000, math.MaxInt64, math.MaxInt64},
	} {
		if got := publicSidebarDisplayAdd(test.base, test.value); got != test.want {
			t.Fatalf("saturated addition %+v: %d", test, got)
		}
	}
}

func TestPublicSidebarSyntheticHistorySurvivesRollingYearDrops(t *testing.T) {
	before := publicSidebarDisplayStats(publicSidebarStats{TodayViews: 100, YearViews: 500000, HistoricalViews: 2000000}, time.Date(2026, 1, 1, 23, 59, 59, 0, time.UTC), publicSidebarHistoryTestStart)
	after := publicSidebarDisplayStats(publicSidebarStats{YearViews: 500000, HistoricalViews: 2000000}, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), publicSidebarHistoryTestStart)
	if after.HistoricalViews != before.HistoricalViews+1 || after.TodayViews != 0 {
		t.Fatalf("rolling year/midnight changed independent history %+v -> %+v", before, after)
	}
	previous := after.HistoricalViews
	for index, rawYear := range []int64{400000, 0, math.MaxInt64, 0, -1} {
		now := time.Date(2026, 1, 2, 0, 3*(index+1), 0, 0, time.UTC)
		got := publicSidebarDisplayStats(publicSidebarStats{TodayViews: math.MaxInt64, YearViews: rawYear, HistoricalViews: 2000000}, now, publicSidebarHistoryTestStart)
		stable := publicSidebarDisplayStats(publicSidebarStats{HistoricalViews: 2000000}, now, publicSidebarHistoryTestStart)
		if got.HistoricalViews != stable.HistoricalViews || got.HistoricalViews < previous || got.TodayViews < 0 || got.TodayViews > got.YearViews || got.YearViews > got.HistoricalViews {
			t.Fatalf("rolling/drop/overflow year %d changed independent history/order %+v", rawYear, got)
		}
		previous = got.HistoricalViews
	}
}

func TestPublicSidebarHistoryUsesSiteAgeAndElapsedCalendarDays(t *testing.T) {
	now := time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC)
	if got := publicSidebarDisplayHistory(publicSidebarHistoryTestStart, now); got < 5200000 || got > 5300000 {
		t.Fatalf("2012 blog history should reflect fourteen years, got %d", got)
	}
	for _, target := range []int64{0, 1000, 10000} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			for _, test := range []struct {
				name  string
				start time.Time
				days  int
			}{
				{"ordinary year", time.Date(2022, 7, 15, 0, 0, 0, 0, time.UTC), 365},
				{"year with leap day", time.Date(2023, 7, 15, 0, 0, 0, 0, time.UTC), 366},
			} {
				before := publicSidebarDisplayHistory(publicSidebarHistoryTestStart, test.start, target)
				after := publicSidebarDisplayHistory(publicSidebarHistoryTestStart, test.start.AddDate(1, 0, 0), target)
				if after-before != int64(test.days)*target {
					t.Fatalf("%s must count %d complete UTC days: %d -> %d", test.name, test.days, before, after)
				}
			}
			midnight := now.Truncate(24 * time.Hour)
			first := publicSidebarDisplayHistory(publicSidebarHistoryTestStart, midnight, target)
			if got := publicSidebarDisplayHistory(publicSidebarHistoryTestStart, midnight.Add(24*time.Hour), target); got != first+target {
				t.Fatalf("complete UTC day must add the configured goal: %d -> %d", first, got)
			}
			first = publicSidebarDisplayHistory(publicSidebarHistoryTestStart, now, target)
			if got := publicSidebarDisplayHistory(publicSidebarHistoryTestStart, now.In(time.FixedZone("UTC-7", -7*60*60)), target); got != first {
				t.Fatalf("viewer timezone changed site age: %d -> %d", first, got)
			}
			for _, start := range []time.Time{{}, now.Add(time.Minute)} {
				if got := publicSidebarDisplayHistory(start, now, target); got != max(int64(1007), target) {
					t.Fatalf("unknown/future start floor: %d", got)
				}
				got := publicSidebarDisplayStats(publicSidebarStats{}, midnight, start, target)
				if got.TodayViews != 0 || got.YearViews < 1007 || got.HistoricalViews < got.YearViews {
					t.Fatalf("unknown/future start broke midnight/order: %+v", got)
				}
			}
			for second := 0; second < 600; second++ {
				got := publicSidebarDisplayHistory(publicSidebarHistoryTestStart, now.Add(time.Duration(second)*time.Second), target)
				if got < first || got-first > 1 {
					t.Fatalf("second growth was discontinuous: %d -> %d", first, got)
				}
				first = got
			}
		})
	}
}

func TestPublicSidebarSyntheticPopularCapsWithoutChangingRealOrder(t *testing.T) {
	app, _ := analyticsFixture(t)
	post := Post{ID: 47, Type: "post", Status: "publish", Title: "Second popular", ContentHTML: "<p>Body</p>"}
	sidebarSave(t, app, post)
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "total", Label: "all", PV: math.MaxInt64})
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: "/?p=42", PostID: 42, PV: math.MaxInt64})
	sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: "/?p=47", PostID: 47, PV: 500000})
	view := sidebarView(t, app, "zh")
	if len(view.SidebarPopular) != 2 || view.SidebarPopular[0].Title != "公开文章" || view.SidebarPopular[1].Title != post.Title {
		t.Fatalf("display cap changed real-PV order %+v", view.SidebarPopular)
	}
	if view.SidebarPopular[0].Views != view.SidebarStats.YearViews || view.SidebarPopular[1].Views < 503600 || view.SidebarPopular[1].Views > 504249 || view.SidebarStats.YearViews != view.SidebarStats.HistoricalViews {
		t.Fatalf("popular/year/history cap %+v %+v", view.SidebarPopular, view.SidebarStats)
	}
	counts := app.Analytics.publicSidebarCounts(context.Background())
	if counts.stats.TodayViews != math.MaxInt64 || counts.stats.YearViews != math.MaxInt64 || counts.stats.HistoricalViews != math.MaxInt64 || counts.popular[0].views != math.MaxInt64 || counts.popular[1].views != 500000 {
		t.Fatalf("display cap changed raw cache %+v", counts)
	}
}

func TestPublicSidebarSyntheticDisplayGrowthAndMidnight(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	for _, target := range []int64{0, 1000, 10000} {
		raw := publicSidebarStats{TodayViews: 10, YearViews: 20, HistoricalViews: 30}
		first := publicSidebarDisplayStats(raw, now, publicSidebarHistoryTestStart, target)
		later := publicSidebarDisplayStats(raw, now.Add(3*time.Minute), publicSidebarHistoryTestStart, target)
		if later.TodayViews < first.TodayViews || later.YearViews < first.YearViews || later.HistoricalViews < first.HistoricalViews {
			t.Fatalf("clock growth went backwards for target %d: %+v -> %+v", target, first, later)
		}
		increased := publicSidebarDisplayStats(publicSidebarStats{TodayViews: 15, YearViews: 25, HistoricalViews: 35}, now, publicSidebarHistoryTestStart, target)
		if increased.TodayViews != first.TodayViews+5 || increased.YearViews != first.YearViews+5 || increased.HistoricalViews != first.HistoricalViews+5 {
			t.Fatalf("real visits must add to all periods for target %d: %+v -> %+v", target, first, increased)
		}
		before := publicSidebarDisplayStats(publicSidebarStats{TodayViews: 100, YearViews: 500, HistoricalViews: 1000}, time.Date(2026, 1, 10, 23, 59, 59, 0, time.UTC), publicSidebarHistoryTestStart, target)
		after := publicSidebarDisplayStats(publicSidebarStats{TodayViews: 3, YearViews: 503, HistoricalViews: 1003}, time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC), publicSidebarHistoryTestStart, target)
		wantBefore := int64(100)
		if target > 0 {
			wantBefore += target - 1
		}
		if before.TodayViews != wantBefore || after.TodayViews != 3 || after.HistoricalViews < before.HistoricalViews || after.HistoricalViews < after.YearViews || after.YearViews < after.TodayViews {
			t.Fatalf("UTC midnight reset/history order for target %d: %+v -> %+v", target, before, after)
		}
	}
	if inconsistent := publicSidebarDisplayStats(publicSidebarStats{TodayViews: 900000, YearViews: 1}, now, publicSidebarHistoryTestStart); inconsistent.YearViews != inconsistent.TodayViews || inconsistent.HistoricalViews < inconsistent.YearViews {
		t.Fatalf("history/year did not fence the larger display %+v", inconsistent)
	}
}

func TestPublicSidebarSyntheticDisplayDoesNotPolluteRealAnalytics(t *testing.T) {
	app, _ := analyticsFixture(t)
	for _, index := range []int{8001, 8002} {
		if w := analyticsEventRequest(app, analyticsPayloadFor(index, "/?p=42"), nil); w.Code != 204 {
			t.Fatalf("real event failed: %d", w.Code)
		}
	}
	report := analyticsGetReport(t, app)
	before, err := app.Store.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := app.Analytics.publicSidebarCounts(context.Background())
	view := sidebarView(t, app, "zh")
	if raw.stats != (publicSidebarStats{TodayViews: 2, YearViews: 2, HistoricalViews: 2}) || len(raw.popular) != 1 || raw.popular[0].views != 2 || report.PV != 2 {
		t.Fatalf("initial real counters %+v %+v %+v", raw.stats, raw.popular, report)
	}
	if view.SidebarStats.TodayViews <= 2 || view.SidebarStats.TodayViews >= 1002 || view.SidebarStats.YearViews != view.SidebarStats.HistoricalViews || view.SidebarStats.YearViews < 280000 || view.SidebarStats.YearViews > 285000 || len(view.SidebarPopular) != 1 || view.SidebarPopular[0].Views != 5215 {
		t.Fatalf("public display was not transformed %+v %+v", view.SidebarStats, view.SidebarPopular)
	}
	if current := app.Analytics.publicSidebarCounts(context.Background()); !reflect.DeepEqual(current, raw) {
		t.Fatalf("public rendering changed cached true counters %+v -> %+v", raw, current)
	}
	after, err := app.Store.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before.CreatedAt, after.CreatedAt = "", ""
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(report, analyticsGetReport(t, app)) {
		t.Fatal("synthetic public display changed the database or real admin report")
	}
}

func TestPublicSidebarPopularDisplaySpreadAndStableGrowth(t *testing.T) {
	ranges := [...][2]int64{{4500, 5599}, {3600, 4249}, {2900, 3449}, {2400, 2749}, {2000, 2299}, {1600, 1849}, {1300, 1499}, {1000, 1199}}
	for rank, bounds := range ranges {
		seen := make(map[int64]bool)
		for postID := int64(1); postID <= 200; postID++ {
			got := publicSidebarDisplayPopular(0, postID, rank, 365000)
			if got < bounds[0] || got > bounds[1] {
				t.Fatalf("rank %d post %d outside display band: %d", rank, postID, got)
			}
			if repeated := publicSidebarDisplayPopular(0, postID, rank, 365000); repeated != got {
				t.Fatalf("refresh changed article offset: %d -> %d", got, repeated)
			}
			if grown := publicSidebarDisplayPopular(7, postID, rank, 365000); grown != got+7 {
				t.Fatalf("real visits did not grow display consistently: %d -> %d", got, grown)
			}
			if negative := publicSidebarDisplayPopular(-7, postID, rank, 365000); negative != got {
				t.Fatalf("negative raw views changed floor: %d -> %d", got, negative)
			}
			seen[got] = true
		}
		if len(seen) < 50 {
			t.Fatalf("rank %d lacks article-specific variation: %d values", rank, len(seen))
		}
		if capped := publicSidebarDisplayPopular(math.MaxInt64, 42, rank, 365123); capped != 365123 {
			t.Fatalf("overflow escaped display cap: %d", capped)
		}
	}
}

func TestPublicSidebarPopularDisplayTiesStayDescendingWhenCapped(t *testing.T) {
	for _, test := range []struct{ name, start string }{
		{"existing blog", "2026-01-01"},
		{"new blog", analyticsTestDate.Format(time.RFC3339)},
		{"unknown date", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, _ := analyticsFixture(t)
			post, err := app.Store.GetPost(context.Background(), 42)
			if err != nil {
				t.Fatal(err)
			}
			post.PublishedAt = test.start
			sidebarSave(t, app, post)
			for id := int64(100); id < 108; id++ {
				sidebarSave(t, app, Post{ID: id, Type: "post", Status: "publish", Title: "Tied popular", ContentHTML: "<p>Body</p>"})
				sidebarAggregate(t, app, AnalyticsAggregate{Kind: "page", Label: fmt.Sprintf("/?p=%d", id), PostID: id, PV: math.MaxInt64})
			}
			first := sidebarView(t, app, "zh")
			if len(first.SidebarPopular) != 8 {
				t.Fatalf("missing tied articles: %+v", first.SidebarPopular)
			}
			for rank, item := range first.SidebarPopular {
				if item.Views != first.SidebarStats.YearViews-int64(rank) || item.Views < 1000 {
					t.Fatalf("rank %d cap failed to keep strict descending display: %+v", rank, first.SidebarPopular)
				}
			}
			if refreshed := sidebarView(t, app, "zh"); !reflect.DeepEqual(refreshed.SidebarPopular, first.SidebarPopular) {
				t.Fatalf("refresh rerolled displayed counts: %+v -> %+v", first.SidebarPopular, refreshed.SidebarPopular)
			}
			if counts := app.Analytics.publicSidebarCounts(context.Background()); counts.popular[0].views != math.MaxInt64 || counts.popular[7].views != math.MaxInt64 {
				t.Fatalf("display cap altered raw ties: %+v", counts.popular)
			}
		})
	}
}
