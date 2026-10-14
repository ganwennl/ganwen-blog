package blog

import (
	"context"
	"encoding/json"
	"encoding/xml"
	stdhtml "html"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func taxonomyTestData() (SiteSettings, []Post) {
	s := SiteSettings{Title: "甘文", BaseURL: "https://www.ganwen.nl", PostsPerPage: 5,
		Categories: []Term{
			{ID: 3, Name: "感触", Slug: "%e6%84%9f%e8%a7%a6", Taxonomy: "category", URL: "https://www.ganwen.nl/?cat=3"},
			{ID: 13, Name: "意境", Slug: "%e6%84%8f%e5%a2%83", Taxonomy: "category", URL: "https://www.ganwen.nl/?cat=13"},
			{ID: 5, Name: "爱情", Slug: "%e7%88%b1%e6%83%85", Taxonomy: "category", URL: "https://www.ganwen.nl/?cat=5"},
		}, Tags: []Term{
			{ID: 22, Name: "习近平", Slug: "%e4%b9%a0%e8%bf%91%e5%b9%b3", Taxonomy: "post_tag", URL: "https://www.ganwen.nl/?tag=%e4%b9%a0%e8%bf%91%e5%b9%b3"},
			{ID: 30, Name: "晴天", Slug: "晴天", Taxonomy: "post_tag", URL: "https://www.ganwen.nl/?tag=%E6%99%B4%E5%A4%A9"},
			{ID: 90, Name: "私密设置标签", Slug: "private-only", Taxonomy: "post_tag", URL: "https://www.ganwen.nl/?tag=private-only"},
		}}
	posts := []Post{
		{ID: 1, Type: "post", Status: "publish", Title: "公开感触", ContentHTML: "<p>公开内容</p>", CanonicalURL: "https://www.ganwen.nl/?p=1", Categories: s.Categories[:1], Tags: s.Tags[:1]},
		{ID: 2, Type: "post", Status: "publish", Title: "公开意境", ContentHTML: "<p>晴天</p>", CanonicalURL: "https://www.ganwen.nl/?p=2", Categories: s.Categories[1:2], Tags: s.Tags[1:2]},
		{ID: 3, Type: "post", Status: "private", Title: "私密文章", ContentHTML: "<p>不公开</p>", Tags: append([]Term{s.Tags[2]}, Term{ID: 91, Name: "仅私密文章的标签", Slug: "private-derived", Taxonomy: "post_tag"})},
		{ID: 4, Type: "post", Status: "draft", Title: "草稿", ContentHTML: "<p>不公开</p>", Tags: []Term{{ID: 92, Name: "草稿标签", Slug: "draft-only", Taxonomy: "post_tag"}}},
	}
	for i := range posts {
		if posts[i].Status == "publish" {
			posts[i].Translations = map[string]Translation{}
			for _, lang := range translationLanguages() {
				posts[i].Translations[lang] = Translation{Title: posts[i].Title + " " + lang, ContentHTML: posts[i].ContentHTML, SourceHash: SourceHash(posts[i]), Status: "publish"}
			}
		}
	}
	return s, posts
}

func taxonomyView(s SiteSettings, posts []Post, lang string, q url.Values) publicView {
	v := newPublicView(s, lang)
	populatePublicTaxonomy(&v, s, posts, q, lang)
	return v
}

func TestPublicTaxonomyIndependentFiltersAndPaginationReset(t *testing.T) {
	s, posts := taxonomyTestData()
	q, _ := url.ParseQuery("cat=13&tag=%e4%b9%a0%e8%bf%91%e5%b9%b3&paged=8&s=needle&author=1&m=202610&post_format=standard")
	before := q.Encode()
	for _, lang := range publicLanguages() {
		v := taxonomyView(s, posts, lang, q)
		if len(v.Categories) != 3 || len(v.FilterTags) != 2 || !v.Categories[1].Current || !v.FilterTags[0].Current || !v.FiltersActive || v.CategoryAllCurrent || v.TagAllCurrent {
			t.Fatalf("%s incorrect terms/current selection", lang)
		}
		for _, link := range []publicLink{v.Categories[0], v.FilterTags[1]} {
			u, _ := url.Parse(link.URL)
			values := u.Query()
			if u.Path != publicLanguagePrefix(lang)+"/" || values.Get("paged") != "" || values.Get("s") != "needle" || values.Get("author") != "1" || values.Get("m") != "202610" || values.Get("post_format") != "standard" {
				t.Fatalf("%s lost language/other filters or retained old page: %s", lang, link.URL)
			}
		}
		category, _ := url.Parse(v.Categories[0].URL)
		if category.Query().Get("cat") != "3" || category.Query().Get("tag") != "习近平" {
			t.Fatal("category selection lost tag filter")
		}
		tag, _ := url.Parse(v.FilterTags[1].URL)
		if tag.Query().Get("cat") != "13" || tag.Query().Get("tag") != "晴天" {
			t.Fatal("tag selection lost category filter")
		}
		categoryAll, _ := url.Parse(v.CategoryAllURL)
		tagAll, _ := url.Parse(v.TagAllURL)
		clear, _ := url.Parse(v.ClearFiltersURL)
		if categoryAll.Query().Get("cat") != "" || categoryAll.Query().Get("tag") != "习近平" || tagAll.Query().Get("cat") != "13" || tagAll.Query().Get("tag") != "" {
			t.Fatal("dimension reset cleared the other filter")
		}
		if clear.Query().Get("cat") != "" || clear.Query().Get("tag") != "" || clear.Query().Get("s") != "needle" || clear.Query().Get("paged") != "1" {
			t.Fatal("clear filters lost search or did not reset page")
		}
	}
	if q.Encode() != before {
		t.Fatal("navigation mutated request query")
	}
	v := taxonomyView(s, posts, "zh", nil)
	if v.Categories[0].URL != s.Categories[0].URL || v.FilterTags[0].URL != s.Tags[0].URL || !v.CategoryAllCurrent || !v.TagAllCurrent || v.FiltersActive {
		t.Fatal("original term URLs or initial all selection changed")
	}
}

func TestPublicUncategorizedGroupLinksToAllArticles(t *testing.T) {
	s, _ := taxonomyTestData()
	post := Post{ID: 10000, Type: "post", Status: "publish", Title: "New publication", ContentHTML: "<p>New article</p>", CanonicalURL: s.BaseURL + "/?p=10000", Translations: map[string]Translation{}}
	for _, lang := range translationLanguages() {
		post.Translations[lang] = Translation{Title: post.Title + " " + lang, ContentHTML: post.ContentHTML, SourceHash: SourceHash(post), Status: "publish"}
	}
	a := taxonomyTestApp(t, s, []Post{post})
	for _, lang := range publicLanguages() {
		groups := publicGroups([]Post{post}, s, lang, false)
		if len(groups) != 1 || len(groups[0].Cards) != 1 {
			t.Fatalf("%s new uncategorized publication missing from overview", lang)
		}
		u, err := url.Parse(groups[0].URL)
		if err != nil || u.Path != publicLanguagePrefix(lang)+"/" || u.RawQuery != "paged=1" {
			t.Fatalf("%s uncategorized group does not link to all articles: %s", lang, groups[0].URL)
		}
		w := publicLayoutTestRequest(a, u.RequestURI(), "classic")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `class="archive-card post-preview"`) || !strings.Contains(w.Body.String(), "New publication") {
			t.Fatalf("%s all-articles destination must show the new publication, status %d", lang, w.Code)
		}
		home := publicLayoutTestRequest(a, publicLanguagePrefix(lang)+"/", "classic")
		if home.Code != 200 || strings.Contains(home.Body.String(), "category_name=") || !strings.Contains(home.Body.String(), "?paged=1") {
			t.Fatalf("%s overview still exposes an empty category link, status %d", lang, home.Code)
		}
	}
}

