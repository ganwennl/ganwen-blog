package blog

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func archiveTestPost(id int64, date string) Post {
	p := Post{ID: id, Type: "post", Status: "publish", Title: fmt.Sprintf("ARCHIVE_ARTICLE_%d", id), ContentHTML: "<p>Archive content</p>", PublishedAt: date, AuthorID: 1}
	p.Translations = map[string]Translation{}
	for _, language := range translationLanguages() {
		p.Translations[language] = Translation{Title: p.Title + " " + language, ContentHTML: "<p>Translated archive content</p>", Status: "publish", SourceHash: SourceHash(p)}
	}
	return p
}

func archiveURLQuery(t *testing.T, raw, language string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Path != publicLanguagePrefix(language)+"/" {
		t.Fatalf("invalid %s archive URL %q: %v", language, raw, err)
	}
	return u.Query()
}

func TestPublicArchivesCountVisibleArticlesInOriginalCalendar(t *testing.T) {
	posts := []Post{
		archiveTestPost(1, "2026-10-20T12:00:00+08:00"),
		archiveTestPost(2, "2026-01-01T00:30:00+14:00"),
		archiveTestPost(3, "2026-01-15"),
		archiveTestPost(4, "2025-12-31T23:30:00-12:00"),
		archiveTestPost(5, "2025-12-20"),
	}
	stale := posts[4].Translations["en"]
	stale.SourceHash = "outdated"
	posts[4].Translations["en"] = stale
	for i, variant := range []string{"missing", "review", "draft", "empty title", "empty content"} {
		p := archiveTestPost(int64(10+i), "2024-02-01")
		translation := p.Translations["en"]
		switch variant {
		case "missing":
			delete(p.Translations, "en")
		case "review", "draft":
			translation.Status = variant
		case "empty title":
			translation.Title = " "
		case "empty content":
			translation.ContentHTML = " "
		}
		if variant != "missing" {
			p.Translations["en"] = translation
		}
		posts = append(posts, p)
	}
	for i, variant := range []string{"private", "draft", "future", "page", "attachment", "unlisted", "invalid date", "missing date", "zero year"} {
		p := archiveTestPost(int64(30+i), "2030-06-01")
		switch variant {
		case "private", "draft", "future":
			p.Status = variant
		case "page", "attachment":
			p.Type = variant
		case "unlisted":
			p.Unlisted = true
		case "invalid date":
			p.PublishedAt = "2030-02-30"
		case "missing date":
			p.PublishedAt = ""
		case "zero year":
			p.PublishedAt = "0000-01-01"
		}
		posts = append(posts, p)
	}
	settings := SiteSettings{BaseURL: "https://example.test"}
	for _, language := range []string{"zh", "en"} {
		view := newPublicView(settings, language)
		populatePublicArchives(&view, settings, posts, nil, language)
		wantYears, wantCounts := []string{"2026", "2025", "2024"}, []int{3, 2, 5}
		if language == "en" {
			wantYears, wantCounts = wantYears[:2], []int{3, 1}
		}
		if len(view.ArchiveYears) != len(wantYears) || !view.ArchiveAllCurrent {
			t.Fatalf("%s public archive years/all state: %+v", language, view.ArchiveYears)
		}
		for i, year := range view.ArchiveYears {
			if archiveURLQuery(t, year.URL, language).Get("m") != wantYears[i] || year.Count != wantCounts[i] || year.Current || year.Open != (i == 0) {
				t.Fatalf("%s year %d has incorrect count/order/state: %+v", language, i, year)
			}
		}
		months := view.ArchiveYears[0].Months
		if len(months) != 2 || archiveURLQuery(t, months[0].URL, language).Get("m") != "202610" || months[0].Count != 1 || archiveURLQuery(t, months[1].URL, language).Get("m") != "202601" || months[1].Count != 2 {
			t.Fatalf("%s months lost order/count or crossed UTC month boundary: %+v", language, months)
		}
		if month := view.ArchiveYears[1].Months; len(month) != 1 || archiveURLQuery(t, month[0].URL, language).Get("m") != "202512" {
			t.Fatalf("%s negative-offset article moved into the next UTC year: %+v", language, month)
		}
	}
}

