package blog

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func publicLayoutTestRequest(a *App, target, layout string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://localhost"+target, nil)
	r.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "zh"})
	if layout != "" {
		r.AddCookie(&http.Cookie{Name: "ganwen.layout.v1", Value: layout})
	}
	a.handlePublic(w, r)
	return w
}

func publicSchemaGraph(t *testing.T, body string) (string, []map[string]any) {
	t.Helper()
	var raw string
	for _, node := range publicMetadataNodes(t, body, "script", "") {
		if publicMetadataAttr(node, "type") == "application/ld+json" {
			if raw != "" {
				t.Fatal("multiple structured data scripts")
			}
			raw = publicMetadataText(node)
		}
	}
	var schema struct {
		Context string           `json:"@context"`
		Graph   []map[string]any `json:"@graph"`
	}
	if err := json.Unmarshal([]byte(raw), &schema); err != nil || schema.Context != "https://schema.org" {
		t.Fatalf("invalid structured data: %v", err)
	}
	return raw, schema.Graph
}

func publicSchemaOfType(graph []map[string]any, kind string) map[string]any {
	for _, node := range graph {
		if node["@type"] == kind {
			return node
		}
	}
	return nil
}

func publicMetaValue(t *testing.T, body, name string) string {
	t.Helper()
	for _, node := range publicMetadataNodes(t, body, "meta", "") {
		if publicMetadataAttr(node, "property") == name || publicMetadataAttr(node, "name") == name {
			return publicMetadataAttr(node, "content")
		}
	}
	return ""
}

func TestPublicJournalAndClassicShareCanonicalAndMetadata(t *testing.T) {
	a := publicTestApp(t)
	for _, target := range []string{"/", "/?paged=1", "/?cat=13&paged=2", "/?p=1447", "/?page_id=2", "/en/?p=1447"} {
		journal := publicLayoutTestRequest(a, target, "")
		classic := publicLayoutTestRequest(a, target, "classic")
		if journal.Code != http.StatusOK || classic.Code != http.StatusOK {
			t.Fatalf("%s layout response = %d/%d", target, journal.Code, classic.Code)
		}
		if !strings.Contains(journal.Body.String(), `data-layout="journal"`) || !strings.Contains(classic.Body.String(), `data-layout="classic"`) {
			t.Fatalf("%s did not honor the layout cookie", target)
		}
		journalSchema, _ := publicSchemaGraph(t, journal.Body.String())
		classicSchema, _ := publicSchemaGraph(t, classic.Body.String())
		if journalSchema != classicSchema {
			t.Fatalf("%s layout selection changed metadata or canonical", target)
		}
		for _, response := range []*httptest.ResponseRecorder{journal, classic} {
			if !strings.Contains(response.Header().Get("Vary"), "Cookie") || !strings.Contains(response.Header().Get("Cache-Control"), "private") {
				t.Fatal("layout can leak through shared caches")
			}
		}
	}
	home := publicLayoutTestRequest(a, "/", "").Body.String()
	if len(publicMetadataNodes(t, home, "section", "featured-story")) != 1 || len(publicMetadataNodes(t, home, "article", "journal-post")) != 2 || len(publicMetadataNodes(t, home, "section", "category-card")) != 0 {
		t.Fatal("journal homepage did not render exactly one featured article and the remaining articles once")
	}
	classic := publicLayoutTestRequest(a, "/", "classic").Body.String()
	if len(publicMetadataNodes(t, classic, "section", "category-card")) != 1 || len(publicMetadataNodes(t, classic, "article", "journal-post")) != 0 {
		t.Fatal("classic homepage cards were not preserved")
	}
	for _, layout := range []string{"journal", "unknown", "CLASSIC"} {
		if body := publicLayoutTestRequest(a, "/", layout).Body.String(); !strings.Contains(body, `data-layout="journal"`) {
			t.Fatalf("unexpected layout cookie %q did not use default", layout)
		}
	}
	for _, test := range []struct{ layout, first string }{{"journal", "最新文章"}, {"classic", "保留原文 & 图片"}} {
		body := publicLayoutTestRequest(a, "/?paged=1", test.layout).Body.String()
		class := "journal-post"
		if test.layout == "classic" {
			class = "archive-card"
		}
		cards := publicMetadataNodes(t, body, "article", class)
		if len(cards) != 2 || !strings.Contains(publicMetadataText(cards[0]), test.first) {
			t.Fatalf("%s archive ordering changed", test.layout)
		}
	}
}