func TestPublicTaxonomyEncodedWordPressQueriesAndEmptyIntersection(t *testing.T) {
	s, posts := taxonomyTestData()
	originalTag, _ := url.Parse(s.Tags[0].URL)
	originalCategory, _ := url.Parse("https://www.ganwen.nl/?category_name=" + s.Categories[0].Slug)
	for _, lang := range publicLanguages() {
		for _, query := range []url.Values{originalTag.Query(), originalCategory.Query()} {
			filtered, valid := filterPublicPosts(posts, s, query, lang)
			if !valid || len(filtered) != 1 || filtered[0].ID != 1 {
				t.Fatalf("%s original percent-encoded query failed: %s", lang, query.Encode())
			}
		}
		category := taxonomyView(s, posts, lang, originalCategory.Query())
		if !category.Categories[0].Current || category.CategoryAllCurrent {
			t.Fatal("category_name failed to select its original category")
		}
		for _, query := range []url.Values{{"cat": {"5"}}, {"cat": {"13"}, "tag": {"习近平"}}} {
			filtered, valid := filterPublicPosts(posts, s, query, lang)
			if !valid || len(filtered) != 0 {
				t.Fatalf("%s known empty category/intersection should remain a valid archive", lang)
			}
		}
		generated, _ := url.Parse(publicTermURL(s.Tags[0], s, lang))
		if generated.Query().Get("tag") != "习近平" || strings.Contains(generated.RawQuery, "%25") {
			t.Fatalf("%s tag URL double-encoded its WordPress slug", lang)
		}
		// Older generated links contained a second percent-encoding layer;
		// accept them while new links use the original single-encoded query.
		if filtered, valid := filterPublicPosts(posts, s, url.Values{"tag": {s.Tags[0].Slug}}, lang); !valid || len(filtered) != 1 {
			t.Fatal("legacy double-encoded tag query lost compatibility")
		}
	}
}

