package blog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func publicSidebarLayoutApp(t *testing.T) *App {
	t.Helper()
	seed := publicTestApp(t)
	post, err := seed.Store.GetPost(context.Background(), 1447)
	if err != nil {
		t.Fatal(err)
	}
	post.Translations["ar"] = Translation{Title: "مقال وصورة", ContentHTML: "<p>المحتوى الأصلي</p>", SourceHash: SourceHash(post), Status: "publish"}
	if err := seed.Store.saveLocked(context.Background(), &post); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(seed.Store, Config{DataDir: t.TempDir(), MediaDir: seed.Config.MediaDir, BaseURL: "https://www.ganwen.nl", Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	app.Analytics.now = func() time.Time { return analyticsTestDate }
	enableArticleSidebar(t, app)
	return app
}

func publicSidebarLayoutDescendants(node *html.Node, element, class string) []*html.Node {
	var nodes []*html.Node
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.ElementNode && (element == "" || current.Data == element) && (class == "" || strings.Contains(" "+publicMetadataAttr(current, "class")+" ", " "+class+" ")) {
			nodes = append(nodes, current)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return nodes
}

func publicSidebarLayoutHasAttr(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}
	return false
}

func publicSidebarLayoutRender(t *testing.T, app *App, target, layout string) (string, *html.Node) {
	t.Helper()
	response := publicLayoutTestRequest(app, target, layout)
	if response.Code != http.StatusOK {
		t.Fatalf("%s/%s returned HTTP %d", layout, target, response.Code)
	}
	body := response.Body.String()
	asides := publicMetadataNodes(t, body, "aside", "blog-sidebar")
	if len(asides) != 1 {
		t.Fatalf("%s/%s rendered %d shared sidebars", layout, target, len(asides))
	}
	return body, asides[0]
}

func publicSidebarLayoutAssertNoAds(t *testing.T, sidebar *html.Node) {
	t.Helper()
	for _, class := range []string{"sidebar-ad", "ad-card-heading", "ad-card-image", "ad-card-body", "ad-card-button", "ad-card-footer"} {
		if len(publicSidebarLayoutDescendants(sidebar, "", class)) != 0 {
			t.Fatalf("disabled or invalid advertising rendered %s DOM", class)
		}
	}
	for _, node := range publicSidebarLayoutDescendants(sidebar, "", "") {
		if publicSidebarLayoutHasAttr(node, "data-ad-carousel") || publicSidebarLayoutHasAttr(node, "data-ad-slide") {
			t.Fatal("disabled or invalid advertising rendered carousel DOM")
		}
	}
}

func TestPublicSidebarLayoutDefaultAndLocalizedSearch(t *testing.T) {
	app := publicSidebarLayoutApp(t)
	settings, err := app.Store.Settings(context.Background())
	if err != nil || settings.SidebarAds.Enabled {
		t.Fatalf("default advertising settings: %+v, %v", settings.SidebarAds, err)
	}
	for _, language := range []struct{ prefix, heading, placeholder, direction string }{
		{"", "搜索文章", "输入关键词", "ltr"},
		{"/en", "Search articles", "Enter a keyword", "ltr"},
		{"/ar", "البحث في المقالات", "أدخل كلمة بحث", "rtl"},
	} {
		for _, layout := range []string{"journal", "classic"} {
			for _, suffix := range []string{"/", "/?p=1447"} {
				target := language.prefix + suffix
				t.Run(layout+target, func(t *testing.T) {
					body, sidebar := publicSidebarLayoutRender(t, app, target, layout)
					publicSidebarLayoutAssertNoAds(t, sidebar)
					roots := publicMetadataNodes(t, body, "html", "")
					if len(roots) != 1 || publicMetadataAttr(roots[0], "data-layout") != layout || publicMetadataAttr(roots[0], "dir") != language.direction {
						t.Fatal("layout or language direction was not honored")
					}
					forms := publicSidebarLayoutDescendants(sidebar, "form", "sidebar-search")
					if len(forms) != 1 || publicMetadataAttr(forms[0], "action") != language.prefix+"/" || publicMetadataAttr(forms[0], "method") != "get" {
						t.Fatal("sidebar search did not target the current language homepage")
					}
					inputs := publicSidebarLayoutDescendants(forms[0], "input", "")
					if len(inputs) != 1 || publicMetadataAttr(inputs[0], "name") != "s" || publicMetadataAttr(inputs[0], "type") != "search" || publicMetadataAttr(inputs[0], "placeholder") != language.placeholder {
						t.Fatal("sidebar search field is missing or not localized")
					}
					labels := publicSidebarLayoutDescendants(forms[0], "label", "")
					if len(labels) != 1 || publicMetadataText(labels[0]) != language.heading || publicMetadataAttr(labels[0], "for") != publicMetadataAttr(inputs[0], "id") {
						t.Fatal("sidebar search has no localized associated label")
					}
					if len(publicSidebarLayoutDescendants(sidebar, "ol", "popular-list")) != 1 || len(publicSidebarLayoutDescendants(sidebar, "dl", "site-stats")) != 1 {
						t.Fatal("sidebar ranking or statistics is missing")
					}
				})
			}
		}
	}
}

func TestPublicSidebarLayoutRendersRecordedAnalytics(t *testing.T) {
	app := publicSidebarLayoutApp(t)
	app.Analytics.now = func() time.Time { return analyticsTestDate.AddDate(0, 0, -1) }
	firstEvent := analyticsPayloadFor(1001, "/?p=1447")
	if response := analyticsEventRequest(app, firstEvent, nil); response.Code != http.StatusNoContent {
		t.Fatalf("yesterday's analytics event returned HTTP %d", response.Code)
	}
	app.Analytics.now = func() time.Time { return analyticsTestDate }
	for index, path := range []string{"/?p=1447", "/en/?p=1447", "/"} {
		payload := analyticsPayloadFor(1002+index, path)
		if strings.HasPrefix(path, "/en/") {
			payload.Language = "en"
		}
		if response := analyticsEventRequest(app, payload, nil); response.Code != http.StatusNoContent {
			t.Fatalf("analytics event %s returned HTTP %d", path, response.Code)
		}
	}
	report, err := app.Store.AnalyticsReport(context.Background(), analyticsTestDate.AddDate(0, 0, -1).Truncate(24*time.Hour), analyticsTestDate.Truncate(24*time.Hour))
	if err != nil || report.PV != 4 {
		t.Fatalf("analytics did not persist four real visits: PV=%d, %v", report.PV, err)
	}
	raw, err := app.Store.publicSidebarAnalytics(context.Background(), analyticsTestDate)
	if err != nil || raw.stats != (publicSidebarStats{TodayViews: 3, YearViews: 4, HistoricalViews: 4}) {
		t.Fatalf("recorded total windows changed: %+v, %v", raw.stats, err)
	}
	parseDisplayViews := func(t *testing.T, value string, minimum int64) int64 {
		t.Helper()
		views, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || views < minimum {
			t.Fatalf("front-end view display %q must be a number of at least %d", value, minimum)
		}
		return views
	}
	for _, language := range []struct{ prefix, posts, title string }{
		{"", "3", "保留原文 & 图片"},
		{"/en", "1", "Original & image"},
		{"/ar", "1", "مقال وصورة"},
	} {
		for _, layout := range []string{"journal", "classic"} {
			for _, suffix := range []string{"/", "/?p=1447"} {
				t.Run(layout+language.prefix+suffix, func(t *testing.T) {
					body, sidebar := publicSidebarLayoutRender(t, app, language.prefix+suffix, layout)
					for _, private := range []string{firstEvent.EventID, firstEvent.VisitorID, firstEvent.SessionID} {
						if strings.Contains(body, private) {
							t.Fatal("front-end page exposed a raw analytics identifier")
						}
					}
					stats := publicSidebarLayoutDescendants(sidebar, "dl", "site-stats")
					if len(stats) != 1 {
						t.Fatal("missing statistics")
					}
					var values []string
					for _, field := range publicSidebarLayoutDescendants(stats[0], "dd", "") {
						values = append(values, strings.TrimSpace(publicMetadataText(field)))
					}
					if len(values) != 4 || values[0] != language.posts {
						t.Fatalf("SSR statistics %v must keep the actual article count and three visit periods", values)
					}
					today := parseDisplayViews(t, values[1], 0)
					year := parseDisplayViews(t, values[2], 1007)
					historical := parseDisplayViews(t, values[3], 1007)
					if today != 3+publicSidebarDisplayDailyGrowth(analyticsTestDate, 1000) || year < today || historical < year {
						t.Fatalf("front-end view periods must satisfy history >= year >= today: %v", values)
					}
					items := publicSidebarLayoutDescendants(sidebar, "li", "popular-item")
					if len(items) != 1 {
						t.Fatalf("recorded article produced %d ranking rows", len(items))
					}
					links := publicSidebarLayoutDescendants(items[0], "a", "popular-link")
					counts := publicSidebarLayoutDescendants(items[0], "bdi", "")
					if len(links) != 1 || publicMetadataAttr(links[0], "href") != language.prefix+"/?p=1447" || !strings.Contains(publicMetadataText(links[0]), language.title) || len(counts) != 1 {
						t.Fatal("popular article title, localized link or display count is missing")
					}
					parseDisplayViews(t, publicMetadataText(counts[0]), 1000)
				})
			}
		}
	}
	report, err = app.Store.AnalyticsReport(context.Background(), analyticsTestDate.AddDate(0, 0, -1).Truncate(24*time.Hour), analyticsTestDate.Truncate(24*time.Hour))
	var actualArticleViews int64
	for _, page := range report.TopPages {
		if page.PostID == 1447 {
			actualArticleViews += page.PV
		}
	}
	if err != nil || report.PV != 4 || actualArticleViews != 3 {
		t.Fatalf("front-end rendering changed recorded analytics: total=%d/article=%d, %v", report.PV, actualArticleViews, err)
	}
}

func TestPublicSidebarLayoutAdvertisingEnabledThenDisabled(t *testing.T) {
	app := publicSidebarLayoutApp(t)
	ctx := context.Background()
	settings, err := app.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.SidebarAds = sidebarAdsFixture()
	settings.SidebarAds.Items[0].Description = "第一条广告说明，可查看相关优惠。"
	settings.SidebarAds.Items[0].ButtonLabel = "访问优惠"
	settings.SidebarAds.Items[0].Sponsor = "合作伙伴"
	settings.SidebarAds.Items[1].Description = "第二条广告说明。"
	if err := app.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	client := server.Client()
	client.Jar, err = cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, layout := range []string{"journal", "classic"} {
		for _, target := range []string{"/", "/?p=1447"} {
			t.Run(layout+target, func(t *testing.T) {
				var previousTitle string
				for refresh := 0; refresh < 2; refresh++ {
					request, err := http.NewRequest(http.MethodGet, server.URL+target, nil)
					if err != nil {
						t.Fatal(err)
					}
					request.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "zh"})
					request.AddCookie(&http.Cookie{Name: "ganwen.layout.v1", Value: layout})
					response, err := client.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					raw, readErr := io.ReadAll(response.Body)
					response.Body.Close()
					if readErr != nil || response.StatusCode != http.StatusOK {
						t.Fatalf("refresh returned HTTP %d: %v", response.StatusCode, readErr)
					}
					if !strings.Contains(response.Header.Get("Cache-Control"), "private") {
						t.Fatal("visitor's advertising choice can leak through a shared cache")
					}
					body := string(raw)
					asides := publicMetadataNodes(t, body, "aside", "blog-sidebar")
					sections := publicMetadataNodes(t, body, "section", "sidebar-ad")
					if len(asides) != 1 || len(sections) != 1 {
						t.Fatal("refresh must render one sidebar containing one advertising card")
					}
					headings := publicSidebarLayoutDescendants(sections[0], "h2", "ad-card-heading")
					if len(headings) != 1 || len(publicSidebarLayoutDescendants(headings[0], "a", "")) != 1 {
						t.Fatal("advertising card must have one linked heading")
					}
					title := publicMetadataText(headings[0])
					if refresh > 0 && title == previousTitle {
						t.Fatal("refresh with the response cookie repeated the same card")
					}
					previousTitle = title
					var ad *SidebarAd
					for index := range settings.SidebarAds.Items {
						if settings.SidebarAds.Items[index].Title == title {
							ad = &settings.SidebarAds.Items[index]
						}
					}
					if ad == nil {
						t.Fatalf("rendered unknown advertising title %q", title)
					}
					links := publicSidebarLayoutDescendants(sections[0], "a", "")
					if len(links) != 3 {
						t.Fatalf("card must link its heading, image and button; got %d links", len(links))
					}
					for _, link := range links {
						if publicMetadataAttr(link, "href") != ad.TargetURL || publicMetadataAttr(link, "target") != "_blank" || publicSidebarLayoutHasAttr(link, "hidden") {
							t.Fatal("advertising card link or visibility is wrong")
						}
						rel := " " + publicMetadataAttr(link, "rel") + " "
						for _, value := range []string{"sponsored", "noopener", "noreferrer"} {
							if !strings.Contains(rel, " "+value+" ") {
								t.Fatalf("advertising card omitted rel=%s", value)
							}
						}
					}
					images := publicSidebarLayoutDescendants(sections[0], "img", "")
					imageLinks := publicSidebarLayoutDescendants(sections[0], "a", "ad-card-image")
					if len(imageLinks) != 1 || publicMetadataAttr(imageLinks[0], "aria-label") != ad.Title || len(images) != 1 || publicMetadataAttr(images[0], "src") != ad.ImageURL || publicMetadataAttr(images[0], "referrerpolicy") != "no-referrer" || publicMetadataAttr(images[0], "width") != "640" || publicMetadataAttr(images[0], "height") != "640" {
						t.Fatal("advertising card image, square dimensions or referrer policy is wrong")
					}
					descriptions := publicSidebarLayoutDescendants(sections[0], "p", "ad-description")
					if len(descriptions) != 1 || publicMetadataText(descriptions[0]) != ad.Description {
						t.Fatal("advertising description is missing or incorrect")
					}
					buttons := publicSidebarLayoutDescendants(sections[0], "a", "ad-card-button")
					buttonLabel := ad.ButtonLabel
					if buttonLabel == "" {
						buttonLabel = "查看详情"
					}
					if len(buttons) != 1 || publicMetadataText(buttons[0]) != buttonLabel {
						t.Fatal("advertising button label or fallback is incorrect")
					}
					promoted := publicSidebarLayoutDescendants(sections[0], "span", "ad-promoted")
					sponsors := publicSidebarLayoutDescendants(sections[0], "span", "ad-sponsor")
					footers := publicSidebarLayoutDescendants(sections[0], "footer", "ad-card-footer")
					if len(footers) != 1 || !strings.Contains(publicMetadataText(footers[0]), "广告") || len(promoted) != 1 || (ad.Sponsor != "" && (!strings.Contains(publicMetadataText(promoted[0]), "推广方") || len(sponsors) != 1 || publicMetadataText(sponsors[0]) != ad.Sponsor)) {
						t.Fatal("advertising disclosure or sponsor is missing")
					}
					if len(publicSidebarLayoutDescendants(sections[0], "", "ad-controls")) != 0 || publicSidebarLayoutHasAttr(sections[0], "data-ad-carousel") {
						t.Fatal("single advertising card retained timed carousel controls")
					}
				}
			})
		}
	}
	settings.SidebarAds.Enabled = false
	if err := app.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	retained, err := app.Store.Settings(ctx)
	if err != nil || retained.SidebarAds.Enabled || !reflect.DeepEqual(retained.SidebarAds.Items, settings.SidebarAds.Items) {
		t.Fatalf("disabling ads lost saved items: %+v, %v", retained.SidebarAds, err)
	}
	for _, layout := range []string{"journal", "classic"} {
		for _, target := range []string{"/", "/?p=1447"} {
			_, sidebar := publicSidebarLayoutRender(t, app, target, layout)
			publicSidebarLayoutAssertNoAds(t, sidebar)
		}
	}
}

