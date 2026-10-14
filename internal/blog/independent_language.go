package blog

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

const independentEvidenceLanguageQuery = "evidence_lang"

// The archived original is immutable when the page's address or edited content
// changes. Recognize its bilingual containers, rather than a particular URL or
// a marker in the editable page body.
func independentEvidencePage(p Post) bool {
	if publicIndependentPagePath(p, "zh") == "" || !strings.Contains(p.SourceHTML, "lang-zh") || !strings.Contains(p.SourceHTML, "lang-nl") {
		return false
	}
	zh, nl := false, false
	tokens := html.NewTokenizer(strings.NewReader(p.SourceHTML))
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken:
			token := tokens.Token()
			if token.Data != "div" {
				continue
			}
			for _, attr := range token.Attr {
				if attr.Key == "id" {
					zh = zh || attr.Val == "lang-zh"
					nl = nl || attr.Val == "lang-nl"
				}
			}
			if zh && nl {
				return true
			}
		}
	}
}

// Unlike the homepage selector, unsupported languages still participate in the
// preference order: an English-first browser must not select a lower-ranked zh.
func independentEvidenceAcceptedLanguage(header string) string {
	selected, bestQuality := "", -1.0
	for _, item := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(item), ";")
		tag := strings.ToLower(strings.TrimSpace(fields[0]))
		if !independentEvidenceLanguageRange(tag) || len(fields) > 2 {
			continue
		}
		quality, valid := 1.0, true
		if len(fields) == 2 {
			parameter := strings.SplitN(strings.TrimSpace(fields[1]), "=", 2)
			if len(parameter) != 2 || !strings.EqualFold(strings.TrimSpace(parameter[0]), "q") {
				valid = false
			} else {
				var err error
				quality, err = strconv.ParseFloat(strings.TrimSpace(parameter[1]), 64)
				valid = err == nil && !math.IsNaN(quality) && !math.IsInf(quality, 0) && quality > 0 && quality <= 1
			}
		}
		if valid && quality > bestQuality {
			selected, bestQuality = tag, quality
		}
	}
	parts := strings.Split(selected, "-")
	if parts[0] != "zh" {
		return "nl"
	}
	simplifiedScript, simplifiedRegion := false, false
	for _, part := range parts[1:] {
		switch part {
		case "hant":
			return "nl"
		case "hans":
			simplifiedScript = true
		case "cn", "sg":
			simplifiedRegion = true
		}
	}
	// An explicit script describes the writing system even outside CN or SG.
	if simplifiedScript {
		return "zh"
	}
	for _, part := range parts[1:] {
		if part == "tw" || part == "hk" || part == "mo" {
			return "nl"
		}
	}
	if simplifiedRegion {
		return "zh"
	}
	return "nl"
}

func independentEvidenceLanguageRange(tag string) bool {
	if tag == "*" {
		return true
	}
	for i, part := range strings.Split(tag, "-") {
		if len(part) == 0 || len(part) > 8 {
			return false
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (i == 0 || char < '0' || char > '9') {
				return false
			}
		}
	}
	return true
}

// Explicit language URLs and the Chinese switch query beat browser detection.
// This page deliberately ignores the general blog language cookie.
func redirectIndependentEvidenceLanguage(w http.ResponseWriter, r *http.Request, p Post, lang string) bool {
	if lang != "zh" {
		return false
	}
	if _, available := localizedPost(p, "nl"); !available {
		return false
	}
	visitorLanguageVary(w.Header())
	w.Header().Set("Cache-Control", "private, no-store")
	if p.Unlisted {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	values := r.URL.Query()[independentEvidenceLanguageQuery]
	if len(values) == 1 && values[0] == "zh" {
		return false
	}
	if independentEvidenceAcceptedLanguage(strings.Join(r.Header.Values("Accept-Language"), ",")) == "zh" {
		return false
	}
	query := r.URL.Query()
	for _, key := range []string{"p", "page_id", "attachment_id"} {
		query.Del(key)
	}
	target := url.URL{Path: publicIndependentPagePath(p, "nl"), RawQuery: query.Encode(), ForceQuery: r.URL.ForceQuery}
	http.Redirect(w, r, target.String(), http.StatusFound)
	return true
}

func independentEvidenceChineseSwitch(rawURL string) string {
	target, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := target.Query()
	query.Set(independentEvidenceLanguageQuery, "zh")
	target.RawQuery = query.Encode()
	return target.String()
}
