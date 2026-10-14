package blog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func independentPageStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newIndependentPage(t *testing.T, s *Store, address, status string) Post {
	t.Helper()
	p := Post{Type: "page", Status: status, Title: "独立证据页", ContentHTML: "<p>证据正文</p>", PublicPath: address, Unlisted: true}
	if err := s.NewPost(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIndependentPageAddressValidation(t *testing.T) {
	for _, address := range []string{"", "/bewijs.html", "/evidence", "/case-2026/evidence_1.html", "/case-2026/", "/Evidence"} {
		if err := validPublicPagePath(address); err != nil {
			t.Errorf("valid address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"/", "bewijs.html", "//bewijs.html", "/bewijs.html?x=1", "/bewijs.html#proof", "/bewijs%2ehtml", "/proof/../bewijs.html", "/proof/./bewijs.html", "/proof//file", "/proof\\file", "/proof:123", "/ proof", "/proof\nfile", "/证据", "/admin", "/admin/proof", "/ADMIN/proof", "/wp-admin/proof", "/wp-content/proof", "/assets/proof", "/analytics/event", "/data/proof", "/en", "/en/proof", "/zh/proof", "/robots.txt", "/sitemap.xml", "/index.php", "/proof.pdf", "/proof.png", "/proof.mp4", "/proof.txt", "/proof.json", "/" + strings.Repeat("a", 512)} {
		if err := validPublicPagePath(address); !errors.Is(err, ErrIndependentPageInvalid) {
			t.Errorf("invalid address %q accepted: %v", address, err)
		}
	}
	for _, p := range []Post{{Type: "post", Unlisted: true}, {Type: "post", PublicPath: "/proof"}, {Type: "attachment", PublicPath: "/proof"}, {Type: "page", Unlisted: true}} {
		if err := validateIndependentPage(p); !errors.Is(err, ErrIndependentPageInvalid) {
			t.Fatalf("invalid metadata accepted: %#v %v", p, err)
		}
	}
}

func TestIndependentPageDiscoveryAddressAndStatus(t *testing.T) {
	s := independentPageStore(t)
	ctx := context.Background()
	p := newIndependentPage(t, s, "/bewijs.html", "publish")
	if p.CanonicalURL != "https://www.ganwen.nl/bewijs.html" {
		t.Fatal("new independent page did not preserve its canonical address")
	}
	if path := publicIndependentPagePath(p, "zh"); path != "/bewijs.html" {
		t.Fatalf("Chinese path = %q", path)
	}
	if path := publicIndependentPagePath(p, "nl"); path != "/nl/bewijs.html" {
		t.Fatalf("Dutch path = %q", path)
	}
	public, err := s.ListPosts(ctx, false)
	if err != nil || len(public) != 0 {
		t.Fatalf("unlisted page appeared in discovery: %v %v", public, err)
	}
	all, err := s.ListAdminPosts(ctx)
	if err != nil || len(all) != 1 || !all[0].Unlisted || all[0].PublicPath != p.PublicPath {
		t.Fatalf("unlisted page absent in admin: %v %v", all, err)
	}
	got, err := s.GetPublicPageByPath(ctx, p.PublicPath)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("direct address lost content: %#v %v", got, err)
	}
	got.Translations["nl"] = Translation{Title: "cache mutation"}
	again, _ := s.GetPublicPageByPath(ctx, p.PublicPath)
	if len(again.Translations) != 0 {
		t.Fatal("returned page shared mutable cache metadata")
	}
	p.PublicPath = "/evidence"
	if err = s.SavePost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if p.CanonicalURL != "https://www.ganwen.nl/evidence" {
		t.Fatal("custom address did not update its canonical metadata")
	}
	if _, err = s.GetPublicPageByPath(ctx, "/bewijs.html"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old custom address still resolves: %v", err)
	}
	for _, status := range []string{"draft", "private", "pending", "future", "trash", "publish"} {
		if err = s.UpdatePost(ctx, p.ID, func(v *Post) error { v.Status = status; return nil }); err != nil {
			t.Fatal(err)
		}
		_, err = s.GetPublicPageByPath(ctx, "/evidence")
		if status == "publish" && err != nil || status != "publish" && !errors.Is(err, ErrNotFound) {
			t.Errorf("status %s direct access = %v", status, err)
		}
	}
	if err = s.UpdatePost(ctx, p.ID, func(v *Post) error { v.Unlisted = false; return nil }); err != nil {
		t.Fatal(err)
	}
	public, err = s.ListPosts(ctx, false)
	if err != nil || len(public) != 1 || public[0].ID != p.ID {
		t.Fatalf("listing toggle did not invalidate cache: %v %v", public, err)
	}
}

func TestIndependentPageAddressConflictsReserveDrafts(t *testing.T) {
	s := independentPageStore(t)
	ctx := context.Background()
	p := newIndependentPage(t, s, "/bewijs.html", "draft")
	duplicate := Post{Type: "page", Status: "publish", PublicPath: p.PublicPath, Title: "重复页面"}
	if err := s.NewPost(ctx, &duplicate); !errors.Is(err, ErrIndependentPagePathConflict) {
		t.Fatalf("draft address reused: %v", err)
	}
	second := newIndependentPage(t, s, "/second", "publish")
	if err := s.UpdatePost(ctx, second.ID, func(v *Post) error { v.PublicPath = p.PublicPath; return nil }); !errors.Is(err, ErrIndependentPagePathConflict) {
		t.Fatalf("update accepted duplicate address: %v", err)
	}
	second.PublicPath = p.PublicPath
	if err := s.SavePost(ctx, &second); !errors.Is(err, ErrIndependentPagePathConflict) {
		t.Fatalf("save accepted duplicate address: %v", err)
	}
	if err := s.UpdatePost(ctx, p.ID, func(v *Post) error { v.PublicPath = "/renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.NewPost(ctx, &duplicate); err != nil {
		t.Fatalf("released address cannot be reused: %v", err)
	}
}

func TestIndependentPageLookupFailsClosedForManualStateOrDuplicates(t *testing.T) {
	s := independentPageStore(t)
	ctx := context.Background()
	p := newIndependentPage(t, s, "/bewijs.html", "publish")
	if _, err := s.db.ExecContext(ctx, "UPDATE posts SET status='private' WHERE id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPublicPageByPath(ctx, p.PublicPath); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SQL-private document became public: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE posts SET status='publish' WHERE id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	// Manual writes have to publish a new revision just like restore/import.
	if err := s.SetValue(ctx, postsRevisionKey, "manual-state-restored"); err != nil {
		t.Fatal(err)
	}
	p.ID++
	raw, _ := json.Marshal(p)
	if _, err := s.db.ExecContext(ctx, "INSERT INTO posts(id,kind,status,published_at,document) VALUES(?,?,?,?,?)", p.ID, p.Type, p.Status, p.PublishedAt, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPublicPageByPath(ctx, p.PublicPath); !errors.Is(err, ErrNotFound) {
		t.Fatalf("duplicate route picked an arbitrary page: %v", err)
	}
}

func TestIndependentPageBackupCompatibilityAndDuplicateRollback(t *testing.T) {
	s := independentPageStore(t)
	ctx := context.Background()
	p := newIndependentPage(t, s, "/bewijs.html", "publish")
	backup, err := s.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := independentPageStore(t)
	if err = restored.RestoreDatabase(ctx, backup); err != nil {
		t.Fatal(err)
	}
	got, err := restored.GetPublicPageByPath(ctx, p.PublicPath)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("restore lost independent page: %#v %v", got, err)
	}
	legacy := Post{ID: 2, Version: 1, Type: "page", Status: "publish", Title: "关于", ContentHTML: "<p>关于博客</p>", CanonicalURL: "https://www.ganwen.nl/?page_id=2"}
	legacyRaw, _ := json.Marshal(legacy)
	if strings.Contains(string(legacyRaw), "unlisted") || strings.Contains(string(legacyRaw), "public_path") {
		t.Fatal("legacy document acquired independent-page metadata")
	}
	legacyTarget := independentPageStore(t)
	legacyBackup := DatabaseBackup{Format: "ganwen-blog-database", Version: 1, Posts: []Post{legacy}}
	if err = legacyTarget.RestoreDatabase(ctx, legacyBackup); err != nil {
		t.Fatal(err)
	}
	legacyGot, _ := legacyTarget.GetPost(ctx, legacy.ID)
	if !reflect.DeepEqual(legacyGot, legacy) {
		t.Fatal("legacy backup changed after restore")
	}
	duplicate := p
	duplicate.ID++
	backup.Posts = append(backup.Posts, duplicate)
	badTarget := independentPageStore(t)
	if err = badTarget.RestoreDatabase(ctx, backup); !errors.Is(err, ErrIndependentPagePathConflict) {
		t.Fatalf("duplicate backup accepted: %v", err)
	}
	if all, err := badTarget.ListPosts(ctx, true); err != nil || len(all) != 0 {
		t.Fatalf("rejected backup left documents: %v %v", all, err)
	}
	file := filepath.Join(t.TempDir(), "wordpress.json")
	raw, _ := json.Marshal(Export{Posts: []Post{p, duplicate}})
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = badTarget.ImportFile(ctx, file); !errors.Is(err, ErrIndependentPagePathConflict) {
		t.Fatalf("duplicate legacy import accepted: %v", err)
	}
}
