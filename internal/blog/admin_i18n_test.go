package blog

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

func TestAdminI18nMessagesCoverAllLanguagesAndParameters(t *testing.T) {
	raw, err := adminI18nFiles.ReadFile("admin_messages.json")
	if err != nil {
		t.Fatal(err)
	}
	var messages map[string]map[string]string
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	params := regexp.MustCompile(`\{\d+\}`)
	for _, lang := range publicLanguages() {
		dictionary := messages[lang]
		if len(dictionary) != len(messages["zh"]) {
			t.Fatalf("%s dictionary has %d keys, want %d", lang, len(dictionary), len(messages["zh"]))
		}
		for key := range messages["zh"] {
			value := dictionary[key]
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s missing UI key %q", lang, key)
			}
			want, got := params.FindAllString(key, -1), params.FindAllString(value, -1)
			for _, p := range want {
				if !strings.Contains(value, p) {
					t.Fatalf("%s loses parameter %s in %q", lang, p, key)
				}
			}
			if len(want) != len(got) {
				t.Fatalf("%s changes parameter count in %q", lang, key)
			}
		}
	}
	if bytes.Contains(adminI18nJS, []byte("__UI_MESSAGES__")) {
		t.Fatal("unexpanded UI dictionary")
	}
}

func TestAdminStaticTextAndAttributesHaveExplicitDictionaryKeys(t *testing.T) {
	raw, _ := adminI18nFiles.ReadFile("admin_messages.json")
	var messages map[string]map[string]string
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(bytes.NewReader(adminUI))
	if err != nil {
		t.Fatal(err)
	}
	var visit func(*html.Node, bool)
	visit = func(n *html.Node, marked bool) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for _, attr := range n.Attr {
			if strings.HasPrefix(attr.Key, "data-ui-") {
				if _, ok := messages["zh"][attr.Val]; !ok {
					t.Fatalf("missing explicit UI key %q", attr.Val)
				}
				marked = marked || attr.Key == "data-ui-key"
			}
		}
		if n.Type == html.TextNode && !marked && strings.IndexFunc(n.Data, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
			t.Fatalf("unmarked application text %q", n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c, marked)
		}
	}
	visit(doc, false)
}

func TestAdminUILanguageSelectionClient(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, node, filepath.Join("..", "..", "scripts", "test-admin-i18n-client.cjs")).CombinedOutput(); err != nil {
		t.Fatalf("UI language selection failed: %v\n%s", err, out)
	}
}

func TestAdminI18nIsStaticWithoutSessionOrPrivateData(t *testing.T) {
	// No configured app, authentication store or database is required for static UI text.
	a := &Admin{}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := httptest.NewRecorder()
		a.serve(w, httptest.NewRequest(method, "/admin/ui-i18n.js", nil))
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/javascript") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("static UI response: %d / %v", w.Code, w.Header())
		}
		if method == http.MethodGet && !bytes.Equal(w.Body.Bytes(), adminI18nJS) || method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatal("static UI body mismatch")
		}
	}
	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest(http.MethodPost, "/admin/ui-i18n.js", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal("static UI script accepted a mutation")
	}
}

func TestSelectedPublicLanguageOnlyReordersAvailableLinks(t *testing.T) {
	links := []publicLink{{Lang: "zh", URL: "/original"}, {Lang: "en", URL: "/en/translation"}}
	copyBefore := append([]publicLink(nil), links...)
	got := selectedPublicLanguageFirst(links, "en")
	if got[0].Lang != "en" || got[1].Lang != "zh" || !reflect.DeepEqual(links, copyBefore) {
		t.Fatal("selection changed links or canonical source order")
	}
	if got := selectedPublicLanguageFirst(links, "ar"); !reflect.DeepEqual(got, links) {
		t.Fatal("selection exposed an unavailable translation")
	}
}

func TestPublicErrorsFollowRouteLanguage(t *testing.T) {
	for _, lang := range publicLanguages() {
		for _, key := range []string{"页面不存在", "请求方法不支持", "服务暂时不可用", "RSS 已停用"} {
			if _, ok := publicMessage(lang, key); !ok {
				t.Fatalf("%s lacks public error %q", lang, key)
			}
		}
		w := httptest.NewRecorder()
		(&App{}).handlePublic(w, httptest.NewRequest(http.MethodPost, publicLanguagePrefix(lang)+"/", nil))
		want, _ := publicMessage(lang, "请求方法不支持")
		if w.Code != http.StatusMethodNotAllowed || strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("%s method error did not use route language", lang)
		}
		w = httptest.NewRecorder()
		publicNotFound(w, lang)
		want, _ = publicMessage(lang, "页面不存在")
		if w.Code != http.StatusNotFound || strings.TrimSpace(w.Body.String()) != "404 — "+want {
			t.Fatalf("%s not-found error did not use selected language", lang)
		}
	}
}
