package blog

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAdminMediaReturnsNewestBeyondWalkLimit(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "wp-content", "uploads", "2020")
	newDir := filepath.Join(root, "wp-content", "uploads", "2026")
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10001; i++ {
		if err := os.WriteFile(filepath.Join(oldDir, fmt.Sprintf("old-%05d.png", i)), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	modified := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	for _, name := range []string{"b.png", "a.png", "newest.mp4", "ignored.txt"} {
		file := filepath.Join(newDir, name)
		if err := os.WriteFile(file, []byte("fixture"), 0644); err != nil {
			t.Fatal(err)
		}
		stamp := modified
		if name == "newest.mp4" {
			stamp = stamp.Add(500 * time.Millisecond)
		}
		if err := os.Chtimes(file, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	admin := &Admin{app: &App{Config: Config{MediaDir: root}}}
	var previous []mediaItem
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		admin.media(response, httptest.NewRequest(http.MethodGet, "/admin/api/media", nil))
		var items []mediaItem
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &items) != nil || len(items) != 1000 {
			t.Fatalf("invalid bounded media response: status=%d count=%d", response.Code, len(items))
		}
		for i, name := range []string{"newest.mp4", "a.png", "b.png"} {
			if items[i].Name != name {
				t.Fatalf("latest media missing or ordering unstable: index=%d got=%q want=%q", i, items[i].Name, name)
			}
		}
		for _, item := range items {
			if item.Name == "ignored.txt" {
				t.Fatal("unsupported file entered the media list")
			}
		}
		if attempt > 0 && !reflect.DeepEqual(items, previous) {
			t.Fatal("identical media requests returned different ordering")
		}
		previous = items
	}
}

func TestAdminMediaStopsCancelledRequest(t *testing.T) {
	admin := &Admin{app: &App{Config: Config{MediaDir: t.TempDir()}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/admin/api/media", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	admin.media(response, request)
	if response.Body.Len() != 0 {
		t.Fatal("cancelled media scan still returned a completed inventory")
	}
}

func adminSizedPNG(t *testing.T, size int) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	// A legal ancillary text chunk gives an exact file size without allocating
	// or decoding a giant pixel canvas. Keep IEND as the final chunk.
	original := encoded.Bytes()
	chunkSize := size - len(original)
	if chunkSize < 20 {
		t.Fatal("fixture size is too small")
	}
	data := make([]byte, size)
	offset := len(original) - 12
	copy(data, original[:offset])
	chunk := data[offset : offset+chunkSize]
	binary.BigEndian.PutUint32(chunk[:4], uint32(chunkSize-12))
	copy(chunk[4:8], "tEXt")
	copy(chunk[8:], "fixture\x00")
	for i := 16; i < len(chunk)-4; i++ {
		chunk[i] = 'a'
	}
	binary.BigEndian.PutUint32(chunk[len(chunk)-4:], crc32.ChecksumIEEE(chunk[4:len(chunk)-4]))
	copy(data[offset+chunkSize:], original[offset:])
	return data
}

func TestAdminUploadAccepts32MiBBeyondJSONLimitAndCleansTemporaryFiles(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	temporary := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, temporary)
	}
	assertClean := func() {
		t.Helper()
		files, err := filepath.Glob(filepath.Join(temporary, "multipart-*"))
		if err != nil || len(files) != 0 {
			t.Fatalf("multipart temporary files remain: count=%d err=%v", len(files), err)
		}
	}
	imageData := adminSizedPNG(t, 32<<20)
	code, body := c.upload("clipboard.png", imageData, c.csrf)
	var result struct {
		URL   string `json:"url"`
		Bytes int    `json:"bytes"`
	}
	if code != http.StatusCreated || json.Unmarshal(body, &result) != nil || result.Bytes != len(imageData) {
		t.Fatalf("32 MiB image was blocked by an unrelated JSON limit: %d %s", code, body)
	}
	stored, err := os.ReadFile(filepath.Join(c.app.Config.MediaDir, filepath.FromSlash(strings.TrimPrefix(result.URL, "/"))))
	if err != nil || !bytes.Equal(stored, imageData) {
		t.Fatal("large uploaded image bytes were not preserved")
	}
	assertClean()
	if code, _ := c.upload("too-large.png", append(imageData, 0), c.csrf); code != http.StatusBadRequest {
		t.Fatal("upload accepted more than 32 MiB")
	}
	assertClean()
	if code, _ := c.upload("invalid.png", make([]byte, 3<<20), c.csrf); code != http.StatusBadRequest {
		t.Fatal("invalid image content was accepted")
	}
	assertClean()
}

