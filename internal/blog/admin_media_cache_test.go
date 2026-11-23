package blog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminMediaListingIsCachedUntilUploadInvalidates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wp-content", "uploads", "2026")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "first.png"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	admin := &Admin{app: &App{Config: Config{MediaDir: root}}}
	list := func() []mediaItem {
		response := httptest.NewRecorder()
		admin.media(response, httptest.NewRequest(http.MethodGet, "/admin/api/media", nil))
		var items []mediaItem
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil {
			t.Fatalf("media listing failed: %d", response.Code)
		}
		return items
	}
	if got := list(); len(got) != 1 {
		t.Fatalf("initial listing has %d items", len(got))
	}
	if err := os.WriteFile(filepath.Join(dir, "second.png"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 1 {
		t.Fatal("listing was rescanned inside the cache window")
	}
	admin.invalidateMediaCache()
	if got := list(); len(got) != 2 {
		t.Fatal("invalidated listing missed a new file")
	}
	admin.mediaMu.Lock()
	admin.mediaExpires = time.Now().Add(-time.Second)
	admin.mediaMu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "third.png"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 3 {
		t.Fatal("expired listing was not rescanned")
	}
}

func TestPublicSidebarServesCachedCountsWhileRefreshing(t *testing.T) {
	app, _ := analyticsFixture(t)
	now := analyticsTestDate
	stale := publicSidebarCache{day: now.Format("2006-01-02"), expires: now.Add(-time.Second), stats: publicSidebarStats{TodayViews: 42}}
	app.Analytics.sidebarMu.Lock()
	app.Analytics.sidebarCache, app.Analytics.sidebarRefreshing = stale, true
	app.Analytics.sidebarMu.Unlock()
	if got := app.Analytics.publicSidebarCountsAt(context.Background(), now); got.stats.TodayViews != 42 {
		t.Fatalf("concurrent request did not get cached counts: %+v", got.stats)
	}
	app.Analytics.sidebarMu.Lock()
	app.Analytics.sidebarRefreshing = false
	app.Analytics.sidebarMu.Unlock()
	got := app.Analytics.publicSidebarCountsAt(context.Background(), now)
	if got.stats.TodayViews == 42 || !got.expires.After(now) || app.Analytics.sidebarRefreshing {
		t.Fatalf("stale counts were not refreshed: %+v", got)
	}
}
