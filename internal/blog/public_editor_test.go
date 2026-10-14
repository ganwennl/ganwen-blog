package blog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestEmbeddedEditorInitialPaintWaitsForSession(t *testing.T) {
	c := adminTestSetup(t, true)
	p := c.newPost("publish")
	route := "/admin/?embed=1&edit=" + strconv.FormatInt(p.ID, 10)
	for _, authenticated := range []bool{false, true} {
		if authenticated {
			c.login()
		}
		response, err := c.client.Get(c.server.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || response.Header.Get("Location") != "" || response.Request.URL.RequestURI() != route {
			t.Fatal("embedded editor navigated away from the requested article")
		}
		if response.Header.Get("Cache-Control") != "no-store" || !strings.Contains(response.Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
			t.Fatal("embedded editor cache or frame authentication boundary changed")
		}
		found := map[string]bool{}
		for _, element := range []string{"section", "div"} {
			for _, node := range publicMetadataNodes(t, string(raw), element, "") {
				id := publicMetadataAttr(node, "id")
				if id != "login" && id != "shell" && id != "startup" {
					continue
				}
				found[id] = true
				hidden := strings.Contains(" "+publicMetadataAttr(node, "class")+" ", " hidden ")
				if hidden != (id != "startup") {
					t.Fatalf("initial %s visibility exposes login or content before session validation", id)
				}
			}
		}
		if len(found) != 3 {
			t.Fatal("missing startup, login or editor shell")
		}
		code, raw := c.request("GET", "editor-session", nil, "", "")
		var session struct {
			Authenticated bool `json:"authenticated"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &session) != nil || session.Authenticated != authenticated {
			t.Fatal("same-origin editor probe lost the existing login session")
		}
		code, raw = c.request("GET", "post?id="+strconv.FormatInt(p.ID, 10), nil, "", "")
		if !authenticated {
			if code != http.StatusUnauthorized {
				t.Fatal("startup shell bypassed article authorization")
			}
			continue
		}
		var article struct {
			Post Post `json:"post"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &article) != nil || article.Post.ID != p.ID {
			t.Fatal("authenticated embedded editor could not read the requested article")
		}
	}
}

func TestPublicEditorShellIsSessionIndependentAndPreservesSEO(t *testing.T) {
	a := publicTestApp(t)
	anon := publicTestRequest(a, "/?p=1447")
	r := httptest.NewRequest(http.MethodGet, "http://localhost/?p=1447", nil)
	r.AddCookie(&http.Cookie{Name: "ganwen_admin", Value: "not-rendered-into-public-cache"})
	logged := httptest.NewRecorder()
	a.handlePublic(logged, r)
	if anon.Code != http.StatusOK || logged.Code != http.StatusOK || anon.Body.String() != logged.Body.String() {
		t.Fatal("Public article HTML must not vary by administrator session")
	}
	body := anon.Body.String()
	for _, want := range []string{`data-post-id="1447"`, `data-article-edit hidden`, `data-admin-link hidden`, `data-public-editor-frame`, `canonical" href="https://www.ganwen.nl/?p=1447"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, `src="/admin/`) || strings.Contains(body, "not-rendered-into-public-cache") || anon.Header().Get("Set-Cookie") != "" {
		t.Fatal("Anonymous public HTML loaded an editor or personalized authentication data")
	}
	if !strings.Contains(anon.Header().Get("Content-Security-Policy"), "frame-src 'self' ") {
		t.Fatal("Same-origin embedded editor is blocked by the public CSP")
	}
	for _, route := range []string{"/", "/?paged=1", "/en/?paged=1"} {
		archive := publicTestRequest(a, route).Body.String()
		if !strings.Contains(archive, "data-article-edit hidden") || !strings.Contains(archive, `data-edit-post-id="1447"`) || !strings.Contains(archive, "data-public-editor-frame") {
			t.Fatalf("Listing %s missing hidden per-post editing controls", route)
		}
		if strings.Contains(archive, `src="/admin/`) {
			t.Fatal("Anonymous listing loaded an editor")
		}
	}
}
