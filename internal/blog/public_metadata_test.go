package blog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func publicMetadataFixture(t *testing.T) (*App, SiteSettings, Post) {
	t.Helper()
	s, posts := taxonomyTestData()
	s.Description = "甘文的个人博客"
	names := []string{"习近平", "偷渡出境", "夏津公安", "夏津县", "护照激活", "拆墙运动", "甘文", "政治"}
	tags := []Term{s.Tags[0]}
	for i, name := range names[1:] {
		tags = append(tags, Term{ID: int64(100 + i), Name: name, Slug: name, Taxonomy: "post_tag", URL: s.BaseURL + "/?tag=" + url.QueryEscape(name)})
	}
	s.Tags = tags
	posts[0].Tags = append([]Term{}, tags...)
	posts[0].Title = "中文源文章"
	posts[0].ContentHTML = "<p>中文源正文</p>"
	for _, lang := range translationLanguages() {
		posts[0].Translations[lang] = Translation{Title: "Translated article " + lang, ContentHTML: "<p>Translated content " + lang + "</p>", SourceHash: SourceHash(posts[0]), Status: "publish"}
	}
	posts[1].Tags = tags[len(tags)-1:]
	return taxonomyTestApp(t, s, posts), s, posts[0]
}

func publicMetadataClient(t *testing.T, a *App) func(string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(a.handlePublic))
	t.Cleanup(server.Close)
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return func(path string) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "zh"})
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s returned HTTP %d", path, response.StatusCode)
		}
		return string(body)
	}
}

