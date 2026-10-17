package blog

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAdminPostSummariesPreserveMetadataAndFullEditorContent(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	for _, input := range []Post{
		{Type: "post", Status: "publish", Title: "公开文章", ContentHTML: "PUBLIC BODY", SourceHTML: "SOURCE BODY", ExcerptHTML: "PUBLIC EXCERPT"},
		{Type: "page", Status: "private", Title: "私密页面", ContentHTML: "PRIVATE BODY", SourceHTML: "PRIVATE SOURCE", ExcerptHTML: "PRIVATE EXCERPT"},
		{Type: "attachment", Status: "publish", Title: "附件", ContentHTML: "ATTACHMENT BODY"},
	} {
		input.Translations = map[string]Translation{"en": {Title: "English title", ContentHTML: "TRANSLATION BODY", ExcerptHTML: "TRANSLATION EXCERPT", Status: "review", SourceHash: SourceHash(input)}}
		if err := c.app.Store.NewPost(ctx, &input); err != nil {
			t.Fatal(err)
		}
	}
	all, err := c.app.Store.ListPosts(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []Post{}
	for _, p := range all {
		if p.Type != "post" && p.Type != "page" {
			continue
		}
		p.ContentHTML, p.SourceHTML, p.ExcerptHTML = "", "", ""
		for language, translation := range p.Translations {
			translation.ContentHTML, translation.ExcerptHTML = "", ""
			p.Translations[language] = translation
		}
		want = append(want, p)
	}
	code, raw := c.request("GET", "posts", nil, "", "")
	var got []Post
	if code != 200 || json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("summary API changed metadata, order or body omission: %d %s", code, raw)
	}
	for _, summary := range got {
		code, raw = c.request("GET", "post?id="+fmtID(summary.ID), nil, "", "")
		var editor struct {
			Post Post `json:"post"`
		}
		if code != 200 || json.Unmarshal(raw, &editor) != nil || editor.Post.ContentHTML == "" || editor.Post.Translations["en"].ContentHTML != "TRANSLATION BODY" {
			t.Fatalf("summary response lost full editor content: %d %s", code, raw)
		}
	}
}

func TestAdminUILongDocumentPerformanceContracts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; browser performance contracts cannot run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, filepath.Join("..", "..", "tests", "client", "test-admin-performance-client.cjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("long document client contracts failed: %v\n%s", err, out)
	}
}
