package blog

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPublicStaticAssetVersionsAndConditionalRequests(t *testing.T) {
	a := &App{}
	for _, name := range []string{"public.css", "public.js", "public-theme.js"} {
		for _, version := range []string{"", "old-version", publicAssetVersion} {
			request := httptest.NewRequest("GET", "http://localhost/assets/"+name+"?v="+version, nil)
			response := httptest.NewRecorder()
			a.handlePublic(response, request)
			if response.Code != http.StatusOK || response.Body.Len() == 0 || response.Header().Get("ETag") == "" {
				t.Fatalf("%s did not serve a versioned asset", name)
			}
			cache := response.Header().Get("Cache-Control")
			if (version == publicAssetVersion) != strings.Contains(cache, "immutable") {
				t.Fatalf("incorrect freshness for version %q: %s", version, cache)
			}
			etag := response.Header().Get("ETag")
			for _, method := range []string{"GET", "HEAD"} {
				conditional := request.Clone(context.Background())
				conditional.Method = method
				conditional.Header.Set("If-None-Match", "W/"+etag)
				cached := httptest.NewRecorder()
				a.handlePublic(cached, conditional)
				if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 || cached.Header().Get("Cache-Control") != cache {
					t.Fatalf("%s conditional %s = %d/%d", name, method, cached.Code, cached.Body.Len())
				}
			}
			request.Method = "HEAD"
			head := httptest.NewRecorder()
			a.handlePublic(head, request)
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(response.Body.Len()) {
				t.Fatal("asset HEAD lost the matching GET length")
			}
		}
	}
}

func TestPublicHomeCardSelectionStaysBoundedAndPreservesGroups(t *testing.T) {
	settings := SiteSettings{Title: "Blog", BaseURL: "https://www.ganwen.nl", PostsPerPage: 10}
	var posts []Post
	for i := 100; i > 0; i-- {
		posts = append(posts, Post{ID: int64(i), Type: "post", Status: "publish", Title: fmt.Sprint(i), ContentHTML: "<p>Body</p>", PublishedAt: fmt.Sprintf("2026-10-09T%02d:%02d:00Z", i/60, i%60), Categories: []Term{{ID: int64(i % 3), Name: fmt.Sprint(i % 3)}}})
	}
	posts[99].Sticky = true
	var view publicView
	populatePublicHome(&view, posts, settings, "zh")
	if view.Featured == nil || view.Featured.ID != 1 || len(view.RecentCards) != 8 || view.RecentCards[0].ID != 100 || view.RecentCards[7].ID != 93 {
		t.Fatal("bounded selection lost an older pinned article or newest writing")
	}
	groups := publicGroups(posts, settings, "zh", false, 4)
	if len(groups) != 3 {
		t.Fatal("a category disappeared when limiting its displayed cards")
	}
	for _, group := range groups {
		if len(group.Cards) != 4 {
			t.Fatal("classic overview does not show exactly four cards per category")
		}
	}
}

func writePublicImageFixture(t *testing.T, file string, width, height int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	err = jpeg.Encode(f, image.NewGray(image.Rect(0, 0, width, height)), nil)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("image fixture: %v/%v", err, closeErr)
	}
}

