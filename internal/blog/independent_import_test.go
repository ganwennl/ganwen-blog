package blog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func independentImportFixture(t *testing.T) (string, independentPagePackage) {
	t.Helper()
	root := t.TempDir()
	source := "<!doctype html><html><body>完整中文原文和荷兰语原文</body></html>"
	p := Post{Type: "page", Status: "publish", PublicPath: "/bewijs.html", Unlisted: true, Slug: "bewijs", Title: "原始证据", ContentHTML: `<h3 id="A1">证据 A1</h3><img src="/proof.jpg"><video controls><source src="/wp-content/uploads/clip.mp4"></video>`, SourceHTML: source}
	p.Translations = map[string]Translation{"nl": {Title: "Origineel bewijs", ContentHTML: `<h3 id="A1">Bewijs A1</h3><img src="/proof.jpg"><a href="#A1">Nummer</a>`, Status: "publish", SourceHash: SourceHash(p)}}
	prepared := independentPagePackage{Schema: 1, SourceURL: "https://www.ganwen.nl/bewijs.html", Post: p, Source: independentPageFile{Path: "source.html", Bytes: int64(len(source)), SHA256: independentBytesHash([]byte(source))}}
	if err := os.WriteFile(filepath.Join(root, "source.html"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"proof.jpg": "original-photo", "wp-content/uploads/clip.mp4": "original-video"} {
		target := filepath.Join(root, "media", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		prepared.Media = append(prepared.Media, independentPageFile{Path: name, Bytes: int64(len(content)), SHA256: independentBytesHash([]byte(content))})
	}
	file := filepath.Join(root, "page.json")
	independentWriteFixture(t, file, prepared)
	return file, prepared
}

func independentWriteFixture(t *testing.T, file string, prepared independentPagePackage) {
	t.Helper()
	raw, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentImportPreservesOriginalsAndIsIdempotent(t *testing.T) {
	file, prepared := independentImportFixture(t)
	data := t.TempDir()
	store, err := OpenStore(filepath.Join(data, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	id, created, err := store.ImportIndependentPageFile(ctx, file, filepath.Join(data, "media"))
	if err != nil || !created || id <= 0 {
		t.Fatalf("initial import: %d %t %v", id, created, err)
	}
	p, err := store.GetPost(ctx, id)
	if err != nil || p.SourceHTML != prepared.Post.SourceHTML || p.ContentHTML != prepared.Post.ContentHTML || p.Translations["nl"].ContentHTML != prepared.Post.Translations["nl"].ContentHTML || !p.Unlisted || p.PublicPath != "/bewijs.html" {
		t.Fatal("import changed originals or publication boundary", err)
	}
	public, err := store.ListPosts(ctx, false)
	if err != nil || len(public) != 0 {
		t.Fatal("unlisted evidence entered discoverable lists", err)
	}
	for _, entry := range prepared.Media {
		if err = independentVerifyFile(filepath.Join(data, "media", filepath.FromSlash(entry.Path)), entry); err != nil {
			t.Fatal("installed original differs", err)
		}
	}
	id2, created, err := store.ImportIndependentPageFile(ctx, file, filepath.Join(data, "media"))
	if err != nil || created || id2 != id {
		t.Fatal("identical import not idempotent", err)
	}
	var jobs int
	if err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM translation_jobs").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("import unexpectedly scheduled translation", err)
	}
	translation := prepared.Post.Translations["nl"]
	translation.ContentHTML += "<p>Changed translation</p>"
	prepared.Post.Translations["nl"] = translation
	independentWriteFixture(t, file, prepared)
	if _, _, err = store.ImportIndependentPageFile(ctx, file, filepath.Join(data, "media")); err == nil || !strings.Contains(err.Error(), "existing page") {
		t.Fatal("different human translation silently replaced managed content", err)
	}
	got, _ := store.GetPost(ctx, id)
	if got.Version != p.Version || got.Translations["nl"].ContentHTML != p.Translations["nl"].ContentHTML {
		t.Fatal("failed import altered managed content")
	}
}

func TestIndependentImportRejectsIncompletePackageBeforePublishing(t *testing.T) {
	for _, mode := range []string{"missing-file", "checksum", "missing-manifest", "unsafe-path", "source-html", "destination-conflict"} {
		t.Run(mode, func(t *testing.T) {
			file, prepared := independentImportFixture(t)
			root := filepath.Dir(file)
			data := t.TempDir()
			store, err := OpenStore(filepath.Join(data, "blog.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			mediaRoot := filepath.Join(data, "media")
			switch mode {
			case "missing-file":
				_ = os.Remove(filepath.Join(root, "media", filepath.FromSlash(prepared.Media[0].Path)))
			case "checksum":
				_ = os.WriteFile(filepath.Join(root, "media", filepath.FromSlash(prepared.Media[0].Path)), []byte("changed"), 0600)
			case "missing-manifest":
				prepared.Media = prepared.Media[:1]
			case "unsafe-path":
				prepared.Media[0].Path = "../proof.jpg"
			case "source-html":
				prepared.Post.SourceHTML += "changed"
			case "destination-conflict":
				_ = os.MkdirAll(mediaRoot, 0700)
				_ = os.WriteFile(filepath.Join(mediaRoot, "proof.jpg"), []byte("keep-existing"), 0600)
			}
			independentWriteFixture(t, file, prepared)
			if _, _, err = store.ImportIndependentPageFile(context.Background(), file, mediaRoot); err == nil {
				t.Fatal("incomplete or conflicting evidence was accepted")
			}
			posts, err := store.ListPosts(context.Background(), true)
			if err != nil || len(posts) != 0 {
				t.Fatal("failed evidence verification published a page", err)
			}
			if mode == "destination-conflict" {
				raw, _ := os.ReadFile(filepath.Join(mediaRoot, "proof.jpg"))
				if string(raw) != "keep-existing" {
					t.Fatal("conflicting old evidence was overwritten")
				}
			}
		})
	}
}

func TestIndependentImportDoesNotRecreatePageAfterAddressWasEdited(t *testing.T) {
	file, _ := independentImportFixture(t)
	data := t.TempDir()
	store, err := OpenStore(filepath.Join(data, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	mediaRoot := filepath.Join(data, "media")
	id, _, err := store.ImportIndependentPageFile(ctx, file, mediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdatePost(ctx, id, func(p *Post) error {
		p.PublicPath = "/updated-evidence.html"
		p.Title = "管理员更新的证据页"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetPost(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.ImportIndependentPageFile(ctx, file, mediaRoot); err == nil || !strings.Contains(err.Error(), "already managed at a changed address") {
		t.Fatal("original package recreated the previous address", err)
	}
	posts, err := store.ListPosts(ctx, true)
	if err != nil || len(posts) != 1 || !independentSamePage(posts[0], before) || posts[0].Version != before.Version {
		t.Fatal("rejected migration changed the managed page", err)
	}
	if _, err = store.GetPublicPageByPath(ctx, "/bewijs.html"); err == nil {
		t.Fatal("previous evidence address was recreated")
	}
	if got, err := store.GetPublicPageByPath(ctx, before.PublicPath); err != nil || got.ID != id {
		t.Fatal("updated evidence address was lost", err)
	}
}
