package blog

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

func publicLayout(r *http.Request, settings SiteSettings) string {
	if cookie, err := r.Cookie("ganwen.layout.v1"); err == nil && (cookie.Value == "journal" || cookie.Value == "classic") {
		return cookie.Value
	}
	if settings.DefaultLayout == "classic" {
		return "classic"
	}
	return "journal"
}

func populatePublicNavigation(view *publicView, settings SiteSettings, posts []Post, lang string) {
	for _, p := range posts {
		if _, ok := localizedPost(p, lang); !ok {
			continue
		}
		if p.Type == "post" {
			view.PostCount++
		}
		if p.ID == 2 && p.Type == "page" {
			view.AboutURL = publicPostURL(p, settings, lang)
		}
	}
}

func populatePublicHome(view *publicView, posts []Post, settings SiteSettings, lang string) {
	// Sort a copy so the classic layout retains its existing pinned groups.
	ordered := append([]Post(nil), posts...)
	publicSortByDate(ordered)
	featured := -1
	for i, p := range ordered {
		if p.Sticky {
			featured = i
			break
		}
	}
	if featured < 0 && len(ordered) > 0 {
		featured = 0
	}
	for i, p := range ordered {
		if view.Featured != nil && len(view.RecentCards) >= 8 {
			break
		}
		if i != featured && len(view.RecentCards) >= 8 {
			continue
		}
		local, ok := localizedPost(p, lang)
		if !ok || p.Type != "post" {
			continue
		}
		card := publicPostCard(local, p, settings, lang)
		if i == featured {
			view.Featured = &card
		} else if len(view.RecentCards) < 8 {
			view.RecentCards = append(view.RecentCards, card)
		}
	}
}

func publicPostNewer(a, b Post) bool {
	at, ae := publicTime(a.PublishedAt)
	bt, be := publicTime(b.PublishedAt)
	if (ae == nil) != (be == nil) {
		return ae == nil
	}
	if ae == nil && !at.Equal(bt) {
		return at.After(bt)
	}
	return a.ID > b.ID
}

func publicSortByDate(posts []Post) {
	sort.SliceStable(posts, func(i, j int) bool { return publicPostNewer(posts[i], posts[j]) })
}

func publicTimestamp(raw string) string {
	if value, err := publicTime(raw); err == nil && value.Year() > 0 {
		return value.Format(time.RFC3339)
	}
	return ""
}

func publicModifiedTimestamp(post Post, lang string) string {
	modified := publicTimestamp(post.ModifiedAt)
	if lang != "zh" {
		if translation, ok := post.Translations[lang]; ok {
			updated := publicTimestamp(translation.UpdatedAt)
			mt, me := publicTime(modified)
			ut, ue := publicTime(updated)
			if ue == nil && (me != nil || ut.After(mt)) {
				modified = updated
			}
		}
	}
	mt, me := publicTime(modified)
	pt, pe := publicTime(post.PublishedAt)
	if me != nil || (pe == nil && mt.Before(pt)) {
		return ""
	}
	return modified
}

func publicPostDescription(post Post) string {
	if description := publicPlainText(post.SEO.Description); description != "" {
		return description
	}
	text := publicPlainText(post.ExcerptHTML)
	if text == "" {
		text = publicPlainText(post.ContentHTML)
	}
	runes := []rune(text)
	if len(runes) > 160 {
		text = strings.TrimSpace(string(runes[:160])) + "…"
	}
	return text
}

func publicReadMinutes(content string) int {
	return publicReadMinutesText(publicPlainText(content))
}

func publicReadMinutesText(content string) int {
	// A visible estimate, counting unspaced CJK characters separately from words.
	cjk, words, inWord := 0, 0, false
	for _, r := range content {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			cjk++
			inWord = false
		} else if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if !inWord {
				words++
			}
			inWord = true
		} else {
			inWord = false
		}
	}
	return max(1, (cjk*220+words*400+87999)/88000)
}

func publicAbsoluteURL(raw, base string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	raw = publicMediaReference(strings.TrimSpace(raw), base)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	b, baseErr := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil || baseErr != nil || u.User != nil {
		return ""
	}
	u = b.ResolveReference(u)
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return u.String()
}

func populatePublicSEO(view *publicView, settings SiteSettings) {
	view.OGType, view.SocialCard = "website", "summary"
	if view.IsBlogPost {
		view.OGType = "article"
	}
	if view.Post != nil {
		view.SocialImage = publicAbsoluteURL(view.Post.Image, settings.BaseURL)
		if view.SocialImage != "" {
			view.SocialImageAlt = view.Post.Title
		}
	}
	if view.SocialImage == "" {
		view.SocialImage = publicAbsoluteURL(settings.HeaderImage, settings.BaseURL)
		view.SocialImageAlt = view.SiteTitle
	}
	if view.SocialImage != "" {
		view.SocialCard = "summary_large_image"
	}
	view.SchemaJSON = publicStructuredData(*view, settings)
}

