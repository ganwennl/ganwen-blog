package blog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestIndependentPagePublicRoutesAndDiscovery(t *testing.T) {
	a := publicTestApp(t)
	p := Post{Type: "page", Status: "publish", Unlisted: true, PublicPath: "/bewijs.html", Title: "INDEPENDENT EVIDENCE TITLE", ContentHTML: `<h2 id="a1">Evidence A1</h2><video controls><source src="/evidence.mp4" type="video/mp4"></video><a href="#a1">Evidence link</a>`}
	p.Translations = map[string]Translation{"nl": {Title: "Bewijs", ContentHTML: "<p>Nederlandse originele inhoud</p>", Status: "publish", SourceHash: SourceHash(p)}}
	if err := a.Store.NewPost(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/bewijs.html", "/nl/bewijs.html", "/?page_id=" + strconv.FormatInt(p.ID, 10)} {
		w := publicTestRequest(a, target)
		if w.Code != http.StatusOK || w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Fatalf("%s: status %d, robots %s", target, w.Code, w.Header().Get("X-Robots-Tag"))
		}
		body := w.Body.String()
		if !strings.Contains(body, `data-independent-page="true"`) || !strings.Contains(body, "bewijs.html") {
			t.Fatalf("%s: missing independent page or canonical", target)
		}
		if target == "/bewijs.html" && (!strings.Contains(body, "返回博客") || !strings.Contains(body, `src="/evidence.mp4"`) || !strings.Contains(body, `href="#a1"`)) {
			t.Fatal("return link or original evidence media/anchor missing")
		}
	}
	for _, target := range []string{"/", "/?s=INDEPENDENT", "/?sitemap=posts-page-1", "/?sitemap=users-1", "/?paged=1", "/?p=1447"} {
		w := publicTestRequest(a, target)
		if strings.Contains(w.Body.String(), p.Title) || strings.Contains(w.Body.String(), p.PublicPath) {
			t.Fatalf("independent page discovered through %s", target)
		}
	}
	for _, target := range []string{"/en/bewijs.html", "/bewijs%2ehtml", "/bewijs.html?page_id=2", "/bewijs.html?preview=true"} {
		if w := publicTestRequest(a, target); w.Code != http.StatusNotFound {
			t.Fatalf("%s: expected 404, got %d", target, w.Code)
		}
	}
	w := httptest.NewRecorder()
	a.handlePublic(w, httptest.NewRequest(http.MethodHead, "http://localhost/bewijs.html", nil))
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatal("HEAD does not retain the independent page boundary")
	}
	if err := a.Store.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.PublicPath = "/evidence.html"; return nil }); err != nil {
		t.Fatal(err)
	}
	if publicTestRequest(a, "/bewijs.html").Code != 404 || publicTestRequest(a, "/evidence.html").Code != 200 {
		t.Fatal("address change did not take effect")
	}
	if err := a.Store.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.Status = "draft"; return nil }); err != nil {
		t.Fatal(err)
	}
	if publicTestRequest(a, "/evidence.html").Code != 404 {
		t.Fatal("draft independent page was exposed")
	}
	if err := a.Store.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.Status = "publish"; p.Unlisted = false; p.PublicPath = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	q := "/?page_id=" + strconv.FormatInt(p.ID, 10)
	if w := publicTestRequest(a, q); w.Code != 200 || !strings.Contains(w.Body.String(), `href="https://www.ganwen.nl`+q+`"`) {
		t.Fatal("returning to a normal page left a dead independent canonical")
	}
}

func TestAdminIndependentPageSettingsAndLegacySave(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	p := Post{Type: "page", Status: "publish", Title: "Independent", ContentHTML: "<p>Evidence</p>", Unlisted: true, PublicPath: "/bewijs.html"}
	code, body := c.request("POST", "create", p, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("create: %d %s", code, body)
	}
	var saved struct {
		Post Post `json:"post"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	p = saved.Post
	legacy := map[string]any{"id": p.ID, "version": p.Version, "title": "Edited evidence", "status": "publish", "content_html": "<p>Updated evidence</p>"}
	code, body = c.request("POST", "post", legacy, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("legacy save: %d %s", code, body)
	}
	p, _ = c.app.Store.GetPost(context.Background(), p.ID)
	if !p.Unlisted || p.PublicPath != "/bewijs.html" || p.Title != "Edited evidence" {
		t.Fatal("older editor cleared independent settings")
	}
	p.PublicPath = "/evidence.html"
	code, body = c.request("POST", "post", p, c.server.URL, c.csrf)
	if code != 200 {
		t.Fatalf("address save: %d %s", code, body)
	}
	p, _ = c.app.Store.GetPost(context.Background(), p.ID)
	p.PublicPath = "/admin/"
	code, body = c.request("POST", "post", p, c.server.URL, c.csrf)
	if code != 400 {
		t.Fatalf("reserved address: %d %s", code, body)
	}
	duplicate := Post{Type: "page", Status: "draft", Title: "Duplicate", PublicPath: "/evidence.html"}
	code, body = c.request("POST", "create", duplicate, c.server.URL, c.csrf)
	if code != 400 {
		t.Fatalf("duplicate address: %d %s", code, body)
	}
}
