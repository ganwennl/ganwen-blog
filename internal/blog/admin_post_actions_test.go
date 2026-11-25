package blog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func postActionCall(t *testing.T, c *adminTestClient, id, version int64, action string) (int, Post) {
	t.Helper()
	code, body := c.request("POST", "post/action", map[string]any{"id": id, "version": version, "action": action}, c.server.URL, c.csrf)
	var out struct {
		Post Post `json:"post"`
	}
	if code == 200 && action != postActionDelete {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
	}
	return code, out.Post
}

func TestAdminPostActionsMovePageToPostAndBack(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	page := Post{Type: "page", Status: "publish", Title: "误发的文章", ContentHTML: "<p>正文</p>"}
	if err := c.app.Store.NewPost(ctx, &page); err != nil {
		t.Fatal(err)
	}
	if page.CanonicalURL != "https://www.ganwen.nl/?page_id=10000" {
		t.Fatalf("page canonical = %q", page.CanonicalURL)
	}
	code, moved := postActionCall(t, c, page.ID, page.Version, postActionToPost)
	if code != 200 || moved.Type != "post" || moved.Status != "publish" || moved.CanonicalURL != "https://www.ganwen.nl/?p=10000" || moved.Version != page.Version+1 {
		t.Fatalf("move to post: %d %#v", code, moved)
	}
	stored, _ := c.app.Store.GetPost(ctx, page.ID)
	if stored.Type != "post" {
		t.Fatal("type not persisted")
	}
	posts, _ := c.app.Store.ListPosts(ctx, false)
	if len(posts) != 1 || posts[0].Type != "post" {
		t.Fatal("moved article missing from public post list")
	}
	req, _ := http.NewRequest(http.MethodGet, c.server.URL+"/?page_id=10000", nil)
	req.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "zh"})
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMovedPermanently || res.Header.Get("Location") != "https://www.ganwen.nl/?p=10000" {
		t.Fatalf("old page link: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if code, _ := postActionCall(t, c, page.ID, page.Version, postActionToPage); code != 409 {
		t.Fatal("stale version accepted")
	}
	if code, _ := postActionCall(t, c, page.ID, moved.Version, postActionToPost); code != 409 {
		t.Fatal("moving to the same group accepted")
	}
	code, back := postActionCall(t, c, page.ID, moved.Version, postActionToPage)
	if code != 200 || back.Type != "page" || back.CanonicalURL != "https://www.ganwen.nl/?page_id=10000" {
		t.Fatalf("move back: %d %#v", code, back)
	}
}

func TestAdminPostActionsIndependentPageAndImportedPermalink(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	independent := Post{Type: "page", Status: "draft", Title: "证据", ContentHTML: "<p>x</p>", PublicPath: "/proof", Unlisted: true}
	if err := c.app.Store.NewPost(ctx, &independent); err != nil {
		t.Fatal(err)
	}
	if code, _ := postActionCall(t, c, independent.ID, independent.Version, postActionToPost); code != 409 {
		t.Fatal("independent page moved without clearing its address")
	}
	if stored, _ := c.app.Store.GetPost(ctx, independent.ID); stored.Type != "page" || stored.PublicPath != "/proof" || !stored.Unlisted {
		t.Fatal("rejected move changed the independent page")
	}
	imported := Post{ID: 77, Version: 1, Type: "page", Status: "publish", Title: "旧页面", ContentHTML: "<p>x</p>", CanonicalURL: "https://www.ganwen.nl/old-page/"}
	if err := c.app.Store.saveLocked(ctx, &imported); err != nil {
		t.Fatal(err)
	}
	code, moved := postActionCall(t, c, 77, 1, postActionToPost)
	if code != 200 || moved.CanonicalURL != "https://www.ganwen.nl/old-page/" {
		t.Fatalf("imported permalink changed: %d %#v", code, moved)
	}
}

func TestAdminPostActionsToggleTrashRestoreDelete(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	p := c.newPost("publish")
	code, hidden := postActionCall(t, c, p.ID, p.Version, postActionHide)
	if code != 200 || hidden.Status != "private" {
		t.Fatalf("hide: %d %#v", code, hidden)
	}
	code, shown := postActionCall(t, c, p.ID, hidden.Version, postActionPublish)
	if code != 200 || shown.Status != "publish" {
		t.Fatalf("publish: %d %#v", code, shown)
	}
	if code, _ := postActionCall(t, c, p.ID, shown.Version, postActionDelete); code != 409 {
		t.Fatal("permanent delete allowed outside the trash")
	}
	code, trashed := postActionCall(t, c, p.ID, shown.Version, postActionTrash)
	if code != 200 || trashed.Status != "trash" {
		t.Fatalf("trash: %d %#v", code, trashed)
	}
	if posts, _ := c.app.Store.ListPosts(ctx, false); len(posts) != 0 {
		t.Fatal("trashed article still public")
	}
	for _, action := range []string{postActionPublish, postActionHide, postActionToPage, postActionTrash} {
		if code, _ := postActionCall(t, c, p.ID, trashed.Version, action); code != 409 {
			t.Fatalf("%s allowed in trash", action)
		}
	}
	trashed.Title, trashed.Status = "edited", "draft"
	if code, _ := c.request("POST", "post", trashed, c.server.URL, c.csrf); code != 409 {
		t.Fatal("editor save silently restored a trashed article")
	}
	code, restored := postActionCall(t, c, p.ID, trashed.Version, postActionRestore)
	if code != 200 || restored.Status != "draft" {
		t.Fatalf("restore: %d %#v", code, restored)
	}
	_, trashed = postActionCall(t, c, p.ID, restored.Version, postActionTrash)
	if code, _ := postActionCall(t, c, p.ID, trashed.Version-1, postActionDelete); code != 409 {
		t.Fatal("stale permanent delete accepted")
	}
	if code, _ := postActionCall(t, c, p.ID, trashed.Version, postActionDelete); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if _, err := c.app.Store.GetPost(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("article not deleted")
	}
	if code, _ := postActionCall(t, c, p.ID, trashed.Version, "bogus"); code != 400 {
		t.Fatal("unknown action accepted")
	}
	if code, _ := c.request("POST", "post/action", map[string]any{"id": 1, "version": 1, "action": "trash"}, c.server.URL, ""); code != 403 {
		t.Fatal("action without CSRF accepted")
	}
}