func TestPublicResponsiveImagesUseExistingUncroppedVariantsAndFallback(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "wp-content", "uploads", "2026", "10")
	for _, fixture := range []struct {
		name string
		w, h int
	}{
		{"custom-scaled.jpg", 1200, 1600}, {"custom-225x300.jpg", 225, 300},
		{"custom-600x800.jpg", 600, 800}, {"custom-600x799.jpg", 600, 799}, {"custom-150x150.jpg", 150, 150},
	} {
		writePublicImageFixture(t, filepath.Join(directory, fixture.name), fixture.w, fixture.h)
	}
	base := "https://www.ganwen.nl"
	original := base + "/wp-content/uploads/2026/10/custom-scaled.jpg"
	settings := SiteSettings{BaseURL: base}
	app := &App{Config: Config{MediaDir: root}}
	card := publicPostCard(Post{ContentHTML: "<p>Body</p>"}, Post{FeaturedImage: original}, settings, "zh")
	view := publicView{IsOverview: true, IsJournal: true, Featured: &card, RecentCards: []publicCard{card}}
	app.populatePublicImages(&view, settings)
	if !strings.HasSuffix(view.RecentCards[0].ThumbnailURL, "custom-225x300.jpg") || !strings.Contains(string(view.Featured.ImageSrcset), "custom-600x800.jpg 600w") || strings.Count(string(view.Featured.ImageSrcset), " 600w") != 1 || strings.Contains(string(view.Featured.ImageSrcset), "150x150") || view.Featured.Image != original {
		t.Fatal("responsive image lost the original metadata or used a cropped/missing thumbnail")
	}
	classic := publicView{IsOverview: true, Groups: []publicGroup{{Cards: []publicCard{card}}}}
	app.populatePublicImages(&classic, settings)
	if !strings.HasSuffix(classic.Groups[0].Cards[0].ThumbnailURL, "custom-600x800.jpg") {
		t.Fatal("classic card received a tiny journal thumbnail")
	}
	for _, fallback := range []string{base + "/missing.jpg", "https://external.example/wp-content/uploads/2026/10/custom-scaled.jpg", base + "/../custom-scaled.jpg"} {
		fallbackCard := publicPostCard(Post{}, Post{FeaturedImage: fallback}, settings, "zh")
		fallbackView := publicView{IsOverview: true, IsJournal: true, Featured: &fallbackCard}
		app.populatePublicImages(&fallbackView, settings)
		if fallbackView.Featured.ThumbnailURL != fallback || string(fallbackView.Featured.ImageSrcset) != fallback {
			t.Fatal("an image without eligible local variants lost its original URL")
		}
	}
	writePublicImageFixture(t, filepath.Join(directory, "single.jpg"), 640, 480)
	single := publicPostCard(Post{}, Post{FeaturedImage: base + "/wp-content/uploads/2026/10/single.jpg"}, settings, "zh")
	singleView := publicView{IsOverview: true, IsJournal: true, Featured: &single}
	app.populatePublicImages(&singleView, settings)
	if singleView.Featured.ThumbnailURL != single.Image || string(singleView.Featured.ImageSrcset) != single.Image {
		t.Fatal("an original without derivatives did not remain usable")
	}
	queryImage := original + "?token=a,b:signature&v=1"
	queryCard := publicPostCard(Post{}, Post{FeaturedImage: queryImage}, settings, "zh")
	queryView := publicView{IsOverview: true, IsJournal: true, Featured: &queryCard}
	app.populatePublicImages(&queryView, settings)
	if queryView.Featured.ThumbnailURL != queryImage || string(queryView.Featured.ImageSrcset) != queryImage {
		t.Fatal("a path-bound image query was copied onto guessed derivative URLs")
	}
	fragmentImage := original + "#xywh=100,100,600,800"
	fragmentCard := publicPostCard(Post{}, Post{FeaturedImage: fragmentImage}, settings, "zh")
	fragmentView := publicView{IsOverview: true, IsJournal: true, Featured: &fragmentCard}
	app.populatePublicImages(&fragmentView, settings)
	if fragmentView.Featured.ThumbnailURL != fragmentImage || string(fragmentView.Featured.ImageSrcset) != fragmentImage {
		t.Fatal("original media-fragment coordinates were copied onto derivative images")
	}
	for _, fixture := range []struct {
		name string
		w, h int
	}{{"comma,photo-scaled.jpg", 1200, 1600}, {"comma,photo-225x300.jpg", 225, 300}} {
		writePublicImageFixture(t, filepath.Join(directory, fixture.name), fixture.w, fixture.h)
	}
	commaImage := base + "/wp-content/uploads/2026/10/comma,photo-scaled.jpg"
	commaCard := publicPostCard(Post{}, Post{FeaturedImage: commaImage}, settings, "zh")
	commaView := publicView{IsOverview: true, IsJournal: true, Featured: &commaCard}
	app.populatePublicImages(&commaView, settings)
	if !strings.Contains(string(commaView.Featured.ImageSrcset), "comma,photo-225x300.jpg 225w") || !strings.Contains(string(commaView.Featured.ImageSrcset), "comma,photo-scaled.jpg 1200w") {
		t.Fatal("ordinary commas in responsive image URLs were rewritten")
	}
}