func publicStructuredData(view publicView, settings SiteSettings) template.JS {
	canonical := publicAbsoluteURL(view.Canonical, settings.BaseURL)
	if canonical == "" {
		return ""
	}
	siteID, pageID := view.HomeURL+"#website", canonical+"#webpage"
	website := map[string]any{"@type": "WebSite", "@id": siteID, "url": view.HomeURL, "name": view.SiteTitle, "inLanguage": view.HTMLLang}
	pageType := "CollectionPage"
	if view.IsArticle {
		pageType = "WebPage"
	}
	if view.AboutCurrent {
		pageType = "AboutPage"
	} else if view.IsSearch {
		pageType = "SearchResultsPage"
	}
	page := map[string]any{"@type": pageType, "@id": pageID, "url": canonical, "name": view.Title, "inLanguage": view.HTMLLang, "isPartOf": map[string]string{"@id": siteID}}
	if view.Description != "" {
		page["description"] = view.Description
	}
	graph := []any{website, page}
	if view.IsBlogPost && view.Post != nil {
		articleID := canonical + "#article"
		article := map[string]any{"@type": "BlogPosting", "@id": articleID, "url": canonical, "headline": view.Post.Title, "inLanguage": view.HTMLLang, "mainEntityOfPage": map[string]string{"@id": pageID}}
		page["mainEntity"] = map[string]string{"@id": articleID}
		if view.Description != "" {
			article["description"] = view.Description
		}
		if view.Author != "" {
			article["author"] = map[string]string{"@type": "Person", "name": view.Author}
		}
		if view.Published != "" {
			article["datePublished"] = view.Published
		}
		if view.Modified != "" {
			article["dateModified"] = view.Modified
		}
		if image := publicAbsoluteURL(view.Post.Image, settings.BaseURL); image != "" {
			article["image"] = []string{image}
		}
		if len(view.Post.Categories) > 0 {
			var sections []string
			for _, category := range view.Post.Categories {
				sections = append(sections, category.Label)
			}
			article["articleSection"] = sections
		}
		graph = append(graph, article)
	}
	if view.IsArticle {
		items := []any{map[string]any{"@type": "ListItem", "position": 1, "name": view.SiteTitle, "item": view.HomeURL}}
		if view.IsBlogPost {
			items = append(items, map[string]any{"@type": "ListItem", "position": 2, "name": publicText(view.Lang, "文章", "Articles", "Artikelen", "Artículos"), "item": view.AllArticlesURL})
		}
		items = append(items, map[string]any{"@type": "ListItem", "position": len(items) + 1, "name": view.Heading, "item": canonical})
		breadcrumbID := canonical + "#breadcrumb"
		page["breadcrumb"] = map[string]string{"@id": breadcrumbID}
		graph = append(graph, map[string]any{"@type": "BreadcrumbList", "@id": breadcrumbID, "itemListElement": items})
	}
	// encoding/json escapes HTML delimiters, including </script>, before this
	// value is marked as trusted JSON for html/template's script context.
	encoded, err := json.Marshal(map[string]any{"@context": "https://schema.org", "@graph": graph})
	if err != nil {
		return ""
	}
	return template.JS(encoded)
}

func publicContentSecurityPolicy(schema template.JS) string {
	scripts := "script-src 'self'"
	if schema != "" {
		hash := sha256.Sum256([]byte(schema))
		scripts += " 'sha256-" + base64.StdEncoding.EncodeToString(hash[:]) + "'"
	}
	return "default-src 'self'; " + scripts + "; style-src 'self'; img-src 'self' https: http: data:; media-src 'self' https: http:; frame-src 'self' https://www.youtube.com https://www.youtube-nocookie.com https://player.vimeo.com; base-uri 'none'; object-src 'none'; form-action 'self'"
}

func publicLayoutCachePolicy(header http.Header) {
	var variesByCookie bool
	for _, value := range header.Values("Vary") {
		for _, field := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(field), "Cookie") || strings.TrimSpace(field) == "*" {
				variesByCookie = true
			}
		}
	}
	if !variesByCookie {
		header.Add("Vary", "Cookie")
	}
	if header.Get("Cache-Control") == "" {
		header.Set("Cache-Control", "private, no-cache")
	}
}