func TestPublicSidebarLayoutRejectsImportedUnsafeAdvertising(t *testing.T) {
	for _, test := range []struct{ name, image, target string }{
		{"HTTP target", "/wp-content/uploads/ad.png", "http://example.com/offer"},
		{"script image", "javascript:alert(1)", "https://example.com/offer"},
		{"outside uploads", "/admin/export/backup.zip", "https://example.com/offer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := publicSidebarLayoutApp(t)
			settings, err := app.Store.Settings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			settings.SidebarAds = SidebarAdsConfig{Enabled: true, IntervalSeconds: 8, Items: []SidebarAd{{Title: "INVALID_AD_SENTINEL", ImageURL: test.image, TargetURL: test.target}}}
			raw, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			// Imported database settings can bypass the normal administrator validation.
			if err := app.Store.SetValue(context.Background(), "site", string(raw)); err != nil {
				t.Fatal(err)
			}
			for _, layout := range []string{"journal", "classic"} {
				for _, target := range []string{"/", "/?p=1447"} {
					body, sidebar := publicSidebarLayoutRender(t, app, target, layout)
					publicSidebarLayoutAssertNoAds(t, sidebar)
					if strings.Contains(body, "INVALID_AD_SENTINEL") || strings.Contains(body, "ZgotmplZ") {
						t.Fatal("invalid imported advertising leaked into the rendered page")
					}
				}
			}
		})
	}
}
