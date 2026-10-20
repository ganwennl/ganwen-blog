package blog

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func sidebarHTTPModules(selected ...string) *[]string {
	modules := append([]string{}, selected...)
	return &modules
}

func assertSidebarHTTPModules(t *testing.T, body string, wanted ...string) {
	t.Helper()
	asides := publicMetadataNodes(t, body, "aside", "blog-sidebar")
	if len(asides) > 1 || len(wanted) > 0 && len(asides) != 1 {
		t.Fatalf("sidebar count %d for modules %v", len(asides), wanted)
	}
	expected := map[string]bool{}
	for _, module := range wanted {
		expected[module] = true
	}
	for module, selector := range map[string]struct{ element, class string }{
		"search":     {"section", "sidebar-search-section"},
		"popular":    {"section", "sidebar-popular"},
		"stats":      {"dl", "site-stats"},
		"archives":   {"section", "sidebar-archives"},
		"categories": {"nav", "category-tabs"},
		"tags":       {"nav", "tag-tabs"},
	} {
		count := 0
		var visit func(*html.Node)
		visit = func(node *html.Node) {
			if node.Type == html.ElementNode && node.Data == selector.element && strings.Contains(" "+publicMetadataAttr(node, "class")+" ", " "+selector.class+" ") {
				count++
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				visit(child)
			}
		}
		for _, aside := range asides {
			visit(aside)
		}
		if count > 1 || (count == 1) != expected[module] {
			t.Errorf("sidebar module %s count %d, expected visible=%t", module, count, expected[module])
		}
	}
}

func TestPublicSidebarModuleCombinationsControlHTTPTemplates(t *testing.T) {
	app := publicTestApp(t)
	settings, err := app.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		modules     *[]string
		hideStats   bool
		wanted      []string
		mobileTopic bool
	}{
		{"legacy defaults", nil, false, []string{"search", "popular", "stats", "archives", "categories", "tags"}, true},
		{"search and dates", sidebarHTTPModules("search", "archives"), false, []string{"search", "archives"}, false},
		{"popular and stats", sidebarHTTPModules("popular", "stats"), false, []string{"popular", "stats"}, false},
		{"categories without tags", sidebarHTTPModules("categories"), false, []string{"categories"}, true},
		{"tags without categories", sidebarHTTPModules("tags"), false, []string{"tags"}, false},
		{"legacy stats switch still hides stats", sidebarHTTPModules("search", "stats"), true, []string{"search"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings.SidebarModules = test.modules
			enabled := !test.hideStats
			settings.PublicStatsEnabled = &enabled
			if err := app.Store.SaveSettings(context.Background(), settings); err != nil {
				t.Fatal(err)
			}
			for _, layout := range []string{"journal", "classic"} {
				for _, target := range []string{"/", "/?paged=1"} {
					response := publicLayoutTestRequest(app, target, layout)
					if response.Code != http.StatusOK {
						t.Fatalf("%s %s returned HTTP %d", layout, target, response.Code)
					}
					body := response.Body.String()
					assertSidebarHTTPModules(t, body, test.wanted...)
					mobileTopics := publicMetadataNodes(t, body, "details", "journal-mobile-topics")
					wantMobile := layout == "journal" && test.mobileTopic
					if len(mobileTopics) > 1 || (len(mobileTopics) == 1) != wantMobile {
						t.Fatalf("%s %s mobile categories ignored the category module: %d", layout, target, len(mobileTopics))
					}
				}
			}
		})
	}
}

func TestPublicSidebarAllModulesDisabledRemovesReservedColumn(t *testing.T) {
	app := publicTestApp(t)
	settings, err := app.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.SidebarModules = sidebarHTTPModules()
	settings.ArticleSidebarEnabled = true
	if err := app.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	for _, layout := range []string{"journal", "classic"} {
		for _, target := range []string{"/", "/?m=2025", "/?p=1447", "/?page_id=2"} {
			response := publicLayoutTestRequest(app, target, layout)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s returned HTTP %d", layout, target, response.Code)
			}
			body := response.Body.String()
			if len(publicMetadataNodes(t, body, "aside", "blog-sidebar")) != 0 || len(publicMetadataNodes(t, body, "details", "journal-mobile-topics")) != 0 {
				t.Fatalf("%s %s retained a disabled sidebar/mobile navigation", layout, target)
			}
			if strings.Contains(target, "p=") || strings.Contains(target, "page_id=") {
				if len(publicMetadataNodes(t, body, "main", "reading-full-width")) != 1 || len(publicMetadataNodes(t, body, "div", "page-with-sidebar")) != 0 {
					t.Fatalf("%s %s still reserves an article sidebar column", layout, target)
				}
			} else if len(publicMetadataNodes(t, body, "div", "without-sidebar")) != 1 {
				t.Fatalf("%s %s does not expand the list into the available width", layout, target)
			}
			assertSidebarHTTPModules(t, body)
		}
	}
}

