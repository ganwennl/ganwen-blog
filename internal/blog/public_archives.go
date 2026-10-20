package blog

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type publicArchiveYear struct {
	Label, URL, Year string
	Count            int
	Current, Open    bool
	Months           []publicLink
}

func parsePublicArchiveDate(value string) (time.Time, bool) {
	layout := "2006"
	if len(value) == 6 {
		layout = "200601"
	} else if len(value) != 4 {
		return time.Time{}, false
	}
	date, err := time.Parse(layout, value)
	return date, err == nil && date.Year() > 0 && date.Format(layout) == value
}

func publicArchiveDateLabel(value, lang string) string {
	date, ok := parsePublicArchiveDate(value)
	if !ok {
		return ""
	}
	if lang == "zh" {
		if len(value) == 4 {
			return fmt.Sprintf("%d年", date.Year())
		}
		return fmt.Sprintf("%d年%d月", date.Year(), date.Month())
	}
	if len(value) == 4 {
		return date.Format("2006")
	}
	return date.Format("2006-01")
}

func publicPostInArchive(post Post, selected string) bool {
	date, err := publicTime(strings.TrimSpace(post.PublishedAt))
	if err != nil {
		return false
	}
	layout := "200601"
	if len(selected) == 4 {
		layout = "2006"
	}
	return date.Format(layout) == selected
}

func populatePublicArchives(view *publicView, settings SiteSettings, posts []Post, query url.Values, lang string) {
	view.ArchiveYears = nil
	view.ArchiveAllURL = publicFilterResetURL(settings, lang, query, "m")
	selected := query.Get("m")
	view.ArchiveAllCurrent = selected == ""
	counts := map[string]int{}
	for _, post := range posts {
		if post.Type != "post" || post.Status != "publish" || post.Unlisted {
			continue
		}
		if _, ok := localizedPost(post, lang); !ok {
			continue
		}
		date, err := publicTime(strings.TrimSpace(post.PublishedAt))
		if err == nil {
			// Keep the publication's original timezone, matching date filtering.
			counts[date.Format("200601")]++
		}
	}
	months := make([]string, 0, len(counts))
	for month := range counts {
		months = append(months, month)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(months)))
	archiveURL := func(value string) string {
		q := publicArchiveQuery(query)
		q.Del("paged")
		q.Set("m", value)
		return publicArchiveURL(settings, lang, q)
	}
	for _, month := range months {
		year := month[:4]
		if len(view.ArchiveYears) == 0 || view.ArchiveYears[len(view.ArchiveYears)-1].Year != year {
			open := selected == "" && len(view.ArchiveYears) == 0 || strings.HasPrefix(selected, year)
			view.ArchiveYears = append(view.ArchiveYears, publicArchiveYear{Label: publicArchiveDateLabel(year, lang), URL: archiveURL(year), Year: year, Current: selected == year, Open: open})
		}
		group := &view.ArchiveYears[len(view.ArchiveYears)-1]
		group.Count += counts[month]
		group.Months = append(group.Months, publicLink{Label: publicArchiveDateLabel(month, lang), URL: archiveURL(month), Current: selected == month, Count: counts[month]})
	}
}
