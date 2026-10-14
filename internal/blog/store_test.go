package blog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestImportPreservesStatesAndOriginals(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	original := Post{ID: 1447, Version: 1, Type: "post", Status: "publish", Title: "原始标题", SourceHTML: "<!-- wp:paragraph --><p>原始内容 &amp; 图片</p>", ContentHTML: "<p>原始内容 &amp; 图片</p>", PublishedAt: "2025-01-30T06:43:26+08:00", ModifiedAt: "2026-01-01T12:00:00+08:00", CanonicalURL: "https://www.ganwen.nl/?p=1447"}
	draft := original
	draft.ID = 1876
	draft.Status = "draft"
	draft.CanonicalURL = "https://www.ganwen.nl/?p=1876"
	raw, _ := json.Marshal(Export{Settings: SiteSettings{BaseURL: "https://www.ganwen.nl", Title: "甘文", PostsPerPage: 10}, Posts: []Post{original, draft}})
	p := filepath.Join(t.TempDir(), "export.json")
	if err = os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	n, err := s.ImportFile(ctx, p)
	if err != nil || n != 2 {
		t.Fatalf("import %d %v", n, err)
	}
	got, err := s.GetPost(ctx, 1447)
	if err != nil || got.SourceHTML != original.SourceHTML || got.ContentHTML != original.ContentHTML || got.CanonicalURL != original.CanonicalURL || got.PublishedAt != original.PublishedAt {
		t.Fatalf("original changed: %#v %v", got, err)
	}
	public, err := s.ListPosts(ctx, false)
	if err != nil || len(public) != 1 {
		t.Fatalf("draft leaked: %v %v", len(public), err)
	}
	n, err = s.ImportFile(ctx, p)
	if err != nil || n != 0 {
		t.Fatal("identical import not idempotent")
	}
	raw, _ = json.Marshal(Export{Posts: []Post{draft}})
	_ = os.WriteFile(p, raw, 0600)
	if _, err = s.ImportFile(ctx, p); err == nil {
		t.Fatal("different snapshot replaced managed data")
	}
}
func TestArticleConcurrencyAndStaleTranslation(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	p := Post{Title: "中文", ContentHTML: "<p>旧正文</p>", Status: "publish"}
	if err = s.NewPost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	stale := p
	p.Translations["en"] = Translation{Title: "English", ContentHTML: "<p>Old body</p>", SourceHash: SourceHash(p), Status: "publish"}
	if err = s.SavePost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	stale.Title = "并发覆盖"
	if err = s.SavePost(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected version conflict: %v", err)
	}
	canonical := p.CanonicalURL
	p.ContentHTML = "<p>新正文</p>"
	p.CanonicalURL = "https://changed.example/"
	p.SourceHTML = "changed"
	if err = s.SavePost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if p.Translations["en"].Status != "review" || p.CanonicalURL != canonical || p.SourceHTML != "" {
		t.Fatal("source update broke URL/original/translation state")
	}
	p.Translations["en"] = Translation{Title: "Stale", SourceHash: "old", Status: "publish"}
	if err = s.SavePost(ctx, &p); err == nil {
		t.Fatal("outdated translation was republished")
	}
}