func TestPublicHomeSelectionUsesChronologyAndAvailableLanguage(t *testing.T) {
	s, _ := taxonomyTestData()
	var posts []Post
	for i := 1; i <= 12; i++ {
		p := Post{ID: int64(i), Type: "post", Status: "publish", Title: fmt.Sprintf("Post %d", i), ContentHTML: "<p>visible</p>", PublishedAt: fmt.Sprintf("2026-10-%02dT12:00:00Z", i), Categories: s.Categories[:1]}
		p.Translations = map[string]Translation{"en": {Title: fmt.Sprintf("English %d", i), ContentHTML: "<p>visible translation</p>", SourceHash: SourceHash(p), Status: "publish"}}
		posts = append(posts, p)
	}
	posts[0].Sticky, posts[1].Sticky = true, true
	posts[11].Translations["en"] = Translation{Title: "Stale title", ContentHTML: "stale", Status: "publish", SourceHash: "stale"}
	posts = append(posts, Post{ID: 99, Type: "post", Status: "private", Sticky: true}, Post{ID: 2, Type: "page", Status: "publish", Title: "About", ContentHTML: "<p>about</p>"})
	for _, test := range []struct {
		lang   string
		count  int
		newest int64
	}{{"zh", 12, 12}, {"en", 11, 11}} {
		view := newPublicView(s, test.lang)
		populatePublicNavigation(&view, s, posts, test.lang)
		list, _ := filterPublicPosts(posts, s, nil, test.lang)
		populatePublicHome(&view, list, s, test.lang)
		populatePublicTaxonomy(&view, s, posts, nil, test.lang)
		if view.PostCount != test.count || view.Featured == nil || view.Featured.ID != 2 || len(view.RecentCards) != 8 || view.RecentCards[0].ID != test.newest || view.Categories[0].Count != test.count {
			t.Fatalf("%s incorrect featured/recent/count selection: %+v", test.lang, view)
		}
		for _, card := range view.RecentCards {
			if card.ID == view.Featured.ID {
				t.Fatal("featured article duplicated in recent list")
			}
		}
		if (view.AboutURL != "") != (test.lang == "zh") {
			t.Fatal("about link exposed an unavailable translation")
		}
	}
	posts[0].Sticky, posts[1].Sticky = false, false
	list, _ := filterPublicPosts(posts, s, nil, "zh")
	view := newPublicView(s, "zh")
	populatePublicHome(&view, list, s, "zh")
	if view.Featured == nil || view.Featured.ID != 12 {
		t.Fatal("latest article was not featured without a pinned article")
	}
	order := []Post{{ID: 1, PublishedAt: "2026-10-08T23:00:00+08:00"}, {ID: 2, PublishedAt: "2026-10-08T18:00:00Z"}, {ID: 3, PublishedAt: "invalid"}}
	publicSortByDate(order)
	if order[0].ID != 2 || order[1].ID != 1 || order[2].ID != 3 {
		t.Fatal("timestamps were sorted lexically or invalid dates came first")
	}
}