func TestPublicTaxonomyNeverDerivesPrivateOnlyTagNames(t *testing.T) {
	s, posts := taxonomyTestData()
	posts = append(posts, Post{ID: 5, Type: "post", Status: "publish", Tags: []Term{{ID: 93, Name: "新公开标签", Slug: "new-public", Taxonomy: "post_tag"}}},
		Post{ID: 6, Type: "attachment", Status: "publish", Tags: []Term{{ID: 94, Name: "附件标签", Slug: "attachment-only", Taxonomy: "post_tag"}}})
	v := taxonomyView(s, posts, "zh", nil)
	if len(v.FilterTags) != 3 || v.FilterTags[2].Label != "新公开标签" {
		t.Fatal("published terms missing from settings disappeared")
	}
	for _, term := range v.FilterTags {
		if strings.Contains(term.Label, "私密") || strings.Contains(term.Label, "草稿") || strings.Contains(term.Label, "附件") {
			t.Fatal("nonpublic tag name exposed in navigation")
		}
	}
	for _, slug := range []string{"private-only", "private-derived", "draft-only", "attachment-only"} {
		q := url.Values{"tag": {slug}}
		if _, valid := filterPublicPosts(posts, s, q, "zh"); valid {
			t.Fatalf("nonpublic-only tag archive accepted: %s", slug)
		}
		if heading := publicArchiveHeading(s, posts, q, "zh"); heading != "" {
			t.Fatalf("nonpublic-only tag name exposed as heading: %s", slug)
		}
	}
}

