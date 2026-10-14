package blog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPublicPlainTextCachePreservesContentAndBounds(t *testing.T) {
	cache := newPublicPlainTextCache(3, 512)
	for _, input := range []string{
		"", "plain 中文 &amp; text", "<p>first <b>bold</b></p><p>second</p>",
		"<script>hidden()</script><style>hidden</style><p>shown</p>",
		"<p>unclosed &lt; &amp; <i>fragment", "<p>edited text</p>",
	} {
		want := parsePublicPlainText(input)
		for i := 0; i < 2; i++ {
			if got := cache.text(input); got != want {
				t.Fatalf("cached parsing changed %q: got %q, want %q", input, got, want)
			}
		}
	}
	if len(cache.entries) != 3 || cache.order.Len() != 3 || cache.bytes > cache.maxBytes {
		t.Fatal("cache did not enforce its entry limit")
	}
	before := cache.bytes
	large := strings.Repeat("<p>large article</p>", 100)
	if got := cache.text(large); got != parsePublicPlainText(large) || cache.bytes != before {
		t.Fatal("oversized article changed output or displaced bounded entries")
	}
	cache = newPublicPlainTextCache(10, 80)
	for i := 0; i < 20; i++ {
		cache.text(fmt.Sprintf("<p>Article number %d</p>", i))
	}
	actual := 0
	for input, text := range cache.entries {
		actual += len(input) + len(text)
	}
	if actual != cache.bytes || actual > 80 || cache.order.Len() != len(cache.entries) {
		t.Fatal("cache did not enforce its retained text limit")
	}
	cache = newPublicPlainTextCache(2, 1<<20)
	spaced := "<p>" + strings.Repeat(" ", 64<<10) + "word" + strings.Repeat(" ", 64<<10) + "</p>"
	if got := cache.text(spaced); got != "word" || cache.entries[spaced] != "word" || cache.bytes != len(spaced)+len("word") {
		t.Fatal("single-word whitespace trimming changed text or retained-byte accounting")
	}
}

func TestPublicPlainTextCacheConcurrentReadersAndEviction(t *testing.T) {
	cache := newPublicPlainTextCache(4, 256)
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				input := fmt.Sprintf("<p>Content %d</p>", (worker+i)%8)
				if got := cache.text(input); got != parsePublicPlainText(input) {
					t.Errorf("concurrent cache returned another article's text: %q", got)
				}
			}
		}(worker)
	}
	wg.Wait()
	if len(cache.entries) > cache.maxEntries || cache.bytes > cache.maxBytes {
		t.Fatal("concurrent inserts exceeded cache limits")
	}
}

func TestPublicTextCacheDoesNotReuseEditedContentOrStaleTranslations(t *testing.T) {
	app := publicTestApp(t)
	before := publicLayoutTestRequest(app, "/en/?p=1447", "journal")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), "Original &amp; content.") {
		t.Fatal("could not warm translated article")
	}
	if err := app.Store.UpdatePost(context.Background(), 1447, func(post *Post) error {
		post.ContentHTML = "<p>updated article unique text</p>"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/?p=1447", "/?s=updated+article+unique+text"} {
		response := publicLayoutTestRequest(app, target, "journal")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "updated article unique text") {
			t.Fatalf("edited content remained stale at %s", target)
		}
	}
	if response := publicLayoutTestRequest(app, "/en/?p=1447", "journal"); response.Code != http.StatusNotFound {
		t.Fatal("cached text made a stale translation public")
	}
}

func TestPublicTrackingQueriesPreserveHomeAndArchiveRendering(t *testing.T) {
	app := publicTestApp(t)
	for _, layout := range []string{"journal", "classic"} {
		for _, target := range []string{"/", "/en/", "/?paged=1", "/?cat=13", "/?s=原文", "/?p=1447"} {
			base := publicLayoutTestRequest(app, target, layout)
			separator := "?"
			if strings.Contains(target, "?") {
				separator = "&"
			}
			for _, tracking := range []string{"utm_source=newsletter&utm_campaign=autumn", "fbclid=example", "unknown=1"} {
				response := publicLayoutTestRequest(app, target+separator+tracking, layout)
				if response.Code != base.Code || response.Body.String() != base.Body.String() {
					t.Fatalf("%s tracking query changed %s content or canonical", layout, target)
				}
			}
		}
	}
}

func BenchmarkPublicPlainText(b *testing.B) {
	input := strings.Repeat("<p>中文文章与性能测试 <strong>article content &amp; formatting</strong> with a useful paragraph.</p>", 100)
	b.Run("parse", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			parsePublicPlainText(input)
		}
	})
	b.Run("cached", func(b *testing.B) {
		cache := newPublicPlainTextCache(512, 16<<20)
		cache.text(input)
		b.ReportAllocs()
		for b.Loop() {
			cache.text(input)
		}
	})
}

func BenchmarkPublicRenderTextCache(b *testing.B) {
	store, err := OpenStore(filepath.Join(b.TempDir(), "benchmark.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	settings := SiteSettings{Title: "甘文", BaseURL: "https://www.ganwen.nl", PostsPerPage: 10}
	if err := store.SaveSettings(ctx, settings); err != nil {
		b.Fatal(err)
	}
	for id := 1; id <= 40; id++ {
		post := Post{ID: int64(id), Type: "post", Status: "publish", Title: fmt.Sprintf("Article %d", id),
			ContentHTML: fmt.Sprintf("<h2>Article %d</h2>", id) + strings.Repeat("<p>中文文章 <strong>searchable content</strong> &amp; performance measurement.</p>", 100),
			PublishedAt: "2026-10-01T12:00:00Z"}
		if err := store.saveLocked(ctx, &post); err != nil {
			b.Fatal(err)
		}
	}
	app := &App{Store: store}
	original := publicTextCache
	defer func() { publicTextCache = original }()
	for _, route := range []struct{ name, target string }{{"home", "/"}, {"search", "/?s=searchable"}, {"article", "/?p=40"}} {
		for _, enabled := range []bool{false, true} {
			name := "uncached"
			if enabled {
				name = "cached"
			}
			b.Run(route.name+"/"+name, func(b *testing.B) {
				publicTextCache = newPublicPlainTextCache(0, 0)
				if enabled {
					publicTextCache = newPublicPlainTextCache(512, 16<<20)
				}
				request := httptest.NewRequest(http.MethodGet, "https://www.ganwen.nl"+route.target, nil)
				request.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "zh"})
				app.handlePublic(httptest.NewRecorder(), request)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					response := httptest.NewRecorder()
					app.handlePublic(response, request)
					if response.Code != http.StatusOK {
						b.Fatal(response.Code)
					}
				}
			})
		}
	}
}