func TestPublicEarlyThemeDeferredScriptAndViewportImageSources(t *testing.T) {
	a := publicTestApp(t)
	post, err := a.Store.GetPost(context.Background(), 1447)
	if err != nil {
		t.Fatal(err)
	}
	post.FeaturedImage = "/wp-content/uploads/custom-scaled.jpg"
	if err := a.Store.saveLocked(context.Background(), &post); err != nil {
		t.Fatal(err)
	}
	get := publicLayoutTestRequest(a, "/", "journal")
	for _, node := range publicMetadataNodes(t, get.Body.String(), "script", "") {
		src := publicMetadataAttr(node, "src")
		if strings.HasPrefix(src, "/assets/public.js?") {
			if publicMetadataAttr(node, "defer") == "" {
				found := false
				for _, attr := range node.Attr {
					found = found || attr.Key == "defer"
				}
				if !found {
					t.Fatal("full public script still blocks document parsing")
				}
			}
		}
	}
	if strings.Index(get.Body.String(), "/assets/public-theme.js?") > strings.Index(get.Body.String(), "rel=\"stylesheet\"") {
		t.Fatal("saved theme is applied after styles load")
	}
	sources := publicMetadataNodes(t, get.Body.String(), "source", "")
	if len(sources) != 4 {
		t.Fatal("featured picture does not contain the ordered viewport fallbacks")
	}
	for _, width := range []float64{390, 560, 560.5, 561, 820, 820.5, 1180, 1180.5, 1181, 1800} {
		var selected string
		for _, source := range sources {
			media := publicMetadataAttr(source, "media")
			matches := media == ""
			if !matches {
				if !strings.HasPrefix(media, "(max-width: ") || !strings.HasSuffix(media, "px)") {
					t.Fatalf("unexpected media query %q", media)
				}
				maximum, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(media, "(max-width: "), "px)"), 64)
				if err != nil {
					t.Fatal(err)
				}
				matches = width <= maximum
			}
			if matches {
				selected = publicMetadataAttr(source, "srcset")
				break
			}
		}
		visible := width > 1180 || (width > 560 && width <= 820)
		if visible && selected != post.FeaturedImage || !visible && !strings.HasPrefix(selected, "data:image/svg+xml,") {
			t.Fatalf("viewport %.1f selected %q; visible=%v", width, selected, visible)
		}
	}
	var originalFallback bool
	for _, node := range publicMetadataNodes(t, get.Body.String(), "img", "") {
		originalFallback = originalFallback || publicMetadataAttr(node, "src") == post.FeaturedImage
	}
	if !originalFallback {
		t.Fatal("browsers without picture support lost the original featured image")
	}
	for _, target := range []string{"/", "/?p=1447", "/en/?p=1447", "/?paged=1"} {
		request := httptest.NewRequest(http.MethodHead, "http://localhost"+target, nil)
		request.AddCookie(&http.Cookie{Name: visitorLanguageCookieName, Value: "zh"})
		head := httptest.NewRecorder()
		a.handlePublic(head, request)
		get := publicLayoutTestRequest(a, target, "")
		if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Security-Policy") != get.Header().Get("Content-Security-Policy") || head.Header().Get("Cache-Control") != get.Header().Get("Cache-Control") {
			t.Fatalf("HEAD changed language/layout cache or CSP for %s", target)
		}
	}
}

func TestPublicResponsiveImageURLsPreserveQueriesAndSafeFallbacks(t *testing.T) {
	a := publicTestApp(t)
	post, err := a.Store.GetPost(context.Background(), 1447)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		image, src string
		srcset     bool
	}{
		{"https://cdn.example/image.jpg", "https://cdn.example/image.jpg", true},
		{"https://cdn.example/image.jpg?token=a,b:signature", "https://cdn.example/image.jpg?token=a,b:signature", true},
		{"https://cdn.example/image.jpg?token=a%2Cb:signature&x=1", "https://cdn.example/image.jpg?token=a%2Cb:signature&x=1", true},
		{"https://cdn.example/image.jpg?q=a b", "https://cdn.example/image.jpg?q=a%20b", false},
		{"https://cdn.example/image.jpg?token=value,", "https://cdn.example/image.jpg?token=value,", false},
		{"/wp-content/uploads/comma,.jpg", "/wp-content/uploads/comma,.jpg", true},
		{`https://cdn.example/image.jpg?x="><img/src=x/onerror=alert(1)>`, `https://cdn.example/image.jpg?x=%22%3e%3cimg/src=x/onerror=alert%281%29%3e`, true},
	} {
		post.FeaturedImage = test.image
		if err := a.Store.saveLocked(context.Background(), &post); err != nil {
			t.Fatal(err)
		}
		for _, layout := range []string{"journal", "classic"} {
			response := publicLayoutTestRequest(a, "/", layout)
			if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "ZgotmplZ") {
				t.Fatalf("%s image %q was rejected by template escaping", layout, test.image)
			}
			images := publicMetadataNodes(t, response.Body.String(), "img", "")
			if len(images) != 1 {
				t.Fatalf("image URL %q created extra DOM nodes: %d images", test.image, len(images))
			}
			for _, image := range images {
				if publicMetadataAttr(image, "onerror") != "" {
					t.Fatal("trusted srcset escaped its HTML attribute")
				}
				if publicMetadataAttr(image, "src") != test.src {
					t.Fatalf("%s original image fallback changed URL: %q", layout, test.image)
				}
			}
			if layout == "journal" && test.srcset {
				sources := publicMetadataNodes(t, response.Body.String(), "source", "")
				if len(sources) != 4 || publicMetadataAttr(sources[1], "srcset") != test.image || publicMetadataAttr(sources[3], "srcset") != test.image {
					t.Fatalf("signed image query changed in picture source: %q", test.image)
				}
				continue
			}
			var found bool
			for _, node := range publicMetadataNodes(t, response.Body.String(), "img", "") {
				if publicMetadataAttr(node, "src") != test.src {
					continue
				}
				found = true
				srcset := publicMetadataAttr(node, "srcset")
				if test.srcset && srcset != test.image || !test.srcset && srcset != "" {
					t.Fatalf("%s fallback for %q has srcset %q", layout, test.image, srcset)
				}
			}
			if !found {
				t.Fatalf("%s lost usable original image %q", layout, test.image)
			}
		}
	}
	for _, unsafe := range []string{"javascript:alert(1)", "data:image/svg+xml,unsafe", "https://cdn.example/image.jpg\n 2x, /extra.jpg"} {
		if publicSingleImageSrcset(unsafe) != "" {
			t.Fatalf("unsafe single srcset URL trusted: %q", unsafe)
		}
	}
}
