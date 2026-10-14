package blog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflineSpanishExportImportPreservesOriginalAndOtherLanguages(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := Post{Title: "中文原文", ContentHTML: `<p>正文 <img src="/wp-content/uploads/a.jpg">。</p>`, ExcerptHTML: "<p>摘要</p>", Status: "publish"}
	if err = s.NewPost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	p.Translations["en"] = Translation{Title: "Existing English", ContentHTML: "<p>Existing</p>", SourceHash: SourceHash(p), Status: "publish"}
	if err = s.SavePost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "offline.json")
	if err = ExportOfflineTasks(ctx, s, file); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	var tasks []OfflineTask
	if err = json.Unmarshal(raw, &tasks); err != nil {
		t.Fatal(err)
	}
	languages := map[string]bool{}
	var spanish OfflineTask
	for _, task := range tasks {
		languages[task.Language] = true
		if task.Language == "es" {
			spanish = task
		}
	}
	if len(tasks) != len(translationLanguages()) || !languages["en"] || !languages["nl"] || !languages["es"] {
		t.Fatal("offline export omitted a translation language")
	}
	spanish.Values = map[string]string{}
	for _, slot := range spanish.Slots {
		spanish.Values[slot.ID] = "Español " + slot.Text
	}
	raw, _ = json.Marshal([]OfflineTask{spanish})
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	n, err := ImportOfflineTasks(ctx, s, file, true)
	if err != nil || n != 1 {
		t.Fatalf("Spanish offline import: %d %v", n, err)
	}
	got, err := s.GetPost(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	tr := got.Translations["es"]
	if tr.Status != "publish" || tr.SourceHash != SourceHash(got) || !strings.Contains(tr.ContentHTML, `src="/wp-content/uploads/a.jpg"`) || tr.MachineHash != TranslationContentHash(tr) {
		t.Fatal("Spanish offline import lost publication/media/source integrity")
	}
	if got.CanonicalURL != p.CanonicalURL || got.Title != p.Title || got.ContentHTML != p.ContentHTML || got.Translations["en"].Title != "Existing English" {
		t.Fatal("Spanish import altered original or English")
	}
	if _, err = ImportOfflineTasks(ctx, s, file, true); err == nil {
		t.Fatal("offline import overwrote existing Spanish without review")
	}
	spanish.Language = "de"
	raw, _ = json.Marshal([]OfflineTask{spanish})
	_ = os.WriteFile(file, raw, 0600)
	if _, err = ImportOfflineTasks(ctx, s, file, true); err == nil {
		t.Fatal("unsupported offline language accepted")
	}
}
