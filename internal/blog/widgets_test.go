package blog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func saveWidgetContacts(t *testing.T, a *App, contacts []ContactLink) {
	t.Helper()
	settings, err := a.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.ContactLinks = contacts
	if err := a.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
}

func widgetAttribute(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func widgetHasClass(n *html.Node, class string) bool {
	for _, value := range strings.Fields(widgetAttribute(n, "class")) {
		if value == class {
			return true
		}
	}
	return false
}

func renderedIntroContacts(t *testing.T, body string) []*html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var menus []*html.Node
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "nav" && widgetHasClass(n, "intro-contacts") {
			menus = append(menus, n)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	return menus
}

func publicWidgetRequest(a *App, target, layout string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://localhost"+target, nil)
	r.Header.Set("Accept-Language", "nl,en;q=0.9")
	r.AddCookie(&http.Cookie{Name: "ganwen.layout.v1", Value: layout})
	if target == "/" {
		r.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "zh"})
	}
	w := httptest.NewRecorder()
	a.handlePublic(w, r)
	return w
}

func assertContactsOnlyInIntroduction(t *testing.T, body string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if widgetHasClass(n, "contact-card") || widgetHasClass(n, "contact-identity") || widgetHasClass(n, "contact-menu") || widgetHasClass(n, "nav-contact") {
				t.Errorf("removed contact card/menu still rendered: %s", n.Data)
			}
			if widgetHasClass(n, "intro-contacts") || widgetHasClass(n, "contact-links") || widgetHasClass(n, "contact-link") || widgetAttribute(n, "data-contact-kind") != "" || widgetAttribute(n, "data-contact-platform") != "" {
				inIntroduction := false
				for ancestor := n.Parent; ancestor != nil; ancestor = ancestor.Parent {
					if widgetHasClass(ancestor, "page-heading") || widgetHasClass(ancestor, "article-header") {
						inIntroduction = true
					}
					if ancestor.Data == "details" || ancestor.Data == "footer" || widgetAttribute(ancestor, "id") == "main-nav" {
						t.Errorf("contact is hidden or outside introduction: %s", ancestor.Data)
					}
					hidden := widgetHasClass(ancestor, "hidden")
					for _, attr := range ancestor.Attr {
						hidden = hidden || attr.Key == "hidden"
					}
					if hidden {
						t.Errorf("contact introduction is hidden: %s", ancestor.Data)
					}
				}
				if !inIntroduction {
					t.Errorf("contact rendered outside page introduction: %s", n.Data)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
}

func TestContactWidgetsAbsentWithoutValidConfiguredLinks(t *testing.T) {
	a := publicTestApp(t)
	for _, links := range [][]ContactLink{
		nil,
		{
			{Kind: "x", URL: ""},
			{Kind: "unknown", URL: "https://example.com"},
			{Kind: "website", URL: "javascript:alert(1)"},
			{Kind: "website", URL: "data:text/html,<script>alert(1)</script>"},
			{Kind: "website", URL: "http://example.com"},
			{Kind: "website", URL: "//example.com"},
			{Kind: "github", URL: "https://user:password@example.com"},
			{Kind: "email", URL: "mailto:hello@example.com?bcc=secret@example.com"},
			{Kind: "email", URL: "mailto:hello@example.com%0d%0aBcc:secret@example.com"},
		},
	} {
		// Direct storage also covers old imports that did not use admin validation.
		saveWidgetContacts(t, a, links)
		for _, layout := range []string{"journal", "classic"} {
			for _, target := range []string{"/", "/?cat=13", "/?p=1447", "/?page_id=2"} {
				w := publicWidgetRequest(a, target, layout)
				if w.Code != 200 || len(renderedIntroContacts(t, w.Body.String())) != 0 {
					t.Fatalf("empty/unsafe widget rendered on %s/%s: %d", layout, target, w.Code)
				}
				assertContactsOnlyInIntroduction(t, w.Body.String())
			}
		}
	}
}

func TestContactWidgetsLocalizedAndSafeRendering(t *testing.T) {
	a := publicTestApp(t)
	contacts := []ContactLink{
		{Kind: "x", Label: `<script>alert(1)</script>`, URL: "https://x.com/ganwen_nl"},
		{Kind: "email", URL: "mailto:ganwen94@gmail.com"},
		{Kind: "telegram", URL: "https://t.me/widget_test"},
		{Kind: "github", URL: "https://github.com/widget-test"},
		{Kind: "mastodon", URL: "https://example.social/@widget-test"},
		{Kind: "website", URL: "https://example.com/?one=1&two=2"},
	}
	saveWidgetContacts(t, a, contacts)
	routes := []struct{ target, heading, label string }{
		{"/", "保持联系", "联系"}, {"/en/", "Stay in touch", "Contact"}, {"/nl/", "Blijf in contact", "Contact"}, {"/es/", "Sigamos en contacto", "Contacto"},
		{"/?cat=13", "保持联系", "联系"}, {"/?p=1447", "保持联系", "联系"}, {"/en/?p=1447", "Stay in touch", "Contact"},
		{"/?page_id=2", "保持联系", "联系"}, {"/?attachment_id=200", "保持联系", "联系"}, {"/?s=原文", "保持联系", "联系"},
		{"/?tag=%E6%A0%87%E7%AD%BE", "保持联系", "联系"}, {"/?m=202501", "保持联系", "联系"}, {"/?author=1", "保持联系", "联系"}, {"/?paged=1", "保持联系", "联系"},
	}
	for _, lang := range []string{"hi", "ar", "fr", "bn", "pt", "id", "ur"} {
		routes = append(routes, struct{ target, heading, label string }{"/" + lang + "/", publicText(lang, "保持联系", "Stay in touch", "Blijf in contact", "Sigamos en contacto"), publicText(lang, "联系", "Contact", "Contact", "Contacto")})
	}
	for _, layout := range []string{"journal", "classic"} {
		for _, route := range routes {
			t.Run(layout+"/"+route.target, func(t *testing.T) {
				w := publicWidgetRequest(a, route.target, layout)
				if w.Code != 200 {
					t.Fatalf("response %d: %s", w.Code, w.Body.String())
				}
				menus := renderedIntroContacts(t, w.Body.String())
				if len(menus) != 1 || widgetAttribute(menus[0], "aria-label") != route.heading {
					t.Fatalf("missing or duplicated localized contact introduction: %d", len(menus))
				}
				menu := menus[0]
				if menu.Parent == nil || !widgetHasClass(menu.Parent, "intro-meta") {
					t.Fatal("contact icons are not beside the introduction metadata")
				}
				var links, caption *html.Node
				for child := menu.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == html.ElementNode && widgetHasClass(child, "contact-caption") {
						caption = child
					}
					if child.Type == html.ElementNode && widgetHasClass(child, "contact-links") {
						links = child
					}
				}
				if links == nil {
					t.Fatalf("direct contact links missing for %s", route.target)
				}
				wantCaption := route.label + ":"
				if route.label == "联系" {
					wantCaption = "联系方式："
				}
				if caption == nil || caption.FirstChild == nil || caption.FirstChild.Data != wantCaption || widgetHasClass(caption, "sr-only") {
					t.Fatalf("visible localized contact caption missing for %s", route.target)
				}
				assertContactsOnlyInIntroduction(t, w.Body.String())
				seen := map[string]int{}
				icons := 0
				var visit func(*html.Node)
				visit = func(n *html.Node) {
					if n.Type == html.ElementNode {
						switch n.Data {
						case "script", "iframe":
							t.Errorf("external embed or executable label in contact menu: %s", n.Data)
						case "img":
							if widgetAttribute(n, "src") != "https://example.com/favicon.ico" || widgetAttribute(n, "referrerpolicy") != "no-referrer" {
								t.Errorf("unexpected contact image: %s", widgetAttribute(n, "src"))
							}
						case "svg":
							icons++
						case "a":
							kind := widgetAttribute(n, "data-contact-kind")
							seen[kind]++
							label := widgetAttribute(n, "aria-label")
							if label == "" || widgetAttribute(n, "title") != label {
								t.Errorf("contact icon missing accessible name or account tooltip: %s", kind)
							}
							platform := kind
							if kind == "email" {
								platform = "gmail"
							}
							if widgetAttribute(n, "data-contact-platform") != platform {
								t.Errorf("contact platform changed: %s", kind)
							}
							if widgetAttribute(n, "rel") != "noopener noreferrer" {
								t.Error("contact link missing opener/referrer protection")
							}
							for _, contact := range contacts {
								if kind != contact.Kind {
									continue
								}
								if widgetAttribute(n, "href") != contact.URL {
									t.Errorf("configured destination changed: %s", kind)
								}
								if kind == "email" && widgetAttribute(n, "target") != "" {
									t.Error("mailto incorrectly opens a new browser tab")
								} else if kind != "email" && widgetAttribute(n, "target") != "_blank" {
									t.Errorf("HTTPS destination does not open a new tab: %s", kind)
								}
							}
						}
					}
					for child := n.FirstChild; child != nil; child = child.NextSibling {
						visit(child)
					}
				}
				visit(links)
				if icons != len(contacts) || len(seen) != len(contacts) {
					t.Fatalf("missing configured contact or local icon: links=%v icons=%d", seen, icons)
				}
				for _, count := range seen {
					if count != 1 {
						t.Error("contact rendered more than once")
					}
				}
			})
		}
	}
}

func TestContactWidgetsDoNotInventAccountsOrExposeDuplicateURLs(t *testing.T) {
	a := publicTestApp(t)
	saveWidgetContacts(t, a, []ContactLink{
		{Kind: "email", URL: "mailto:ganwen94@gmail.com"},
		{Kind: "email", URL: "mailto:ganwen94@GMAIL.COM"},
		{Kind: "x", Label: "未配置账号", URL: ""},
	})
	body := publicTestRequest(a, "/").Body.String()
	for _, absent := range []string{`data-contact-kind="x"`, `data-contact-kind="telegram"`, "未配置账号", "duplicate@example.com"} {
		if strings.Contains(body, absent) {
			t.Errorf("unconfigured or duplicate contact exposed: %s", absent)
		}
	}
	if !strings.Contains(body, "ganwen94@gmail.com") {
		t.Error("configured email was hidden")
	}
}

func renderedAvatarElements(t *testing.T, body string) (images, fallbacks []*html.Node) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, attr := range n.Attr {
				if attr.Key == "data-avatar-image" {
					images = append(images, n)
				} else if attr.Key == "data-avatar-fallback" {
					fallbacks = append(fallbacks, n)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	return images, fallbacks
}

func TestAvatarWidgetHeaderUsesSafeImageAndLocalizesFallback(t *testing.T) {
	a := publicTestApp(t)
	settings, err := a.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.ContactLinks = []ContactLink{{Kind: "email", URL: "mailto:ganwen94@gmail.com"}}
	for _, avatar := range []struct{ configured, rendered string }{
		{"/wp-content/uploads/2026/10/avatar.webp", "/wp-content/uploads/2026/10/avatar.webp"},
		{"https://www.ganwen.nl/wp-content/uploads/avatar.png?v=2", "/wp-content/uploads/avatar.png?v=2"},
		{"https://images.example.com/avatar.jpg?size=64&version=2", "https://images.example.com/avatar.jpg?size=64&version=2"},
	} {
		settings.AvatarURL = avatar.configured
		if err := a.Store.SaveSettings(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"/", "/en/", "/nl/", "/es/", "/?p=1447"} {
			w := publicTestRequest(a, target)
			if w.Code != 200 {
				t.Fatalf("%s => %d", target, w.Code)
			}
			images, fallbacks := renderedAvatarElements(t, w.Body.String())
			if len(images) != 1 || len(fallbacks) != 1 {
				t.Fatalf("%s: header avatar missing or repeated outside navigation: images=%d fallbacks=%d", target, len(images), len(fallbacks))
			}
			for _, image := range images {
				if image.Data != "img" || widgetAttribute(image, "src") != avatar.rendered {
					t.Errorf("avatar source changed or preview left on production host: %s", widgetAttribute(image, "src"))
				}
				width := widgetAttribute(image, "width")
				if width != "53" || widgetAttribute(image, "height") != width {
					t.Error("avatar has no fixed square size")
				}
				if widgetAttribute(image, "alt") != "" || widgetAttribute(image.Parent, "aria-hidden") != "true" {
					t.Error("decorative avatar repeats the visible name for assistive technology")
				}
				if widgetAttribute(image, "onerror") != "" || widgetAttribute(image, "onload") != "" {
					t.Error("avatar uses an inline script instead of shared load/error handlers")
				}
			}
			wantFallback := "甘"
			if strings.HasPrefix(target, "/en/") || strings.HasPrefix(target, "/nl/") || strings.HasPrefix(target, "/es/") {
				wantFallback = "G"
			}
			for _, fallback := range fallbacks {
				if fallback.FirstChild == nil || fallback.FirstChild.Data != wantFallback {
					t.Errorf("avatar fallback differs from locale: want %q", wantFallback)
				}
				for _, attr := range fallback.Attr {
					if attr.Key == "hidden" {
						t.Error("fallback hidden before an image has loaded successfully")
					}
				}
			}
		}
	}
}

func TestAvatarWidgetRejectsUnsafeImportedSettings(t *testing.T) {
	a := publicTestApp(t)
	settings, err := a.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.ContactLinks = []ContactLink{{Kind: "email", URL: "mailto:ganwen94@gmail.com"}}
	for _, unsafe := range []string{
		"", "javascript:alert(1)", "data:image/svg+xml,<svg onload='alert(1)'/>", "file:///private/avatar.jpg",
		"//images.example.com/avatar.jpg", "http://images.example.com/avatar.jpg", "https://user:password@example.com/avatar.jpg",
		"/wp-content/uploads/../../data/private.png", "/wp-content/uploads/avatar.svg", "https://images.example.com/avatar.jpg%0a",
	} {
		// Imported settings must receive the same defense as admin settings.
		settings.AvatarURL = unsafe
		if err := a.Store.SaveSettings(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
		images, fallbacks := renderedAvatarElements(t, publicTestRequest(a, "/?p=1447").Body.String())
		if len(images) != 0 || len(fallbacks) != 1 {
			t.Errorf("unsafe/empty avatar rendered instead of fallback: %q images=%d fallbacks=%d", unsafe, len(images), len(fallbacks))
		}
	}
}
