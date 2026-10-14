package blog

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"unicode"
)

func TestPublicLanguageDropdownNativeLinksPreserveArchiveContext(t *testing.T) {
	a, _, _ := publicMetadataFixture(t)
	get := publicMetadataClient(t, a)
	wantLanguages := []string{"zh", "nl", "en", "hi", "es", "ar", "fr", "bn", "pt", "id", "ur"}
	for _, selected := range wantLanguages {
		t.Run(selected, func(t *testing.T) {
			body := get(publicLanguagePrefix(selected) + "/?cat=3&tag=%E4%B9%A0%E8%BF%91%E5%B9%B3&paged=1")
			pickers := publicMetadataNodes(t, body, "details", "language-picker")
			if len(pickers) != 1 {
				t.Fatal("native language dropdown missing")
			}
			picker := pickers[0]
			hasDropdownHook, defaultOpen := false, false
			for _, attr := range picker.Attr {
				hasDropdownHook = hasDropdownHook || attr.Key == "data-language-picker"
				defaultOpen = defaultOpen || attr.Key == "open"
			}
			var firstElementName string
			for child := picker.FirstChild; child != nil; child = child.NextSibling {
				if child.Data == "summary" || child.Data == "div" {
					firstElementName = child.Data
					break
				}
			}
			if !hasDropdownHook || firstElementName != "summary" || defaultOpen {
				t.Fatal("language chooser must use a closed native details/summary control")
			}
			panels := publicMetadataNodes(t, body, "div", "language-links")
			if len(panels) != 1 {
				t.Fatal("existing language-links hook missing")
			}
			var gotLanguages []string
			current := 0
			for child := panels[0].FirstChild; child != nil; child = child.NextSibling {
				if child.Data != "a" {
					continue
				}
				lang := publicMetadataAttr(child, "lang")
				gotLanguages = append(gotLanguages, lang)
				link, err := url.Parse(publicMetadataAttr(child, "href"))
				if err != nil || link.IsAbs() || link.Path != publicLanguagePrefix(lang)+"/" || link.Query().Get("cat") != "3" || link.Query().Get("tag") != "习近平" || link.Query().Get("paged") != "1" {
					t.Fatalf("%s dropdown lost archive context: %v", lang, link)
				}
				if strings.TrimSpace(publicMetadataText(child)) != nativeLanguageName(lang) || publicMetadataAttr(child, "dir") != publicLanguageDirection(lang) {
					t.Fatalf("%s native language label/direction missing", lang)
				}
				if publicMetadataAttr(child, "aria-current") == "true" {
					current++
					if lang != selected {
						t.Fatal("wrong selected language")
					}
				}
			}
			if !reflect.DeepEqual(gotLanguages, wantLanguages) || current != 1 {
				t.Fatalf("unexpected language order/current state: %v / %d", gotLanguages, current)
			}
			if got := publicMetadataOneText(t, body, "span", "language-current"); got != nativeLanguageName(selected) {
				t.Fatalf("summary does not show current language: %q", got)
			}
			for _, node := range publicMetadataNodes(t, body, "html", "") {
				if publicMetadataAttr(node, "dir") != publicLanguageDirection(selected) || !reflect.DeepEqual(strings.Fields(publicMetadataAttr(node, "data-supported-languages")), wantLanguages) {
					t.Fatal("page language direction or shared supported languages missing")
				}
			}
			if selected != "zh" {
				for _, node := range publicMetadataNodes(t, body, "a", "nav-item") {
					if strings.IndexFunc(publicMetadataText(node), func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
						t.Fatal("foreign navigation fell back to Chinese")
					}
				}
			}
		})
	}
}

func TestPublicLanguageDropdownOnlyLinksPublishedArticleTranslations(t *testing.T) {
	a := publicTestApp(t)
	for _, selected := range []string{"zh", "en"} {
		body := publicTestRequest(a, publicLanguagePrefix(selected)+"/?p=1447").Body.String()
		panels := publicMetadataNodes(t, body, "div", "language-links")
		if len(panels) != 1 {
			t.Fatal("article language dropdown missing")
		}
		var got []string
		for child := panels[0].FirstChild; child != nil; child = child.NextSibling {
			if child.Data != "a" {
				continue
			}
			lang := publicMetadataAttr(child, "lang")
			got = append(got, lang)
			link, _ := url.Parse(publicMetadataAttr(child, "href"))
			if link.Path != publicLanguagePrefix(lang)+"/" || link.Query().Get("p") != "1447" {
				t.Fatalf("article context lost for %s: %v", lang, link)
			}
		}
		if !reflect.DeepEqual(got, []string{"zh", "en"}) {
			t.Fatalf("unavailable article translations exposed: %v", got)
		}
	}
}