func TestPublicStructuredMetadataIsSafeLocalizedAndTyped(t *testing.T) {
	a := publicTestApp(t)
	post, err := a.Store.GetPost(context.Background(), 1447)
	if err != nil {
		t.Fatal(err)
	}
	post.Title = `Title </script><script id="injected">alert(1)</script>`
	post.SEO.Description = ""
	post.ContentHTML = "<p>A useful description.</p><script>hidden secret</script>"
	post.FeaturedImage = "/wp-content/uploads/photo.jpg"
	post.ModifiedAt = "malformed-modification"
	post.Translations["en"] = Translation{Title: "English headline", ContentHTML: "<p>English description.</p>", Status: "publish", SourceHash: SourceHash(post), UpdatedAt: "2026-10-08T09:00:00Z"}
	if err := a.Store.saveLocked(context.Background(), &post); err != nil {
		t.Fatal(err)
	}
	response := publicLayoutTestRequest(a, "/?p=1447", "")
	body := response.Body.String()
	raw, graph := publicSchemaGraph(t, body)
	article := publicSchemaOfType(graph, "BlogPosting")
	if article == nil || article["headline"] != post.Title || article["description"] != "A useful description." || article["dateModified"] != nil || article["datePublished"] != "2025-01-30T06:43:26+08:00" {
		t.Fatalf("incorrect article metadata: %#v", article)
	}
	if strings.Contains(raw, "</script>") || strings.Contains(body, "malformed-modification") || strings.Contains(body, "hidden secret") {
		t.Fatal("unsafe script text or invalid timestamp leaked into page")
	}
	if len(publicMetadataNodes(t, body, "script", "")) != 3 {
		t.Fatal("untrusted text created an executable script")
	}
	hash := sha256.Sum256([]byte(raw))
	if !strings.Contains(response.Header().Get("Content-Security-Policy"), "'sha256-"+base64.StdEncoding.EncodeToString(hash[:])+"'") || strings.Contains(response.Header().Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("structured data CSP does not match the actual script")
	}
	if publicMetaValue(t, body, "og:type") != "article" || publicMetaValue(t, body, "og:image") != "https://www.ganwen.nl/wp-content/uploads/photo.jpg" || publicMetaValue(t, body, "twitter:card") != "summary_large_image" || publicMetaValue(t, body, "description") != "A useful description." {
		t.Fatal("social metadata missing or has relative image URL")
	}
	english := publicLayoutTestRequest(a, "/en/?p=1447", "").Body.String()
	_, graph = publicSchemaGraph(t, english)
	article = publicSchemaOfType(graph, "BlogPosting")
	if article["headline"] != "English headline" || article["description"] != "English description." || article["inLanguage"] != "en" || article["dateModified"] != "2026-10-08T09:00:00Z" || strings.Contains(english, "A useful description.") {
		t.Fatal("translated metadata used source content or lost its update date")
	}
	about := publicLayoutTestRequest(a, "/?page_id=2", "").Body.String()
	_, graph = publicSchemaGraph(t, about)
	if publicSchemaOfType(graph, "AboutPage") == nil || publicSchemaOfType(graph, "BlogPosting") != nil || publicMetaValue(t, about, "og:type") != "website" || publicMetaValue(t, about, "article:published_time") != "" {
		t.Fatal("about page is incorrectly described as a blog article")
	}
	a.Config.Preview = true
	preview := publicLayoutTestRequest(a, "/?p=1447", "")
	if preview.Header().Get("X-Robots-Tag") != "noindex, nofollow" || publicMetaValue(t, preview.Body.String(), "robots") != "noindex, nofollow" {
		t.Fatal("preview indexing protection lost")
	}
}

func TestPublicNavigationCurrentStateAndAboutAvailability(t *testing.T) {
	a := publicTestApp(t)
	for _, test := range []struct{ target, current string }{{"/", "/"}, {"/?paged=1", "/?paged=1"}, {"/?p=1447", "/?paged=1"}, {"/?page_id=2", "/?page_id=2"}, {"/en/?p=1447", "/en/?paged=1"}} {
		body := publicLayoutTestRequest(a, test.target, "").Body.String()
		active := 0
		for _, link := range publicMetadataNodes(t, body, "a", "nav-item") {
			if publicMetadataAttr(link, "aria-current") == "page" {
				active++
				if publicMetadataAttr(link, "href") != test.current {
					t.Fatalf("%s highlights the wrong navigation item", test.target)
				}
			}
			if strings.HasPrefix(test.target, "/en/") && strings.Contains(publicMetadataAttr(link, "href"), "page_id=2") {
				t.Fatal("navigation points to an unavailable about translation")
			}
		}
		if active != 1 {
			t.Fatalf("%s has %d active navigation items", test.target, active)
		}
	}
}

func TestPublicDescriptionDatesAndReadingEstimate(t *testing.T) {
	if publicDate("not-a-date", "zh") != "" || publicTimestamp("not-a-date") != "" || publicTimestamp("0000-01-01") != "" {
		t.Fatal("invalid date is rendered as metadata")
	}
	if publicModifiedTimestamp(Post{PublishedAt: "2026-10-08", ModifiedAt: "2020-01-01"}, "zh") != "" {
		t.Fatal("modification before publication is presented as valid")
	}
	if publicReadMinutes("<p>"+strings.Repeat("文", 801)+"</p>") != 3 || publicReadMinutes(strings.Repeat("word ", 441)) != 3 || publicReadMinutes("<script>"+strings.Repeat("secret ", 10000)+"</script><p>Short.</p>") != 1 {
		t.Fatal("reading estimate does not use visible CJK characters and words")
	}
	for _, value := range []string{"javascript:alert(1)", "data:image/png;base64,AAAA", "https://user:password@example.com/image.png", ""} {
		if publicAbsoluteURL(value, "https://www.ganwen.nl") != "" {
			t.Fatalf("unsafe social image URL accepted: %q", value)
		}
	}
}
