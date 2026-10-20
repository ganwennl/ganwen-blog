package blog

import (
	"context"
	"errors"
	"html"
	"log"
	"sort"
	"time"
)

type publicPopular struct {
	Title, URL string
	Views      int64
}

type publicSidebarStats struct {
	TodayViews, YearViews int64
	HistoricalViews       int64
}

type publicPopularCount struct {
	postID int64
	views  int64
}

// Cache counts only: publication state, translations and URLs stay current.
type publicSidebarCache struct {
	day     string
	expires time.Time
	stats   publicSidebarStats
	popular []publicPopularCount
}

func (s *Store) publicSidebarAnalytics(ctx context.Context, now time.Time) (publicSidebarCache, error) {
	day := now.UTC().Truncate(24 * time.Hour)
	date := day.Format("2006-01-02")
	var counts publicSidebarCache
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN date=? THEN pv ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN date>=? THEN pv ELSE 0 END),0),COALESCE(SUM(pv),0)
		FROM analytics_aggregates WHERE kind='total' AND label='all' AND date<=?`,
		date, day.AddDate(0, 0, -364).Format("2006-01-02"), date).Scan(&counts.stats.TodayViews, &counts.stats.YearViews, &counts.stats.HistoricalViews)
	if err != nil {
		return publicSidebarCache{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT post_id,SUM(pv) FROM analytics_aggregates
		WHERE kind='page' AND post_id>0 AND date>=? AND date<=? GROUP BY post_id`,
		day.AddDate(0, 0, -29).Format("2006-01-02"), date)
	if err != nil {
		return publicSidebarCache{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var count publicPopularCount
		if err := rows.Scan(&count.postID, &count.views); err != nil {
			return publicSidebarCache{}, err
		}
		if count.views > 0 {
			counts.popular = append(counts.popular, count)
		}
	}
	if err := rows.Err(); err != nil {
		return publicSidebarCache{}, err
	}
	sort.Slice(counts.popular, func(i, j int) bool {
		if counts.popular[i].views == counts.popular[j].views {
			return counts.popular[i].postID < counts.popular[j].postID
		}
		return counts.popular[i].views > counts.popular[j].views
	})
	return counts, nil
}

func (a *Analytics) publicSidebarCounts(ctx context.Context) publicSidebarCache {
	return a.publicSidebarCountsAt(ctx, a.now())
}

func (a *Analytics) publicSidebarCountsAt(ctx context.Context, now time.Time) publicSidebarCache {
	now = now.UTC()
	day := now.Format("2006-01-02")
	a.sidebarMu.Lock()
	defer a.sidebarMu.Unlock()
	if a.sidebarCache.day == day && now.Before(a.sidebarCache.expires) {
		return a.sidebarCache
	}
	counts, err := a.app.Store.publicSidebarAnalytics(ctx, now)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return publicSidebarCache{}
		}
		log.Printf("public sidebar analytics unavailable: %v", err)
		// Cache failures briefly too, avoiding repeated queries/logs per page.
		counts = publicSidebarCache{}
	}
	counts.day, counts.expires = day, now.Add(time.Minute)
	a.sidebarCache = counts
	return counts
}

func (a *App) populatePublicSidebar(ctx context.Context, view *publicView, settings SiteSettings, posts []Post) {
	view.SidebarPopular = nil
	view.SidebarStats = publicSidebarStats{}
	view.SidebarModules = make(map[string]bool)
	for _, module := range publicSidebarModules(settings) {
		if module == "tags" && len(view.FilterTags) == 0 {
			continue
		}
		view.SidebarModules[module] = true
	}
	view.ShowPublicStats = view.SidebarModules["stats"] && publicStatsEnabled(settings)
	if a.Analytics == nil || !view.SidebarModules["popular"] && !view.SidebarModules["stats"] {
		return
	}
	now := a.Analytics.now()
	counts := a.Analytics.publicSidebarCountsAt(ctx, now)
	virtualViews := publicVirtualViewsEnabled(settings)
	view.SidebarStats = counts.stats
	if virtualViews {
		view.SidebarStats = publicSidebarDisplayStats(counts.stats, now, publicSidebarStartDate(posts, now), publicDailyDisplayTarget(settings))
	}
	visible := make(map[int64]Post, len(posts))
	for _, post := range posts {
		if post.Type == "post" && post.Status == "publish" {
			visible[post.ID] = post
		}
	}
	for _, count := range counts.popular {
		post, ok := visible[count.postID]
		if !ok {
			continue
		}
		localized, ok := localizedPost(post, view.Lang)
		if !ok {
			continue
		}
		views := count.views
		if virtualViews {
			rank, ceiling := len(view.SidebarPopular), view.SidebarStats.YearViews
			if rank > 0 {
				ceiling = min(ceiling, view.SidebarPopular[rank-1].Views-1)
			}
			views = publicSidebarDisplayPopular(count.views, post.ID, rank, ceiling)
		}
		view.SidebarPopular = append(view.SidebarPopular, publicPopular{
			Title: html.UnescapeString(localized.Title), URL: publicPostURL(post, settings, view.Lang),
			Views: views,
		})
		if len(view.SidebarPopular) == publicSidebarPopularLimit {
			break
		}
	}
}
