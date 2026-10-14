package blog

import (
	"math"
	"strings"
	"time"
)

const publicSidebarPopularLimit = 8
const publicSidebarMinimumHistory int64 = 1000 + publicSidebarPopularLimit - 1

func publicSidebarDisplayAdd(base, value int64) int64 {
	value = max(0, value)
	if base > math.MaxInt64-value {
		return math.MaxInt64
	}
	return base + value
}

// Separate rank bands preserve the real popularity order, while a stable
// article-specific offset avoids identical/rounded presentation counts.
// The offset is pure: refreshes and process restarts do not reroll it.
func publicSidebarDisplayPopular(raw, postID int64, rank int, ceiling int64) int64 {
	bands := [...]struct{ base, spread int64 }{
		{4500, 1100}, {3600, 650}, {2900, 550}, {2400, 350},
		{2000, 300}, {1600, 250}, {1300, 200}, {1000, 200},
	}
	band := bands[max(0, min(rank, len(bands)-1))]
	base := band.base + int64(publicSidebarDisplaySeed(postID)%uint64(band.spread))
	return min(max(int64(1000), ceiling), publicSidebarDisplayAdd(base, raw))
}

func publicSidebarDisplaySeed(value int64) uint64 {
	seed := uint64(value) + 0x9e3779b97f4a7c15
	seed = (seed ^ (seed >> 30)) * 0xbf58476d1ce4e5b9
	seed = (seed ^ (seed >> 27)) * 0x94d049bb133111eb
	seed ^= seed >> 31
	return seed
}

// Use original public articles across all languages, independently of the
// current archive/search page, and ignore invalid or future publication dates.
func publicSidebarStartDate(posts []Post, now time.Time) time.Time {
	var earliest time.Time
	for _, post := range posts {
		if post.Type != "post" || post.Status != "publish" {
			continue
		}
		published, err := publicTime(strings.TrimSpace(post.PublishedAt))
		if err != nil || published.Year() <= 1 || published.After(now) {
			continue
		}
		if earliest.IsZero() || published.Before(earliest) {
			earliest = published.UTC()
		}
	}
	return earliest
}

func publicSidebarDisplayTarget(targets []int64) int64 {
	if len(targets) == 0 {
		return publicDailyViewsDefault
	}
	return max(0, min(targets[0], publicDailyViewsMax))
}

// The same UTC day has a stable pace, including after a refresh or restart.
// Random positive interval weights vary the rate while their cumulative sum
// distributes exactly one daily target over 24 hours without counting backwards.
func publicSidebarDisplayDailyGrowth(now time.Time, target int64) int64 {
	target = max(0, min(target, publicDailyViewsMax))
	utc := now.UTC()
	day := utc.Truncate(24 * time.Hour)
	elapsed := utc.Unix() - day.Unix()
	const intervalSeconds int64 = 15 * 60
	completed, partial := elapsed/intervalSeconds, elapsed%intervalSeconds
	var total, accumulated int64
	for interval := int64(0); interval < 96; interval++ {
		weight := int64(60 + publicSidebarDisplaySeed(day.Unix()+interval)%81)
		total += weight * intervalSeconds
		if interval < completed {
			accumulated += weight * intervalSeconds
		} else if interval == completed {
			accumulated += weight * partial
		}
	}
	// Both factors are bounded: one million visits and 96 small time weights.
	return target * accumulated / total
}

func publicSidebarDisplayHistory(startedAt, now time.Time, targets ...int64) int64 {
	target := publicSidebarDisplayTarget(targets)
	minimum := max(publicSidebarMinimumHistory, target)
	if startedAt.IsZero() || startedAt.After(now) {
		return minimum
	}
	startDay := startedAt.UTC().Truncate(24 * time.Hour)
	endDay := now.UTC().Truncate(24 * time.Hour)
	days := (endDay.Unix() - startDay.Unix()) / 86400
	if target > 0 && days > math.MaxInt64/target {
		return math.MaxInt64
	}
	base := 1000 + int64(publicSidebarDisplaySeed(startedAt.Unix())%1000)
	fullDays := days * target
	startProgress := publicSidebarDisplayDailyGrowth(startedAt, target)
	endProgress := publicSidebarDisplayDailyGrowth(now, target)
	// This subtraction is nonnegative when now is on a later UTC day. On the
	// starting day, monotonic progress makes endProgress >= startProgress.
	if fullDays >= startProgress {
		return max(minimum, publicSidebarDisplayAdd(publicSidebarDisplayAdd(base, fullDays-startProgress), endProgress))
	}
	return max(minimum, publicSidebarDisplayAdd(base, endProgress-startProgress))
}

// Synthetic counts were explicitly requested for the public presentation.
// Never persist them or pass them to the real analytics cache/admin report.
func publicSidebarDisplayStats(raw publicSidebarStats, now, startedAt time.Time, targets ...int64) publicSidebarStats {
	utc := now.UTC()
	target := publicSidebarDisplayTarget(targets)
	growth := publicSidebarDisplayDailyGrowth(utc, target)
	history := publicSidebarDisplayAdd(publicSidebarDisplayHistory(startedAt, utc, target), raw.HistoricalViews)
	today := min(history, publicSidebarDisplayAdd(growth, raw.TodayViews))
	// The existing analytics window includes 364 completed UTC days and today.
	yearBase := max(publicSidebarMinimumHistory, 364*target+growth)
	year := min(history, max(today, publicSidebarDisplayAdd(yearBase, raw.YearViews)))
	return publicSidebarStats{TodayViews: today, YearViews: year, HistoricalViews: history}
}
