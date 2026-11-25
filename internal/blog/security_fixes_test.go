package blog

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestAdminSettingsAndPostRejectUnsafeImageURLs(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	baseSettings := map[string]any{
		"title":          "Ganwen Blog",
		"description":    "desc",
		"posts_per_page": 10,
	}
	for _, bad := range []string{
		"javascript:alert(1)",
		"JAVASCRIPT:alert(1)",
		"//evil.example/banner.png",
		"/\\evil.example/banner.png",
		"data:image/png;base64,AA==",
		"https://user:pass@example.com/banner.png",
		"https://example.com/" + strings.Repeat("a", 2050),
	} {
		payload := map[string]any{
			"title":          baseSettings["title"],
			"description":    baseSettings["description"],
			"posts_per_page": baseSettings["posts_per_page"],
			"header_image":   bad,
		}
		code, raw := c.request(http.MethodPost, "settings", payload, c.server.URL, c.csrf)
		if code != http.StatusBadRequest || !strings.Contains(string(raw), "页头图片地址不正确") {
			t.Fatalf("header_image %q expected 400, got %d %s", bad, code, raw)
		}

		postPayload := map[string]any{
			"type":           "post",
			"title":          "Test Post",
			"status":         "draft",
			"content_html":   "<p>hi</p>",
			"featured_image": bad,
		}
		code, raw = c.request(http.MethodPost, "create", postPayload, c.server.URL, c.csrf)
		if code != http.StatusBadRequest || !strings.Contains(string(raw), "特色图片地址不正确") {
			t.Fatalf("featured_image %q expected 400, got %d %s", bad, code, raw)
		}
	}

	for _, good := range []string{"", " /wp-content/uploads/2026/11/a.jpg ", "https://cdn.example.com/a.webp"} {
		payload := map[string]any{
			"title":          baseSettings["title"],
			"description":    baseSettings["description"],
			"posts_per_page": baseSettings["posts_per_page"],
			"header_image":   good,
		}
		code, raw := c.request(http.MethodPost, "settings", payload, c.server.URL, c.csrf)
		if code != http.StatusOK {
			t.Fatalf("valid header_image %q failed: %d %s", good, code, raw)
		}
		var saved SiteSettings
		if err := json.Unmarshal(raw, &saved); err != nil || saved.HeaderImage != strings.TrimSpace(good) {
			t.Fatalf("header_image mismatch: got %q want %q", saved.HeaderImage, strings.TrimSpace(good))
		}
	}
}

func TestAdminSettingsPublicStatsToggleErrorMessage(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	code, raw := c.request(http.MethodPost, "settings", map[string]any{
		"title":                "Ganwen Blog",
		"description":          "desc",
		"posts_per_page":       10,
		"public_stats_enabled": "invalid",
	}, c.server.URL, c.csrf)
	if code != http.StatusBadRequest || !strings.Contains(string(raw), "站点统计开关不正确") {
		t.Fatalf("expected stats toggle error, got %d %s", code, raw)
	}
}

func TestConcurrentSettingsAndSidebarUpdatesPreserveBoth(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		var code1, code2 int
		var raw1, raw2 []byte
		go func() {
			defer wg.Done()
			code1, raw1 = c.request(http.MethodPost, "settings", map[string]any{
				"title":          "Updated Title",
				"description":    "Updated Desc",
				"posts_per_page": 12,
			}, c.server.URL, c.csrf)
		}()
		go func() {
			defer wg.Done()
			code2, raw2 = c.request(http.MethodPut, "sidebar", map[string]any{
				"modules":                 []string{"search", "archives"},
				"article_sidebar_enabled": true,
				"sidebar_ads":             map[string]any{"enabled": false},
			}, c.server.URL, c.csrf)
		}()
		wg.Wait()
		if code1 != http.StatusOK || code2 != http.StatusOK {
			t.Fatalf("iter %d failed: settings=%d %s sidebar=%d %s", i, code1, raw1, code2, raw2)
		}
		s, err := c.app.Store.Settings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if s.Title != "Updated Title" || s.PostsPerPage != 12 || !s.ArticleSidebarEnabled || s.SidebarModules == nil || len(*s.SidebarModules) != 2 {
			t.Fatalf("iter %d lost concurrent update: %+v", i, s)
		}
	}
}

