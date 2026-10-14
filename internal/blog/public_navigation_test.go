package blog

import (
	"net/url"
	"strings"
	"testing"
)

func TestReadingNavigationPreservesURLAndHidesUnavailableArticles(t *testing.T) {
	a := publicTestApp(t)
	w := publicTestRequest(a, "/?p=1447")
	body := w.Body.String()
	start := strings.Index(body, `<nav class="article-toolbar"`)
	if start < 0 {
		t.Fatal("reading toolbar missing")
	}
	end := strings.Index(body[start:], `</nav>`)
	if end < 0 {
		t.Fatal("reading toolbar missing")
	}
	toolbar := body[start : start+end]
	for _, want := range []string{`data-back-link href="/?paged=1"`, `data-previous href="/?p=1875"`, `data-next href="/?p=10"`, `data-scroll-top`} {
		if !strings.Contains(toolbar, want) {
			t.Errorf("toolbar missing %s: %s", want, toolbar)
		}
	}
	if strings.Contains(toolbar, "?p=100") || strings.Contains(toolbar, "attachment_id") {
		t.Fatal("nonpublic article in navigation")
	}
	if !strings.Contains(body, `canonical" href="https://www.ganwen.nl/?p=1447"`) {
		t.Fatal("canonical changed")
	}
	w = publicTestRequest(a, "/en/?p=1447")
	if strings.Contains(w.Body.String(), "data-previous href") || strings.Contains(w.Body.String(), "data-next href") {
		t.Fatal("stale or review translations in neighbors")
	}
}

func TestArchivePaginationRetainsSelectionAndUsesFullWidth(t *testing.T) {
	a := publicTestApp(t)
	w := publicLayoutTestRequest(a, "/?cat=13&paged=2", "classic")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{`class="site-main archive-page"`, `class="post-grid single-result"`, `class="archive-card post-preview"`, `name="cat" value="13"`, `name="paged" min="1" max="2" value="2"`, `href="/?cat=13&amp;paged=1"`, `aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(publicLayoutTestRequest(a, "/", "classic").Body.String(), `<nav class="pagination"`) {
		t.Fatal("category overview mixed with global pagination")
	}
	all := publicLayoutTestRequest(a, "/?paged=2", "classic").Body.String()
	if !strings.Contains(all, `data-previous href="/?paged=1"`) {
		t.Fatal("all articles page two returned to category overview")
	}
	links := publicPaginationLinks(SiteSettings{BaseURL: "https://www.ganwen.nl"}, "es", url.Values{"cat": {"13"}, "s": {"texto"}}, 10, 20)
	for _, link := range links {
		if link.URL != "" && (!strings.Contains(link.URL, "/es/") || !strings.Contains(link.URL, "cat=13") || !strings.Contains(link.URL, "s=texto")) {
			t.Fatal(link)
		}
	}
}