func TestPublicArchivesPreserveFiltersResetPaginationAndSelectDates(t *testing.T) {
	settings := SiteSettings{BaseURL: "https://example.test"}
	posts := []Post{archiveTestPost(1, "2026-01-01"), archiveTestPost(2, "2025-12-01")}
	for _, selected := range []string{"", "2025", "202601", "2023"} {
		query := url.Values{"cat": {"13"}, "tag": {"标签"}, "s": {"needle"}, "author": {"1"}, "post_format": {"standard"}, "paged": {"8"}}
		if selected != "" {
			query.Set("m", selected)
		}
		before := query.Encode()
		view := newPublicView(settings, "en")
		populatePublicArchives(&view, settings, posts, query, "en")
		if query.Encode() != before || view.ArchiveAllCurrent != (selected == "") || len(view.ArchiveYears) != 2 {
			t.Fatalf("selection %q changed query, counts or all-date state", selected)
		}
		for i, year := range view.ArchiveYears {
			key := []string{"2026", "2025"}[i]
			wantOpen := (selected == "" && i == 0) || strings.HasPrefix(selected, key)
			if year.Current != (selected == key) || year.Open != wantOpen {
				t.Fatalf("selection %q has incorrect year state: %+v", selected, year)
			}
			for _, link := range append([]publicLink{{URL: year.URL}}, year.Months...) {
				values := archiveURLQuery(t, link.URL, "en")
				if values.Has("paged") {
					t.Fatalf("date link retained stale page: %s", link.URL)
				}
				for _, filter := range []string{"cat", "tag", "s", "author", "post_format"} {
					if values.Get(filter) != query.Get(filter) {
						t.Fatalf("date link dropped %s: %s", filter, link.URL)
					}
				}
			}
			for _, month := range year.Months {
				if month.Current != (archiveURLQuery(t, month.URL, "en").Get("m") == selected) {
					t.Fatalf("selection %q has incorrect month state: %+v", selected, month)
				}
			}
		}
		all := archiveURLQuery(t, view.ArchiveAllURL, "en")
		if all.Has("m") || all.Get("paged") != "1" {
			t.Fatalf("all dates did not clear date/reset page: %s", view.ArchiveAllURL)
		}
		for _, filter := range []string{"cat", "tag", "s", "author", "post_format"} {
			if all.Get(filter) != query.Get(filter) {
				t.Fatalf("all dates dropped %s", filter)
			}
		}
	}
}

func TestPublicArchiveDateLabelsAndYearMonthFilters(t *testing.T) {
	for _, language := range publicLanguages() {
		year, month := publicArchiveDateLabel("2026", language), publicArchiveDateLabel("202601", language)
		if language == "zh" {
			if year != "2026年" || !strings.Contains(month, "1月") {
				t.Fatalf("Chinese archive labels: %q, %q", year, month)
			}
		} else if year != "2026" || month != "2026-01" {
			t.Fatalf("%s numeric archive labels: %q, %q", language, year, month)
		}
	}
	posts := []Post{archiveTestPost(1, "2026-01-01T00:30:00+14:00"), archiveTestPost(2, "2026-10-20"), archiveTestPost(3, "2025-12-31T23:30:00-12:00")}
	for _, test := range []struct {
		date string
		ids  []int64
	}{{"2026", []int64{1, 2}}, {"202601", []int64{1}}, {"202610", []int64{2}}, {"2025", []int64{3}}, {"202512", []int64{3}}} {
		filtered, valid := filterPublicPosts(posts, SiteSettings{}, url.Values{"m": {test.date}}, "en")
		got := map[int64]bool{}
		for _, post := range filtered {
			got[post.ID] = true
		}
		want := map[int64]bool{}
		for _, id := range test.ids {
			want[id] = true
		}
		if !valid || !reflect.DeepEqual(got, want) {
			t.Fatalf("date %s returned %v, want %v (valid=%t)", test.date, got, want, valid)
		}
	}
}

