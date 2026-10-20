package blog

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func (s *Store) AnalyticsReport(ctx context.Context, from, to time.Time) (AnalyticsReport, error) {
	out := AnalyticsReport{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Timezone: "UTC", Trend: []AnalyticsTrend{}, TopPages: []AnalyticsPage{}, Sources: []AnalyticsDistribution{}, Countries: []AnalyticsDistribution{}, Languages: []AnalyticsDistribution{}, Devices: []AnalyticsDistribution{}, Browsers: []AnalyticsDistribution{}, OS: []AnalyticsDistribution{}, Regions: []AnalyticsDistribution{}, Networks: []AnalyticsDistribution{}}
	rows, err := s.db.QueryContext(ctx, "SELECT date,kind,label,post_id,pv,uv FROM analytics_aggregates WHERE date>=? AND date<=? ORDER BY date", out.From, out.To)
	if err != nil {
		return out, err
	}
	byDay := map[string]AnalyticsTrend{}
	pages := map[string]AnalyticsPage{}
	dimensions := map[string]map[string]int64{}
	for rows.Next() {
		var row AnalyticsAggregate
		if err = rows.Scan(&row.Date, &row.Kind, &row.Label, &row.PostID, &row.PV, &row.UV); err != nil {
			break
		}
		switch row.Kind {
		case "total":
			out.PV += row.PV
			out.UVDailySum += row.UV
			byDay[row.Date] = AnalyticsTrend{row.Date, row.PV, row.UV}
		case "page":
			p := pages[row.Label]
			p.Path, p.PostID = row.Label, row.PostID
			p.PV += row.PV
			pages[row.Label] = p
		default:
			if dimensions[row.Kind] == nil {
				dimensions[row.Kind] = map[string]int64{}
			}
			dimensions[row.Kind][row.Label] += row.PV
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		trend := byDay[date]
		trend.Date = date
		out.Trend = append(out.Trend, trend)
	}
	for _, p := range pages {
		out.TopPages = append(out.TopPages, p)
	}
	sort.Slice(out.TopPages, func(i, j int) bool {
		if out.TopPages[i].PV == out.TopPages[j].PV {
			return out.TopPages[i].Path < out.TopPages[j].Path
		}
		return out.TopPages[i].PV > out.TopPages[j].PV
	})
	out.TopPages = out.TopPages[:min(len(out.TopPages), 50)]
	for i := range out.TopPages {
		p := &out.TopPages[i]
		p.Title = "文章列表"
		if p.Path == "other" {
			p.Title = "其他页面"
		} else if p.PostID > 0 {
			post, err := s.GetPost(ctx, p.PostID)
			if err == nil && post.Status == "publish" {
				p.Title = post.Title
			} else {
				p.Title = "已下线页面"
			}
		} else if strings.Contains(p.Path, "s=") {
			p.Title = "搜索"
		}
	}
	out.Sources = sortAnalyticsDistribution(dimensions["source"])
	out.Countries = sortAnalyticsDistribution(dimensions["country"])
	out.Languages = sortAnalyticsDistribution(dimensions["language"])
	out.Devices = sortAnalyticsDistribution(dimensions["device"])
	out.Browsers = sortAnalyticsDistribution(dimensions["browser"])
	out.OS = sortAnalyticsDistribution(dimensions["os"])
	out.Regions = sortAnalyticsDistribution(dimensions["region"])
	out.Networks = sortAnalyticsDistribution(dimensions["network"])
	return out, nil
}

func snapshotAnalytics(ctx context.Context, tx *sql.Tx) (*AnalyticsBackup, error) {
	out := &AnalyticsBackup{Aggregates: []AnalyticsAggregate{}, Visitors: []AnalyticsVisitor{}, Events: []AnalyticsEvent{}}
	rows, err := tx.QueryContext(ctx, "SELECT date,kind,label,post_id,pv,uv FROM analytics_aggregates ORDER BY date,kind,label")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var row AnalyticsAggregate
		if err = rows.Scan(&row.Date, &row.Kind, &row.Label, &row.PostID, &row.PV, &row.UV); err != nil {
			break
		}
		out.Aggregates = append(out.Aggregates, row)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, table := range []string{"analytics_visitors", "analytics_events"} {
		rows, err = tx.QueryContext(ctx, "SELECT date,id_hash FROM "+table+" ORDER BY date,id_hash")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var date, hash string
			if err = rows.Scan(&date, &hash); err != nil {
				break
			}
			if table == "analytics_visitors" {
				out.Visitors = append(out.Visitors, AnalyticsVisitor{date, hash})
			} else {
				out.Events = append(out.Events, AnalyticsEvent{date, hash})
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func restoreAnalytics(ctx context.Context, tx *sql.Tx, b *AnalyticsBackup) error {
	if b == nil {
		return nil
	}
	for _, row := range b.Aggregates {
		if _, err := tx.ExecContext(ctx, "INSERT INTO analytics_aggregates(id_hash,date,kind,label,post_id,pv,uv) VALUES(?,?,?,?,?,?,?)", analyticsAggregateID(row), row.Date, row.Kind, row.Label, row.PostID, row.PV, row.UV); err != nil {
			return err
		}
	}
	for _, row := range b.Visitors {
		if _, err := tx.ExecContext(ctx, "INSERT INTO analytics_visitors(id_hash,date) VALUES(?,?)", row.Hash, row.Date); err != nil {
			return err
		}
	}
	for _, row := range b.Events {
		if _, err := tx.ExecContext(ctx, "INSERT INTO analytics_events(id_hash,date) VALUES(?,?)", row.Hash, row.Date); err != nil {
			return err
		}
	}
	return nil
}
func validAnalyticsDate(date string) bool {
	_, err := time.Parse(time.DateOnly, date)
	return err == nil
}
func validAnalyticsHash(hash string) bool {
	value, err := hex.DecodeString(hash)
	return err == nil && len(value) == 32 && hash == strings.ToLower(hash)
}
func validateAnalyticsBackup(b DatabaseBackup) error {
	invalid := errors.New("invalid analytics backup")
	secretPresent := false
	for _, value := range b.Values {
		if value.Key == analyticsSecretKey {
			if !validAnalyticsHash(value.Value) {
				return invalid
			}
			secretPresent = true
		}
	}
	if b.Analytics == nil {
		return nil
	}
	if (len(b.Analytics.Visitors) > 0 || len(b.Analytics.Events) > 0) && !secretPresent {
		return invalid
	}
	seen := map[string]bool{}
	for _, row := range b.Analytics.Aggregates {
		id := analyticsAggregateID(row)
		labelValid := len(row.Label) <= 512
		if row.Kind == "region" || row.Kind == "network" {
			labelValid = validAnalyticsGeoLabel(row.Label)
		}
		if !validAnalyticsDate(row.Date) || row.PV < 0 || row.PV > analyticsDailyLimit || row.UV < 0 || row.UV > row.PV || !labelValid || seen[id] || row.PostID < 0 {
			return invalid
		}
		seen[id] = true
		if row.Kind != "total" && row.UV != 0 {
			return invalid
		}
		switch row.Kind {
		case "total":
			if row.Label != "all" || row.PostID != 0 {
				return invalid
			}
		case "page":
			if row.Label == "other" && row.PostID == 0 {
				continue
			}
			u, err := url.Parse(row.Label)
			if err != nil || u.Host != "" || u.Scheme != "" || u.User != nil || u.Fragment != "" {
				return invalid
			}
			_, route, ok := publicLanguagePath(u.Path)
			if !ok || (route != "/" && route != "/index.php") {
				return invalid
			}
			q, err := url.ParseQuery(u.RawQuery)
			if err != nil {
				return invalid
			}
			for key, values := range q {
				if len(values) != 1 {
					return invalid
				}
				switch key {
				case "p", "page_id", "cat", "category_name", "tag", "author", "m", "post_format", "paged":
				case "s":
					if values[0] != "" {
						return invalid
					}
				default:
					return invalid
				}
			}
		case "source":
			if row.Label != "direct" && row.Label != "internal" && row.Label != "unknown" && row.Label != "other" && analyticsSource("https://"+row.Label, "none.invalid") != row.Label {
				return invalid
			}
		case "country":
			if row.Label != "unknown" && row.Label != "other" && (len(row.Label) != 2 || row.Label[0] < 'A' || row.Label[0] > 'Z' || row.Label[1] < 'A' || row.Label[1] > 'Z') {
				return invalid
			}
		case "region", "network":
			if !validAnalyticsGeoLabel(row.Label) {
				return invalid
			}
		case "language":
			if row.Label != "zh" && row.Label != "other" && !isTranslationLanguage(row.Label) {
				return invalid
			}
		case "device":
			if row.Label != "desktop" && row.Label != "mobile" && row.Label != "tablet" && row.Label != "other" {
				return invalid
			}
		case "browser":
			if row.Label != "Chrome" && row.Label != "Safari" && row.Label != "Firefox" && row.Label != "Edge" && row.Label != "Opera" && row.Label != "other" {
				return invalid
			}
		case "os":
			if row.Label != "Windows" && row.Label != "macOS" && row.Label != "iOS" && row.Label != "Android" && row.Label != "Linux" && row.Label != "other" {
				return invalid
			}
		default:
			return invalid
		}
		if row.Kind != "page" && row.PostID != 0 {
			return invalid
		}
	}
	for _, table := range []string{"visitors", "events"} {
		seen = map[string]bool{}
		if table == "visitors" {
			for _, row := range b.Analytics.Visitors {
				if !validAnalyticsDate(row.Date) || !validAnalyticsHash(row.Hash) || seen[row.Hash] {
					return invalid
				}
				seen[row.Hash] = true
			}
		} else {
			for _, row := range b.Analytics.Events {
				if !validAnalyticsDate(row.Date) || !validAnalyticsHash(row.Hash) || seen[row.Hash] {
					return invalid
				}
				seen[row.Hash] = true
			}
		}
	}
	return nil
}

func validAnalyticsGeoLabel(label string) bool {
	if label == "" || !utf8.ValidString(label) || len(label) > 2048 || utf8.RuneCountInString(label) > 512 {
		return false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func analyticsGeoLabel(label string) string {
	label = strings.TrimSpace(label)
	if !validAnalyticsGeoLabel(label) || strings.EqualFold(label, "CZ88.NET") {
		return "unknown"
	}
	return label
}
