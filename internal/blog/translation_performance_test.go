package blog

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestTranslationCommitInvalidatesWarmPostSnapshot(t *testing.T) {
	s, translator, _ := testTranslator(t)
	ctx := context.Background()
	post := testTranslationPost(t, s, "<p>你好甘</p>")
	before, err := s.ListPosts(ctx, false)
	if err != nil || len(before) != 1 || len(before[0].Translations) != 0 {
		t.Fatalf("cannot warm untranslated post snapshot: %v", err)
	}
	testTranslationServer(t, translator, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) { respondTranslation(w, slots) })
	if err := translator.Queue(ctx, post, false); err != nil {
		t.Fatal(err)
	}
	job := processNextTranslation(t, translator)
	after, err := s.ListPosts(ctx, false)
	if err != nil || len(after) != 1 || after[0].Translations[job.Language].ContentHTML == "" {
		t.Fatalf("translation commit left a stale warm post snapshot: %v", err)
	}
	if len(before[0].Translations) != 0 {
		t.Fatal("translation commit mutated an earlier returned snapshot")
	}
}

func TestTranslationDocumentAuthorProtectionAcrossTextSlots(t *testing.T) {
	source := `<p>你好 甘文 (A+)，链接 https://example.test/2026。</p><p>你好 甘文 (A+)，价格 2026.50。</p><pre>你好 甘文 (A+)</pre>`
	document, err := newTranslationDocument(source, false, "content", "甘文 (A+)")
	if err != nil {
		t.Fatal(err)
	}
	if len(document.slots) != 2 {
		t.Fatalf("expected two translated text slots, got %d", len(document.slots))
	}
	values := make(map[string]string, len(document.slots))
	for _, slot := range document.slots {
		values[slot.ID] = strings.ReplaceAll(slot.Text, "你好", "Hello")
	}
	got, err := document.render(values)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(source, "<p>你好", "<p>Hello")
	if got != want {
		t.Fatalf("author, URL, number or protected HTML changed: %s", got)
	}
}

func BenchmarkTranslationDocumentLongArticle(b *testing.B) {
	source := strings.Repeat(`<p>这是一段中文正文，甘文写了关于日常生活的故事，并保留 https://example.test 链接 2026。</p>`, 2400)
	b.SetBytes(int64(len(source)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := newTranslationDocument(source, false, "content", "甘文"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestTranslationRenderProtectsEachOccurrence(t *testing.T) {
	source := `<p>你好 甘文，42 和 42，⟪GW_KEEP_0⟫。</p><p>你好 甘文，42 和 42，⟪GW_KEEP_0⟫。</p>`
	document, err := newTranslationDocument(source, false, "content", "甘文")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "missing", "duplicate", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			values := make(map[string]string, len(document.slots))
			for _, slot := range document.slots {
				values[slot.ID] = strings.ReplaceAll(slot.Text, "你好", "Hello")
			}
			id := document.slots[len(document.slots)-1].ID
			switch mode {
			case "missing":
				values[id] = strings.ReplaceAll(values[id], "⟪GW_KEEP_1⟫", "")
			case "duplicate":
				values[id] += "⟪GW_KEEP_1⟫"
			case "unknown":
				values[id] += "⟪GW_KEEP_999⟫"
			}
			got, err := document.render(values)
			if mode == "valid" {
				if err != nil || got != strings.ReplaceAll(source, "你好", "Hello") {
					t.Fatalf("protected occurrences changed: %q, %v", got, err)
				}
			} else if err == nil || got != "" {
				t.Fatalf("%s protected token accepted: %q, %v", mode, got, err)
			}
		})
	}
}

func BenchmarkTranslationRenderLongArticle(b *testing.B) {
	for _, paragraphs := range []int{1000, 2400, 8000} {
		b.Run(fmt.Sprintf("paragraphs_%d", paragraphs), func(b *testing.B) {
			source := strings.Repeat(`<p>这是一段中文正文，甘文写了关于日常生活的故事，并保留 https://example.test 链接 2026。</p>`, paragraphs)
			document, err := newTranslationDocument(source, false, "content", "甘文")
			if err != nil {
				b.Fatal(err)
			}
			values := make(map[string]string, len(document.slots))
			for _, slot := range document.slots {
				values[slot.ID] = slot.Text
			}
			b.SetBytes(int64(len(source)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := document.render(values); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