func TestPublicTaxonomyNewZeroIDCategoriesAcrossLanguages(t *testing.T) {
	s, posts := taxonomyTestData()
	newCategory := Term{Name: "新随笔", Slug: "%e6%96%b0%e9%9a%8f%e7%ac%94", Taxonomy: "category"}
	configuredEmpty := Term{Name: "手写分类", Slug: "手写分类", Taxonomy: "category"}
	s.Categories = append(s.Categories, configuredEmpty)
	post := Post{ID: 7, Type: "post", Status: "publish", Title: "公开的新随笔", ContentHTML: "<p>公开正文</p>", Categories: []Term{newCategory}, Translations: map[string]Translation{}}
	for _, lang := range translationLanguages() {
		post.Translations[lang] = Translation{Title: post.Title, ContentHTML: post.ContentHTML, SourceHash: SourceHash(post), Status: "publish"}
	}
	posts = append(posts, post, Post{ID: 8, Type: "post", Status: "private", Categories: []Term{{Name: "私密隐藏分类", Slug: "private-category", Taxonomy: "category"}}})
	a := taxonomyTestApp(t, s, posts)
	for _, lang := range publicLanguages() {
		v := taxonomyView(s, posts, lang, nil)
		if len(v.Categories) != 5 {
			t.Fatalf("%s lost original/empty categories or exposed a private category", lang)
		}
		for _, category := range v.Categories {
			if strings.Contains(category.Label, "私密") {
				t.Fatal("private-only category exposed")
			}
		}
		generated, _ := url.Parse(publicTermURL(newCategory, s, lang))
		if generated.Path != publicLanguagePrefix(lang)+"/" || generated.Query().Get("category_name") != "新随笔" || generated.Query().Has("cat") || strings.Contains(generated.RawQuery, "%25") {
			t.Fatalf("%s zero-ID category produced an invalid archive URL", lang)
		}
		filtered, valid := filterPublicPosts(posts, s, generated.Query(), lang)
		if !valid || len(filtered) != 1 || filtered[0].ID != 7 {
			t.Fatalf("%s new category cannot filter its published article", lang)
		}
		w := publicTestRequest(a, generated.RequestURI())
		if w.Code != 200 {
			t.Fatalf("%s new category HTTP status %d", lang, w.Code)
		}
		assertCurrentTaxonomyLink(t, w.Body.String(), "category_name", "新随笔")
		empty, _ := url.Parse(publicTermURL(configuredEmpty, s, lang))
		if w := publicTestRequest(a, empty.RequestURI()); w.Code != 200 {
			t.Fatalf("%s configured empty zero-ID category returned %d", lang, w.Code)
		}
		for _, query := range []url.Values{{"category_name": {"unknown-category"}}, {"category_name": {"private-category"}}, {"cat": {"0"}}} {
			if _, valid := filterPublicPosts(posts, s, query, lang); valid {
				t.Fatalf("%s unknown/private/zero category accepted: %s", lang, query.Encode())
			}
		}
	}
}