func publicMetadataNodes(t *testing.T, body, element, class string) []*html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var nodes []*html.Node
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && (element == "" || node.Data == element) && (class == "" || strings.Contains(" "+publicMetadataAttr(node, "class")+" ", " "+class+" ")) {
			nodes = append(nodes, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	return nodes
}

func publicMetadataAttr(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func publicMetadataText(node *html.Node) string {
	if node.Type == html.TextNode {
		return node.Data
	}
	var text strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		text.WriteString(publicMetadataText(child))
	}
	return text.String()
}

func publicMetadataOneText(t *testing.T, body, element, class string) string {
	t.Helper()
	nodes := publicMetadataNodes(t, body, element, class)
	if len(nodes) != 1 {
		t.Fatalf("expected one %s.%s, got %d", element, class, len(nodes))
	}
	return publicMetadataText(nodes[0])
}

func publicMetadataDescription(t *testing.T, body string) string {
	t.Helper()
	for _, node := range publicMetadataNodes(t, body, "meta", "") {
		if publicMetadataAttr(node, "name") == "description" {
			return publicMetadataAttr(node, "content")
		}
	}
	t.Fatal("meta description is missing")
	return ""
}

func TestPublicMetadataLocalizedHTTP(t *testing.T) {
	a, settings, post := publicMetadataFixture(t)
	get := publicMetadataClient(t, a)
	for _, test := range []struct{ lang, title, description string }{
		{"zh", "甘文", "甘文的个人博客"},
		{"en", "Ganwen", "Wen Gan's personal blog"},
		{"nl", "Ganwen", "De persoonlijke blog van Wen Gan"},
		{"es", "Ganwen", "El blog personal de Wen Gan"},
	} {
		t.Run(test.lang, func(t *testing.T) {
			prefix := publicLanguagePrefix(test.lang)
			home := get(prefix + "/")
			if got := publicMetadataOneText(t, home, "title", ""); got != test.title {
				t.Fatalf("homepage title = %q, want %q", got, test.title)
			}
			if got := publicMetadataDescription(t, home); got != test.description {
				t.Fatalf("homepage meta description = %q", got)
			}
			heading := publicMetadataOneText(t, home, "", "page-heading")
			if !strings.Contains(heading, test.description) || !strings.Contains(publicMetadataOneText(t, home, "footer", "site-footer"), "© "+test.title) {
				t.Fatal("homepage visible description or footer uses source settings")
			}
			labels := map[string]string{}
			for _, node := range publicMetadataNodes(t, home, "a", "") {
				u, err := url.Parse(publicMetadataAttr(node, "href"))
				if err == nil && u.Query().Get("tag") != "" {
					labels[u.Query().Get("tag")] = publicMetadataText(node)
				}
			}
			article := get(prefix + "/?p=1")
			articleTags := publicMetadataOneText(t, article, "footer", "post-tags")
			for i, term := range settings.Tags {
				slug := term.Name
				label, found := labels[slug]
				if !found || !strings.HasPrefix(label, "#") {
					t.Fatalf("tag %q lost its original query slug or label", slug)
				}
				if test.lang == "zh" && label != "#"+term.Name {
					t.Fatalf("Chinese tag changed: %q", label)
				}
				if test.lang != "zh" && strings.Contains(label, term.Name) {
					t.Fatalf("untranslated %s tag %q", test.lang, term.Name)
				}
				if test.lang == "en" {
					want := []string{"Xi Jinping", "Illegal departure", "Xiajin Police", "Xiajin County", "Passport activation", "Tear Down the Wall movement", "Wen Gan", "Politics"}[i]
					if label != "#"+want {
						t.Fatalf("English tag %q = %q", term.Name, label)
					}
				}
				archive := get(prefix + "/?tag=" + url.QueryEscape(slug))
				if got := publicMetadataOneText(t, archive, "h1", ""); got != strings.TrimPrefix(label, "#") {
					t.Fatalf("tag archive heading %q differs from navigation %q", got, label)
				}
				assertCurrentTaxonomyLink(t, archive, "tag", slug)
				if !strings.Contains(articleTags, label) {
					t.Fatalf("article tag %q differs from navigation", term.Name)
				}
				if test.lang == "zh" {
					original, err := url.Parse(term.URL)
					if err != nil || !strings.Contains(home, `href="`+original.RequestURI()+`"`) {
						t.Fatalf("original WordPress URL changed for %q", term.Name)
					}
				}
			}
			articleTitle := post.Title
			if test.lang != "zh" {
				articleTitle = post.Translations[test.lang].Title
			}
			if got := publicMetadataOneText(t, article, "title", ""); got != articleTitle+" | "+test.title {
				t.Fatalf("article SEO site name = %q", got)
			}
		})
	}
	stored, err := a.Store.Settings(context.Background())
	if err != nil || !reflect.DeepEqual(stored, settings) {
		t.Fatalf("rendering changed stored site settings: %v", err)
	}
	storedPost, err := a.Store.GetPost(context.Background(), post.ID)
	if err != nil || !reflect.DeepEqual(storedPost.Tags, post.Tags) || storedPost.Title != post.Title {
		t.Fatalf("rendering changed stored source post: %v", err)
	}
}

func TestPublicMetadataUnknownValuesPreservedHTTP(t *testing.T) {
	a, settings, post := publicMetadataFixture(t)
	settings.Title, settings.Description = "自定义站名", "自定义站点简介"
	unknown := Term{ID: 300, Name: "自定义新标签", Slug: "original-custom-slug", Taxonomy: "post_tag", URL: settings.BaseURL + "/?tag=original-custom-slug"}
	settings.Tags = append(settings.Tags, unknown)
	post.Tags = append(post.Tags, unknown)
	if err := a.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.saveLocked(context.Background(), &post); err != nil {
		t.Fatal(err)
	}
	get := publicMetadataClient(t, a)
	for _, lang := range publicLanguages() {
		prefix := publicLanguagePrefix(lang)
		home := get(prefix + "/")
		if publicMetadataOneText(t, home, "title", "") != settings.Title || publicMetadataDescription(t, home) != settings.Description || !strings.Contains(publicMetadataOneText(t, home, "", "page-heading"), settings.Description) {
			t.Fatalf("%s rewrote unknown site settings", lang)
		}
		archive := get(prefix + "/?tag=" + unknown.Slug)
		if publicMetadataOneText(t, archive, "h1", "") != unknown.Name || !strings.Contains(publicMetadataOneText(t, get(prefix+"/?p=1"), "footer", "post-tags"), "#"+unknown.Name) {
			t.Fatalf("%s rewrote an unknown tag", lang)
		}
	}
}