func TestPublicArchivesHTTPYearPaginationMonthAndLanguage(t *testing.T) {
	app := publicTestApp(t)
	for i, date := range []string{"2020-01-01T00:30:00+14:00", "2020-05-01", "2020-05-02", "2020-12-31T23:30:00-12:00"} {
		post := archiveTestPost(int64(30001+i), date)
		if err := app.Store.saveLocked(context.Background(), &post); err != nil {
			t.Fatal(err)
		}
	}
	for _, language := range publicLanguages() {
		for _, layout := range []string{"journal", "classic"} {
			t.Run(language+"/"+layout, func(t *testing.T) {
				prefix := publicLanguagePrefix(language)
				response := publicLayoutTestRequest(app, prefix+"/?m=2020&paged=2", layout)
				if response.Code != http.StatusOK {
					t.Fatalf("year page two returned HTTP %d", response.Code)
				}
				body := response.Body.String()
				if len(publicMetadataNodes(t, body, "section", "sidebar-archives")) != 1 || !strings.Contains(body, `data-archive-year="2020"`) {
					t.Fatal("archive sidebar missing when analytics is unavailable")
				}
				canonical := ""
				for _, node := range publicMetadataNodes(t, body, "link", "") {
					if publicMetadataAttr(node, "rel") == "canonical" {
						canonical = publicMetadataAttr(node, "href")
					}
				}
				if canonical != "https://www.ganwen.nl"+prefix+"/?m=2020&paged=2" {
					t.Fatalf("year page canonical lost date/language/page: %s", canonical)
				}
				for _, node := range publicMetadataNodes(t, body, "a", "") {
					if targetLanguage := publicMetadataAttr(node, "lang"); targetLanguage != "" {
						query := archiveURLQuery(t, publicMetadataAttr(node, "href"), targetLanguage)
						if query.Get("m") != "2020" || query.Get("paged") != "2" {
							t.Fatal("language switch lost year/page")
						}
					}
				}
				pagination := publicMetadataNodes(t, body, "nav", "pagination")
				if len(pagination) != 1 {
					t.Fatal("year archive lost pagination")
				}
				var visit func(*html.Node)
				visit = func(node *html.Node) {
					if node.Data == "a" {
						if query := archiveURLQuery(t, publicMetadataAttr(node, "href"), language); query.Get("m") != "2020" {
							t.Fatal("pagination lost year filter")
						}
					}
					for child := node.FirstChild; child != nil; child = child.NextSibling {
						visit(child)
					}
				}
				visit(pagination[0])
				if !strings.Contains(body, `name="m" value="2020"`) || !strings.Contains(body, "ARCHIVE_ARTICLE_30001") || !strings.Contains(body, "ARCHIVE_ARTICLE_30002") || strings.Contains(body, "ARCHIVE_ARTICLE_30003") || strings.Contains(body, "ARCHIVE_ARTICLE_30004") {
					t.Fatal("year page two lost jump filter or returned incorrect articles")
				}
				month := publicLayoutTestRequest(app, prefix+"/?m=202005", layout)
				if month.Code != http.StatusOK || !strings.Contains(month.Body.String(), "ARCHIVE_ARTICLE_30002") || !strings.Contains(month.Body.String(), "ARCHIVE_ARTICLE_30003") || strings.Contains(month.Body.String(), "ARCHIVE_ARTICLE_30001") || strings.Contains(month.Body.String(), "ARCHIVE_ARTICLE_30004") {
					t.Fatal("month filter returned articles from another month")
				}
			})
		}
	}
	for _, invalid := range []string{"-2026", "202", "20260", "2026011", "202x", "0000", "000001", "202600", "202613"} {
		if response := publicTestRequest(app, "/?m="+url.QueryEscape(invalid)); response.Code != http.StatusNotFound {
			t.Fatalf("invalid date %q returned HTTP %d", invalid, response.Code)
		}
	}
}