func TestPublicTaxonomyRelativeURLsProduceAbsoluteSitemapLocations(t *testing.T) {
	s, posts := taxonomyTestData()
	category := Term{Name: "新公开分类", Slug: "新公开分类", URL: "/?category_name=%E6%96%B0%E5%85%AC%E5%BC%80%E5%88%86%E7%B1%BB", Taxonomy: "category"}
	tag := Term{Name: "新公开标签", Slug: "新公开标签", URL: "/?tag=%E6%96%B0%E5%85%AC%E5%BC%80%E6%A0%87%E7%AD%BE", Taxonomy: "post_tag"}
	post := Post{ID: 7, Type: "post", Status: "publish", Title: "公开的新文章", ContentHTML: "<p>公开内容</p>", Categories: []Term{category}, Tags: []Term{tag}, Translations: map[string]Translation{}}
	for _, lang := range translationLanguages() {
		post.Translations[lang] = Translation{Title: post.Title, ContentHTML: post.ContentHTML, SourceHash: SourceHash(post), Status: "publish"}
	}
	posts = append(posts, post, Post{ID: 8, Type: "post", Status: "private", Categories: []Term{{Name: "隐藏新增分类", Slug: "hidden-category", URL: "/?category_name=hidden-category", Taxonomy: "category"}}, Tags: []Term{{Name: "隐藏新增标签", Slug: "hidden-tag", URL: "/?tag=hidden-tag", Taxonomy: "post_tag"}}})
	a := taxonomyTestApp(t, s, posts)
	if publicTermURL(s.Categories[0], s, "zh") != s.Categories[0].URL || publicTermURL(s.Tags[0], s, "zh") != s.Tags[0].URL {
		t.Fatal("original WordPress absolute URLs changed")
	}
	for _, lang := range publicLanguages() {
		for _, term := range []Term{category, tag} {
			generated, err := url.Parse(publicTermURL(term, s, lang))
			if err != nil || !generated.IsAbs() || generated.Scheme != "https" || generated.Host != "www.ganwen.nl" || generated.Path != publicLanguagePrefix(lang)+"/" {
				t.Fatalf("%s new term URL must be absolute HTTPS with its language prefix", lang)
			}
			if w := publicTestRequest(a, generated.RequestURI()); w.Code != 200 {
				t.Fatalf("%s new term URL is not a usable archive: %d", lang, w.Code)
			}
		}
		for _, query := range []string{"category_name=hidden-category", "tag=hidden-tag"} {
			if w := publicTestRequest(a, publicLanguagePrefix(lang)+"/?"+query); w.Code != 404 {
				t.Fatal("new private-only taxonomy is publicly accessible")
			}
		}
	}
	for _, test := range []struct {
		sitemap, key, value string
	}{{"taxonomies-category-1", "category_name", category.Slug}, {"taxonomies-post_tag-1", "tag", tag.Slug}} {
		w := publicTestRequest(a, "/?sitemap="+test.sitemap)
		var sitemap publicSitemap
		if w.Code != 200 || xml.Unmarshal(w.Body.Bytes(), &sitemap) != nil {
			t.Fatal("taxonomy sitemap response invalid")
		}
		found := make(map[string]bool)
		for _, item := range sitemap.URLs {
			u, err := url.Parse(item.Loc)
			if err != nil || !u.IsAbs() || u.Scheme != "https" || u.Host != "www.ganwen.nl" {
				t.Fatal("taxonomy sitemap contains a relative or invalid loc")
			}
			if u.Query().Get("category_name") == "hidden-category" || u.Query().Get("tag") == "hidden-tag" {
				t.Fatal("taxonomy sitemap exposes a private-only term")
			}
			if u.Query().Get(test.key) == test.value {
				found[u.Path] = true
			}
		}
		for _, lang := range publicLanguages() {
			if !found[publicLanguagePrefix(lang)+"/"] {
				t.Fatalf("taxonomy sitemap omits new public term language %s", lang)
			}
		}
	}
}

