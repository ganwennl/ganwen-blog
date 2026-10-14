package blog

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func enableArticleSidebar(t *testing.T, app *App) {
	t.Helper()
	settings, err := app.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.ArticleSidebarEnabled = true
	if err := app.Store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
}

func TestArticleSidebarDefaultsOffAndControlsReadingPagesOnly(t *testing.T) {
	app := publicTestApp(t)
	ctx := context.Background()
	settings, err := app.Store.Settings(ctx)
	if err != nil || settings.ArticleSidebarEnabled {
		t.Fatal("legacy settings must default to hiding the reading sidebar", err)
	}
	settings.SidebarAds = sidebarAdsFixture()
	post, err := app.Store.GetPost(ctx, 1447)
	if err != nil {
		t.Fatal(err)
	}
	post.Translations["ar"] = Translation{Title: "مقال", ContentHTML: "<p>محتوى</p>", SourceHash: SourceHash(post), Status: "publish"}
	sidebarSave(t, app, post)
	independent := Post{Type: "page", Status: "publish", Unlisted: true, PublicPath: "/bewijs.html", Title: "Evidence", ContentHTML: `<h2 id="proof">Proof</h2><a href="/wp-content/uploads/proof.pdf">Evidence file</a>`}
	if err := app.Store.NewPost(ctx, &independent); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true, false} {
		settings.ArticleSidebarEnabled = enabled
		if err := app.Store.SaveSettings(ctx, settings); err != nil {
			t.Fatal(err)
		}
		for _, layout := range []string{"journal", "classic"} {
			for _, target := range []string{"/?p=1447", "/en/?p=1447", "/ar/?p=1447", "/?page_id=2", "/bewijs.html"} {
				response := publicLayoutTestRequest(app, target, layout)
				if response.Code != http.StatusOK {
					t.Fatalf("%s %s: HTTP %d", layout, target, response.Code)
				}
				body := response.Body.String()
				asides := publicMetadataNodes(t, body, "aside", "blog-sidebar")
				grids := publicMetadataNodes(t, body, "div", "page-with-sidebar")
				fullWidth := publicMetadataNodes(t, body, "main", "reading-full-width")
				if enabled && (len(asides) != 1 || len(grids) != 1 || len(fullWidth) != 0) {
					t.Fatalf("enabled %s %s did not restore the reading sidebar", layout, target)
				}
				if !enabled && (len(asides) != 0 || len(grids) != 0 || len(fullWidth) != 1 || strings.Contains(body, "data-sidebar-ad")) {
					t.Fatalf("disabled %s %s left a sidebar or reserved column", layout, target)
				}
				for _, cookie := range response.Result().Cookies() {
					if !enabled && cookie.Name == publicSidebarAdCookieName {
						t.Fatal("hidden article advertisements consumed the refresh sequence")
					}
				}
				if target == "/bewijs.html" && (!strings.Contains(body, "返回博客") || !strings.Contains(body, `href="/wp-content/uploads/proof.pdf"`) || response.Header().Get("X-Robots-Tag") != "noindex, nofollow") {
					t.Fatal("sidebar setting changed the independent page evidence, return link or visibility")
				}
			}
			for _, target := range []string{"/", "/?paged=1", "/?s=原文", "/en/"} {
				response := publicLayoutTestRequest(app, target, layout)
				body := response.Body.String()
				if response.Code != http.StatusOK || len(publicMetadataNodes(t, body, "aside", "blog-sidebar")) != 1 || strings.Contains(body, "reading-full-width") {
					t.Fatalf("setting %t changed sidebar on %s %s", enabled, layout, target)
				}
			}
		}
	}
}

func TestAdminArticleSidebarSettingsPersistenceLegacyAndValidation(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	code, raw := c.request(http.MethodGet, "settings", nil, "", "")
	var saved SiteSettings
	if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.ArticleSidebarEnabled || !strings.Contains(string(raw), `"article_sidebar_enabled":false`) {
		t.Fatal("settings API did not expose the default-off article sidebar")
	}
	for _, enabled := range []bool{true, false, true} {
		input := map[string]any{"title": "文章侧栏设置", "posts_per_page": 10, "article_sidebar_enabled": enabled}
		code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
		if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.ArticleSidebarEnabled != enabled {
			t.Fatalf("saving sidebar %t failed: HTTP %d %s", enabled, code, raw)
		}
		reopened, err := OpenStore(filepath.Join(c.app.Config.DataDir, "blog.db"))
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := reopened.Settings(ctx)
		_ = reopened.Close()
		if err != nil || persisted.ArticleSidebarEnabled != enabled {
			t.Fatal("article sidebar setting did not survive reopening", err)
		}
		delete(input, "article_sidebar_enabled")
		input["title"] = "旧客户端保存"
		code, raw = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
		if code != http.StatusOK || json.Unmarshal(raw, &saved) != nil || saved.ArticleSidebarEnabled != enabled {
			t.Fatal("legacy settings save reset the article sidebar")
		}
	}
	before, err := c.app.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, "true", 1, []bool{true}, map[string]any{"enabled": true}} {
		input := map[string]any{"title": "不应保存", "posts_per_page": 8, "article_sidebar_enabled": value}
		if code, raw := c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
			t.Fatalf("invalid article sidebar %v returned HTTP %d %s", value, code, raw)
		}
		after, err := c.app.Store.Settings(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("invalid article sidebar update changed saved settings", err)
		}
	}
}

func TestAdminArticleSidebarRequiresAuthenticationAndCSRF(t *testing.T) {
	c := adminTestSetup(t, true)
	input := map[string]any{"title": "设置", "posts_per_page": 8, "article_sidebar_enabled": true}
	if code, _ := c.request(http.MethodPost, "settings", input, c.server.URL, ""); code != http.StatusUnauthorized {
		t.Fatal("anonymous client changed article sidebar", code)
	}
	c.login()
	if code, _ := c.request(http.MethodPost, "settings", input, c.server.URL, "bad-csrf"); code != http.StatusForbidden {
		t.Fatal("article sidebar update bypassed CSRF", code)
	}
	saved, err := c.app.Store.Settings(context.Background())
	if err != nil || saved.ArticleSidebarEnabled {
		t.Fatal("rejected update changed article sidebar", err)
	}
}
