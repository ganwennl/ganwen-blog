package blog

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAdminDefaultLayoutPersistsAndPreservesOmittedField(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	input := map[string]any{"title": "站点版式", "posts_per_page": 8, "default_layout": "classic"}
	code, body := c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	var saved SiteSettings
	if code != http.StatusOK || json.Unmarshal(body, &saved) != nil || saved.DefaultLayout != "classic" {
		t.Fatalf("default layout save failed: %d", code)
	}
	stored, err := c.app.Store.Settings(context.Background())
	if err != nil || stored.DefaultLayout != "classic" {
		t.Fatal("default layout was not persisted")
	}
	delete(input, "default_layout")
	input["title"] = "旧客户端修改标题"
	code, body = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
	if code != http.StatusOK || json.Unmarshal(body, &saved) != nil || saved.DefaultLayout != "classic" {
		t.Fatal("old client omitted layout but reset administrator preference")
	}
	for _, layout := range []string{"journal", "classic", ""} {
		input["default_layout"] = layout
		code, body = c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf)
		want := layout
		if want == "" {
			want = "journal"
		}
		if code != http.StatusOK || json.Unmarshal(body, &saved) != nil || saved.DefaultLayout != want {
			t.Fatalf("layout %q was not stored as %q", layout, want)
		}
		code, body = c.request(http.MethodGet, "settings", nil, "", "")
		if code != http.StatusOK || json.Unmarshal(body, &saved) != nil || saved.DefaultLayout != want {
			t.Fatal("settings reread lost default layout")
		}
	}
	for _, bad := range []any{"cards", "CLASSIC", " journal ", "<script>", 1, nil, true} {
		input["default_layout"] = bad
		if code, _ := c.request(http.MethodPost, "settings", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
			t.Fatalf("invalid default layout accepted: %v", bad)
		}
		stored, err := c.app.Store.Settings(context.Background())
		if err != nil || stored.DefaultLayout != "journal" {
			t.Fatal("invalid default layout modified saved settings")
		}
	}
}

func TestPublicDefaultLayoutAndVisitorOverride(t *testing.T) {
	a := publicTestApp(t)
	settings, err := a.Store.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, defaultLayout := range []string{"", "journal", "classic", "unrecognized-import"} {
		settings.DefaultLayout = defaultLayout
		if err := a.Store.SaveSettings(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
		for _, cookie := range []string{"", "journal", "classic", "invalid", "CLASSIC"} {
			want := "journal"
			if defaultLayout == "classic" {
				want = "classic"
			}
			if cookie == "journal" || cookie == "classic" {
				want = cookie
			}
			for _, target := range []string{"/", "/?paged=1", "/?p=1447", "/en/?p=1447"} {
				response := publicLayoutTestRequest(a, target, cookie)
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `data-layout="`+want+`"`) {
					t.Fatalf("default=%q cookie=%q target=%s did not render %s", defaultLayout, cookie, target, want)
				}
				if response.Header().Get("Set-Cookie") != "" {
					t.Fatal("site default was persisted as an explicit visitor preference")
				}
				if !strings.Contains(response.Header().Get("Vary"), "Cookie") || !strings.Contains(response.Header().Get("Cache-Control"), "private") {
					t.Fatal("default/visitor layouts can leak through shared caches")
				}
			}
		}
	}
}