func taxonomyTestApp(t *testing.T, settings SiteSettings, posts []Post) *App {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "taxonomy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	for i := range posts {
		if err = store.saveLocked(context.Background(), &posts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return &App{Store: store, Config: Config{MediaDir: t.TempDir()}}
}

func assertCurrentTaxonomyLink(t *testing.T, body, selector, selected string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			href, current := "", ""
			for _, attr := range node.Attr {
				if attr.Key == "href" {
					href = attr.Val
				}
				if attr.Key == "aria-current" {
					current = attr.Val
				}
			}
			if u, err := url.Parse(href); err == nil && current == "page" && publicTermMatches(u.Query().Get(selector), selected) {
				found = true
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	if !found {
		t.Fatalf("selected %s lacks aria-current=page", selector)
	}
}

func TestPublicTaxonomyHTTPOriginalQueriesAndKnownEmptyArchives(t *testing.T) {
	s, posts := taxonomyTestData()
	a := taxonomyTestApp(t, s, posts)
	tag, _ := url.Parse(s.Tags[0].URL)
	for _, lang := range publicLanguages() {
		prefix := publicLanguagePrefix(lang)
		for _, route := range []string{"/?" + tag.RawQuery, "/?category_name=" + s.Categories[0].Slug, "/?cat=5", "/?cat=13&" + tag.RawQuery} {
			w := publicTestRequest(a, prefix+route)
			if w.Code != 200 {
				t.Fatalf("%s%s expected 200, got %d", prefix, route, w.Code)
			}
			parsed, _ := url.Parse(route)
			canonical := publicArchiveURL(s, lang, parsed.Query())
			if !strings.Contains(w.Body.String(), `rel="canonical" href="`+stdhtml.EscapeString(canonical)+`"`) {
				t.Fatalf("%s%s archive canonical changed", prefix, route)
			}
			if selected := parsed.Query().Get("tag"); selected != "" {
				assertCurrentTaxonomyLink(t, w.Body.String(), "tag", selected)
			}
			if cat := parsed.Query().Get("cat"); cat != "" {
				assertCurrentTaxonomyLink(t, w.Body.String(), "cat", cat)
			}
			if parsed.Query().Get("category_name") != "" {
				assertCurrentTaxonomyLink(t, w.Body.String(), "cat", "3")
			}
			if strings.Contains(w.Body.String(), "私密设置标签") || strings.Contains(w.Body.String(), "仅私密文章的标签") {
				t.Fatal("private taxonomy leaked in HTTP HTML")
			}
		}
		if w := publicTestRequest(a, prefix+"/?tag=private-only"); w.Code != 404 {
			t.Fatal("private-only tag archive must be 404")
		}
	}
}

func TestOriginalWordPressTaxonomyURLsInAllLanguages(t *testing.T) {
	// The private migration archive stays outside Git. When available, exercise
	// its actual IDs, original URL escape case and original WordPress slugs.
	raw, err := os.ReadFile(filepath.Join("..", "..", "migration", "wordpress.json"))
	if os.IsNotExist(err) {
		t.Skip("private WordPress migration snapshot unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	var source Export
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	for i := range source.Posts {
		if source.Posts[i].Status == "publish" && source.Posts[i].Type == "post" {
			// Synthetic translations isolate taxonomy routing from translation
			// content; source terms and original Chinese canonical remain exact.
			source.Posts[i].Translations = make(map[string]Translation)
			for _, lang := range translationLanguages() {
				source.Posts[i].Translations[lang] = Translation{Title: source.Posts[i].Title, ContentHTML: source.Posts[i].ContentHTML, SourceHash: SourceHash(source.Posts[i]), Status: "publish"}
			}
		}
	}
	a := taxonomyTestApp(t, source.Settings, source.Posts)
	publicTags := publicFilterTagTerms(source.Settings, source.Posts)
	if len(source.Settings.Categories) != 8 || len(publicTags) != 8 {
		t.Fatal("migration taxonomy inventory changed; inspect the snapshot")
	}
	checks := 0
	for _, lang := range publicLanguages() {
		prefix := publicLanguagePrefix(lang)
		for _, term := range append(append([]Term{}, source.Settings.Categories...), publicTags...) {
			u, err := url.Parse(term.URL)
			if err != nil {
				t.Fatal(err)
			}
			path := prefix + u.Path + "?" + u.RawQuery
			if w := publicTestRequest(a, path); w.Code != 200 {
				t.Fatalf("original taxonomy id=%d lang=%s expected 200, got %d", term.ID, lang, w.Code)
			}
			checks++
		}
		for _, category := range source.Settings.Categories {
			path := prefix + "/?category_name=" + category.Slug
			if w := publicTestRequest(a, path); w.Code != 200 {
				t.Fatalf("original category_name id=%d lang=%s expected 200, got %d", category.ID, lang, w.Code)
			}
			checks++
		}
	}
	t.Logf("verified %d original taxonomy and category_name routes across four languages", checks)
}
