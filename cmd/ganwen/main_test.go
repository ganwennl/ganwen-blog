package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"ganwen-blog/internal/blog"
)

func TestDatabaseCLIExportImport(t *testing.T) {
	t.Setenv("GBLOG_DATABASE_DRIVER", "sqlite")
	t.Setenv("GBLOG_DATABASE_PATH", "")
	t.Setenv("GBLOG_DATABASE_DSN", "")
	source := t.TempDir()
	target := t.TempDir()
	file := filepath.Join(t.TempDir(), "backup.json")
	s, err := blog.OpenStore(filepath.Join(source, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := blog.Post{Title: "CLI迁移😀", ContentHTML: "<p>正文</p>", Status: "private"}
	if err = s.NewPost(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	s.Close()
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"ganwen", "database-export", "--data", source, "--file", file}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"ganwen", "database-import", "--data", target, "--file", file}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	s, err = blog.OpenStore(filepath.Join(target, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetPost(context.Background(), p.ID)
	if err != nil || got.Title != p.Title || got.Status != p.Status || got.CanonicalURL != p.CanonicalURL {
		t.Fatal("CLI migration altered private post or canonical")
	}
	if err = run(); err == nil {
		t.Fatal("CLI overwrote a nonempty target")
	}
}

func TestConfigureSiteFreshDefaultsAndLegacyPreservation(t *testing.T) {
	store, err := blog.OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	settings, err := configureSite(ctx, store, "")
	if err != nil || settings.BaseURL != "https://localhost" || settings.PublicVirtualViewsEnabled == nil || *settings.PublicVirtualViewsEnabled || settings.PublicStatsEnabled == nil || !*settings.PublicStatsEnabled {
		t.Fatal("fresh install did not choose real counters", err)
	}
	legacy := blog.SiteSettings{Title: "Existing", BaseURL: "https://blog.example", PostsPerPage: 8}
	if err := store.SaveSettings(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	settings, err = configureSite(ctx, store, "")
	if err != nil || settings.PublicVirtualViewsEnabled != nil || settings.BaseURL != legacy.BaseURL {
		t.Fatal("legacy configuration was changed")
	}
	if _, err := configureSite(ctx, store, "http://invalid.example"); err == nil {
		t.Fatal("accepted non-HTTPS site URL")
	}
	settings, err = configureSite(ctx, store, "https://new.example")
	if err != nil || settings.BaseURL != "https://new.example" || settings.PublicVirtualViewsEnabled != nil {
		t.Fatal("site URL changed unrelated legacy defaults", err)
	}
}

func TestIndependentPageCLIImportsCompleteEvidencePackage(t *testing.T) {
	t.Setenv("GBLOG_DATABASE_DRIVER", "sqlite")
	t.Setenv("GBLOG_DATABASE_PATH", "")
	t.Setenv("GBLOG_DATABASE_DSN", "")
	target := t.TempDir()
	prepared := t.TempDir()
	source := []byte("<!doctype html><html><body>保留原证据</body></html>")
	evidence := []byte("unchanged-evidence-fixture")
	if err := os.WriteFile(filepath.Join(prepared, "source.html"), source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prepared, "media"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared, "media", "evidence.jpg"), evidence, 0600); err != nil {
		t.Fatal(err)
	}
	p := blog.Post{Type: "page", Status: "publish", PublicPath: "/bewijs.html", Unlisted: true, Title: "原证据", ContentHTML: `<img src="/evidence.jpg">`, SourceHTML: string(source)}
	entry := func(name string, raw []byte) map[string]any {
		return map[string]any{"path": name, "bytes": len(raw), "sha256": fmt.Sprintf("%x", sha256.Sum256(raw))}
	}
	raw, err := json.Marshal(map[string]any{"schema": 1, "source_url": "https://www.ganwen.nl/bewijs.html", "post": p, "source": entry("source.html", source), "media": []any{entry("evidence.jpg", evidence)}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(prepared, "page.json")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"ganwen", "import-independent-page", "--data", target, "--file", file}
	if err = run(); err != nil {
		t.Fatal(err)
	}
	if err = run(); err != nil {
		t.Fatal("identical CLI import failed", err)
	}
	s, err := blog.OpenStore(filepath.Join(target, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	posts, err := s.ListPosts(context.Background(), true)
	if err != nil || len(posts) != 1 || posts[0].SourceHTML != p.SourceHTML || !posts[0].Unlisted {
		t.Fatal("CLI import changed publication/originals", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "media", "evidence.jpg"))
	if err != nil || string(got) != string(evidence) {
		t.Fatal("CLI did not install original media", err)
	}
}
