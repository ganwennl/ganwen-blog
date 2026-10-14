package blog

import (
	"html/template"
	"image"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type publicImageCandidate struct {
	URL   string
	Width int
}

type publicImageCacheEntry struct {
	Candidates []publicImageCandidate
	Expires    time.Time
}

var publicImageCache = struct {
	sync.Mutex
	Entries map[string]publicImageCacheEntry
}{Entries: make(map[string]publicImageCacheEntry)}

// A single srcset URL may contain internal commas, including signed query
// parameters. Keep those bytes intact; html/template's string srcset filter
// otherwise treats each comma as a new URL. Whitespace and a trailing comma
// cannot represent the same single URL in the browser's srcset grammar, so
// those images keep the original img src context instead.
func publicSingleImageSrcset(raw string) template.Srcset {
	if raw == "" || strings.ContainsAny(raw, " \t\n\r\f") || strings.HasSuffix(raw, ",") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return template.Srcset(raw)
}

func (a *App) populatePublicImages(view *publicView, settings SiteSettings) {
	thumbnailWidth := 200
	if !view.IsJournal {
		thumbnailWidth = 600
	}
	apply := func(card *publicCard) {
		if card == nil || card.Image == "" {
			return
		}
		if card.ThumbnailURL == "" {
			card.ThumbnailURL = card.Image
		}
		if card.ImageSrcset == "" {
			card.ImageSrcset = publicSingleImageSrcset(card.Image)
		}
		if card.ImageSrcset == "" {
			return
		}
		candidates := publicImageCandidates(a.Config.MediaDir, card.Image, settings.BaseURL)
		if len(candidates) < 2 {
			return // A remote image or one without derivatives keeps its original URL.
		}
		card.ThumbnailURL = candidates[len(candidates)-1].URL
		var sources []string
		for _, candidate := range candidates {
			if publicSingleImageSrcset(candidate.URL) == "" {
				return
			}
			sources = append(sources, candidate.URL+" "+strconv.Itoa(candidate.Width)+"w")
			if card.ThumbnailURL == candidates[len(candidates)-1].URL && candidate.Width >= thumbnailWidth {
				card.ThumbnailURL = candidate.URL
			}
		}
		card.ImageSrcset = template.Srcset(strings.Join(sources, ", "))
	}
	if view.IsOverview && view.IsJournal {
		apply(view.Featured)
		for i := range view.RecentCards {
			apply(&view.RecentCards[i])
		}
	} else if view.IsOverview {
		for i := range view.Groups {
			for j := range view.Groups[i].Cards {
				apply(&view.Groups[i].Cards[j])
			}
		}
	} else if !view.IsArticle {
		for i := range view.ArchiveCards {
			apply(&view.ArchiveCards[i])
		}
	}
}

// Only existing same-origin, same-aspect images inside MediaDir are eligible.
// The small bounded cache also avoids directory scans for images without variants.
func publicImageCandidates(root, rawURL, base string) []publicImageCandidate {
	if root == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	b, baseErr := url.Parse(base)
	if err != nil || baseErr != nil || u.User != nil || (u.Host != "" && !strings.EqualFold(u.Host, b.Host)) {
		return nil
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return nil
	}
	// Queries can transform an image or sign its exact path; media fragments
	// can select coordinates in the original image. Existing files alone do
	// not prove that a different path preserves either URL's meaning.
	if u.RawQuery != "" || u.Fragment != "" {
		return nil
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil
	}
	key := root + "\x00" + rawURL
	now := time.Now()
	publicImageCache.Lock()
	cached, ok := publicImageCache.Entries[key]
	publicImageCache.Unlock()
	if ok && now.Before(cached.Expires) {
		return cached.Candidates
	}
	candidates := readPublicImageCandidates(root, u)
	publicImageCache.Lock()
	if len(publicImageCache.Entries) >= 256 {
		publicImageCache.Entries = make(map[string]publicImageCacheEntry)
	}
	publicImageCache.Entries[key] = publicImageCacheEntry{Candidates: candidates, Expires: now.Add(time.Minute)}
	publicImageCache.Unlock()
	return candidates
}

func readPublicImageCandidates(root string, u *url.URL) []publicImageCandidate {
	file, err := publicMediaPath(root, u.Path)
	if err != nil {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	config, _, decodeErr := image.DecodeConfig(io.LimitReader(f, 1<<20))
	_ = f.Close()
	if decodeErr != nil || config.Width < 1 || config.Height < 1 {
		return nil
	}
	name := path.Base(u.Path)
	extension := path.Ext(name)
	stem := strings.TrimSuffix(strings.TrimSuffix(name, extension), "-scaled")
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		return nil
	}
	variants := make(map[int]publicImageCandidate)
	differences := make(map[int]int64)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), stem+"-") || path.Ext(entry.Name()) != extension {
			continue
		}
		dimensions := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), stem+"-"), extension)
		widthString, heightString, found := strings.Cut(dimensions, "x")
		width, widthErr := strconv.Atoi(widthString)
		height, heightErr := strconv.Atoi(heightString)
		if !found || widthErr != nil || heightErr != nil || width < 1 || height < 1 || width >= config.Width || height > config.Height {
			continue
		}
		// Ignore cropped WordPress thumbnails; rounding one pixel is acceptable.
		difference := int64(width)*int64(config.Height) - int64(height)*int64(config.Width)
		if difference < 0 {
			difference = -difference
		}
		if difference > int64(config.Width)+int64(config.Height) {
			continue
		}
		variant := *u
		variant.Path, variant.RawPath = path.Join(path.Dir(u.Path), entry.Name()), ""
		variantFile, err := publicMediaPath(root, variant.Path)
		if err != nil {
			continue
		}
		info, err := os.Stat(variantFile)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		variantHandle, err := os.Open(variantFile)
		if err != nil {
			continue
		}
		variantConfig, _, decodeErr := image.DecodeConfig(io.LimitReader(variantHandle, 1<<20))
		_ = variantHandle.Close()
		if decodeErr != nil || variantConfig.Width != width || variantConfig.Height != height {
			continue
		}
		// Width descriptors must be unique; prefer the closest original aspect.
		if previous, ok := differences[width]; !ok || difference < previous {
			variants[width] = publicImageCandidate{URL: variant.String(), Width: width}
			differences[width] = difference
		}
	}
	var candidates []publicImageCandidate
	for _, variant := range variants {
		candidates = append(candidates, variant)
	}
	candidates = append(candidates, publicImageCandidate{URL: u.String(), Width: config.Width})
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Width < candidates[j].Width })
	return candidates
}