func TestPublicSidebarModuleSelectionPreservesArticleGateAndIndependentAds(t *testing.T) {
	app := publicTestApp(t)
	settings, err := app.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name           string
		modules        *[]string
		articleEnabled bool
		adsEnabled     bool
		homeSidebar    bool
		articleSidebar bool
	}{
		{"selected module does not enable article sidebar", sidebarHTTPModules("search"), false, false, true, false},
		{"selected module respects article opt-in", sidebarHTTPModules("search"), true, false, true, true},
		{"ads alone keep home sidebar", sidebarHTTPModules(), false, true, true, false},
		{"ads alone respect article opt-in", sidebarHTTPModules(), true, true, true, true},
		{"disabling last ad removes sidebar", sidebarHTTPModules(), true, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings.SidebarModules = test.modules
			settings.ArticleSidebarEnabled = test.articleEnabled
			settings.SidebarAds = sidebarAdsFixture()
			settings.SidebarAds.Enabled = test.adsEnabled
			if err := app.Store.SaveSettings(context.Background(), settings); err != nil {
				t.Fatal(err)
			}
			for _, layout := range []string{"journal", "classic"} {
				for _, target := range []string{"/", "/?p=1447"} {
					response := publicLayoutTestRequest(app, target, layout)
					if response.Code != http.StatusOK {
						t.Fatalf("%s %s returned HTTP %d", layout, target, response.Code)
					}
					body := response.Body.String()
					wantSidebar := test.homeSidebar
					if target != "/" {
						wantSidebar = test.articleSidebar
					}
					if count := len(publicMetadataNodes(t, body, "aside", "blog-sidebar")); count > 1 || (count == 1) != wantSidebar {
						t.Fatalf("%s %s sidebar did not respect module/ad/article settings", layout, target)
					}
					wantAd := wantSidebar && test.adsEnabled
					if count := len(publicMetadataNodes(t, body, "section", "sidebar-ad")); count > 1 || (count == 1) != wantAd {
						t.Fatalf("%s %s advertisement did not retain its independent switch", layout, target)
					}
					if wantSidebar && len(*test.modules) > 0 {
						assertSidebarHTTPModules(t, body, "search")
					} else {
						assertSidebarHTTPModules(t, body)
					}
					for _, cookie := range response.Result().Cookies() {
						if !wantAd && cookie.Name == publicSidebarAdCookieName {
							t.Fatal("hidden advertisement advanced its refresh sequence")
						}
					}
				}
			}
		})
	}
}

func TestPublicSidebarEmptyTagsDoNotReserveColumnOrResetSelection(t *testing.T) {
	app := publicTestApp(t)
	ctx := context.Background()
	settings, err := app.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.SidebarModules = sidebarHTTPModules("tags")
	settings.ArticleSidebarEnabled = true
	if err := app.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.UpdatePost(ctx, 1447, func(post *Post) error { post.Tags = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := app.Store.UpdatePost(ctx, 100, func(post *Post) error { post.Tags = settings.Tags; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, layout := range []string{"journal", "classic"} {
		for _, target := range []string{"/", "/?paged=1", "/?p=1447"} {
			response := publicLayoutTestRequest(app, target, layout)
			if response.Code != http.StatusOK {
				t.Fatalf("%s %s returned HTTP %d", layout, target, response.Code)
			}
			body := response.Body.String()
			if len(publicMetadataNodes(t, body, "aside", "blog-sidebar")) != 0 || len(publicMetadataNodes(t, body, "details", "journal-mobile-topics")) != 0 {
				t.Fatalf("%s %s rendered an empty/private-only tag sidebar", layout, target)
			}
			if target == "/?p=1447" {
				if len(publicMetadataNodes(t, body, "main", "reading-full-width")) != 1 {
					t.Fatal("empty tags retained the article sidebar column")
				}
			} else if len(publicMetadataNodes(t, body, "div", "without-sidebar")) != 1 {
				t.Fatal("empty tags retained the list sidebar column")
			}
		}
	}
	persisted, err := app.Store.Settings(ctx)
	if err != nil || persisted.SidebarModules == nil || len(*persisted.SidebarModules) != 1 || (*persisted.SidebarModules)[0] != "tags" {
		t.Fatal("rendering empty tags changed the saved module selection", err)
	}
	if err := app.Store.UpdatePost(ctx, 1447, func(post *Post) error { post.Tags = settings.Tags; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, layout := range []string{"journal", "classic"} {
		response := publicLayoutTestRequest(app, "/", layout)
		if response.Code != http.StatusOK {
			t.Fatalf("%s public tag returned HTTP %d", layout, response.Code)
		}
		assertSidebarHTTPModules(t, response.Body.String(), "tags")
	}
}