func makeMP4Box(kind string, payload []byte, extended bool) []byte {
	if !extended {
		out := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
		copy(out[4:8], kind)
		copy(out[8:], payload)
		return out
	}
	out := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(out[0:4], 1)
	copy(out[4:8], kind)
	binary.BigEndian.PutUint64(out[8:16], uint64(len(out)))
	copy(out[16:], payload)
	return out
}

func TestValidMP4MultiTrackAnd64BitBoxSize(t *testing.T) {
	ftyp := makeMP4Box("ftyp", append([]byte("isom\x00\x00\x02\x00"), []byte("mp41")...), false)
	mvhdPayload := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhdPayload[12:16], 1000) // timescale
	mvhd := makeMP4Box("mvhd", mvhdPayload, false)

	validTrak := makeMP4Box("trak", append(
		makeMP4Box("tkhd", make([]byte, 84), false),
		makeMP4Box("mdia", make([]byte, 16), false)...,
	), false)
	trailingMetaTrak := makeMP4Box("trak", makeMP4Box("tkhd", make([]byte, 84), false), false)
	mdat := makeMP4Box("mdat", []byte{1, 2, 3, 4}, false)

	// Valid video track followed by a metadata-only track must stay valid.
	moovMulti := makeMP4Box("moov", append(append(append([]byte{}, mvhd...), validTrak...), trailingMetaTrak...), false)
	mp4Multi := append(append(append([]byte{}, ftyp...), moovMulti...), mdat...)
	if !validMP4(mp4Multi) {
		t.Fatal("valid MP4 with trailing non-media track was rejected")
	}

	// 64-bit extended box sizes inside moov/trak must be accepted.
	extTrak := makeMP4Box("trak", append(
		makeMP4Box("tkhd", make([]byte, 84), true),
		makeMP4Box("mdia", make([]byte, 16), true)...,
	), true)
	moovExt := makeMP4Box("moov", append(append([]byte{}, makeMP4Box("mvhd", mvhdPayload, true)...), extTrak...), true)
	mp4Ext := append(append(append([]byte{}, ftyp...), moovExt...), mdat...)
	if !validMP4(mp4Ext) {
		t.Fatal("valid MP4 with 64-bit box size inside moov/trak was rejected")
	}

	// Zero-size box extending to end of moov must be accepted.
	zeroTrak := append([]byte{0, 0, 0, 0}, []byte("trak")...)
	zeroTrak = append(zeroTrak, makeMP4Box("tkhd", make([]byte, 84), false)...)
	zeroTrak = append(zeroTrak, makeMP4Box("mdia", make([]byte, 16), false)...)
	moovZero := makeMP4Box("moov", append(append([]byte{}, mvhd...), zeroTrak...), false)
	mp4Zero := append(append(append([]byte{}, ftyp...), moovZero...), mdat...)
	if !validMP4(mp4Zero) {
		t.Fatal("valid MP4 with zero-size trailing trak box was rejected")
	}

	// Only invalid track must be rejected.
	moovBad := makeMP4Box("moov", append(append([]byte{}, mvhd...), trailingMetaTrak...), false)
	mp4Bad := append(append(append([]byte{}, ftyp...), moovBad...), mdat...)
	if validMP4(mp4Bad) {
		t.Fatal("MP4 without any valid media track was accepted")
	}
}

func TestNewPostCanonicalTrimsTrailingSlashBaseURL(t *testing.T) {
	c := adminTestSetup(t, true)
	ctx := context.Background()
	s, err := c.app.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.BaseURL = "https://example.test/"
	if err := c.app.Store.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	p := Post{Type: "post", Title: "Slash check", Status: "publish"}
	if err := c.app.Store.NewPost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.CanonicalURL, "//?p=") || !strings.HasPrefix(p.CanonicalURL, "https://example.test/?p=") {
		t.Fatalf("unexpected canonical URL: %q", p.CanonicalURL)
	}
}

func TestSafePublicHTMLDoesNotDuplicateImgLoadingAttribute(t *testing.T) {
	out := string(safePublicHTMLForLanguage(`<p><img src="/wp-content/uploads/2026/11/a.jpg" loading="eager" alt="pic"></p>`, "https://example.test", "zh"))
	if strings.Count(out, "loading=") != 1 || !strings.Contains(out, `loading="lazy"`) || strings.Contains(out, `loading="eager"`) {
		t.Fatalf("unexpected img loading attribute output: %s", out)
	}
}