func TestAdminSlimPostPayloadPreservesLargeSourceAndTranslations(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	p := Post{Title: "Original", Status: "private", ContentHTML: "<p>原文</p>", SourceHTML: strings.Repeat("archive", 1<<20), Translations: map[string]Translation{}}
	for _, language := range []string{"en", "nl", "es"} {
		p.Translations[language] = Translation{Title: language, ContentHTML: strings.Repeat(language, 1<<20), Status: "draft"}
	}
	if err := c.app.Store.NewPost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	whole, err := json.Marshal(p)
	if err != nil || len(whole) <= 12<<20 {
		t.Fatal("fixture must exceed the JSON request ceiling as a full detail object")
	}
	input := map[string]any{"id": p.ID, "version": p.Version, "title": "Edited", "status": "private", "content_html": `<p>原文</p><img src="/wp-content/uploads/2026/10/clipboard.png">`, "excerpt_html": "", "categories": []Term{}, "tags": []Term{}, "featured_image": "", "sticky": false, "seo": SEO{}}
	if code, body := c.request(http.MethodPost, "post", input, c.server.URL, c.csrf); code != http.StatusOK {
		t.Fatalf("slim editable-fields payload failed: %d %s", code, body)
	}
	saved, err := c.app.Store.GetPost(ctx, p.ID)
	if err != nil || saved.Title != "Edited" || saved.ContentHTML != input["content_html"] {
		t.Fatal("editable fields were not saved")
	}
	if saved.SourceHTML != p.SourceHTML || !reflect.DeepEqual(saved.Translations, p.Translations) || saved.CanonicalURL != p.CanonicalURL {
		t.Fatal("omitted source archive, translations or canonical URL were overwritten")
	}
}

func TestAdminPostHTMLSizeLimitsStillRejectEmbeddedImageExpansion(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	p := c.newPost("private")
	for _, field := range []string{"content_html", "excerpt_html"} {
		limit := 4 << 20
		if field == "excerpt_html" {
			limit = 1 << 20
		}
		input := map[string]any{"id": p.ID, "version": p.Version, "title": p.Title, "status": p.Status, "content_html": p.ContentHTML, "excerpt_html": p.ExcerptHTML}
		input[field] = `<img src="data:image/png;base64,` + strings.Repeat("A", limit) + `">`
		if code, _ := c.request(http.MethodPost, "post", input, c.server.URL, c.csrf); code != http.StatusBadRequest {
			t.Fatalf("%s exceeded its existing HTML safety limit", field)
		}
		saved, err := c.app.Store.GetPost(context.Background(), p.ID)
		if err != nil || saved.Version != p.Version || saved.ContentHTML != p.ContentHTML || saved.ExcerptHTML != p.ExcerptHTML {
			t.Fatal("failed oversized save modified the article")
		}
	}
}

func TestAdminClipboardBlobFetchCSPIsLimitedToEditorShell(t *testing.T) {
	c := adminTestSetup(t, true)
	for _, path := range []string{"/admin/", "/admin/?embed=1&edit=1"} {
		response, err := c.client.Get(c.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		policy := response.Header.Get("Content-Security-Policy")
		if response.StatusCode != http.StatusOK || !strings.Contains(policy, "connect-src 'self' blob:;") {
			t.Fatalf("%s blocks resolving browser-local pasted images", path)
		}
		if !strings.Contains(policy, "img-src 'self' https: http: data: blob:;") {
			t.Fatal("parent CSP blocks blob image previews inherited by srcdoc")
		}
		if strings.Contains(policy, "frame-src 'self' blob:") {
			t.Fatal("clipboard image CSP broadened frame navigation")
		}
	}
	public := publicTestApp(t)
	response := publicTestRequest(public, "/")
	if strings.Contains(response.Header().Get("Content-Security-Policy"), "blob:") {
		t.Fatal("editor-only blob access leaked into public CSP")
	}
}
