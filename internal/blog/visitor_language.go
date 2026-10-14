package blog

import (
	"math"
	"net/http"
	"strconv"
	"strings"
)

const visitorLanguageCookieName = "ganwen_language"

// redirectVisitorLanguage only negotiates the unfiltered homepage. Old article,
// archive and explicit language URLs never depend on browser preferences.
func (a *App) redirectVisitorLanguage(w http.ResponseWriter, r *http.Request) bool {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.URL.Path != "/" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return false
	}
	// Every negotiable homepage response, including Chinese and crawler visits,
	// must bypass shared caches so one visitor cannot select another's language.
	visitorLanguageVary(w.Header())
	w.Header().Set("Cache-Control", "private, no-store")
	if visitorSearchCrawler(r.UserAgent()) {
		return false
	}

	lang := ""
	if cookie, err := r.Cookie(visitorLanguageCookieName); err == nil {
		candidate := strings.ToLower(strings.TrimSpace(cookie.Value))
		if candidate == "zh" || isTranslationLanguage(candidate) {
			lang = candidate
		}
	}
	if lang == "" {
		lang = visitorAcceptedLanguage(strings.Join(r.Header.Values("Accept-Language"), ","))
	}
	if lang == "zh" {
		return false
	}
	// The target is a fixed allowlisted path; no supplied URL enters Location.
	http.Redirect(w, r, "/"+lang+"/", http.StatusFound)
	return true
}

func visitorAcceptedLanguage(header string) string {
	selected := "zh"
	bestQuality := -1.0
	for _, item := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(item), ";")
		lang := visitorLanguageRange(strings.TrimSpace(fields[0]))
		if lang == "" {
			continue
		}
		quality := 1.0
		valid := true
		if len(fields) > 2 {
			valid = false
		} else if len(fields) == 2 {
			parameter := strings.SplitN(strings.TrimSpace(fields[1]), "=", 2)
			if len(parameter) != 2 || !strings.EqualFold(strings.TrimSpace(parameter[0]), "q") {
				valid = false
			} else {
				var err error
				quality, err = strconv.ParseFloat(strings.TrimSpace(parameter[1]), 64)
				valid = err == nil && !math.IsNaN(quality) && !math.IsInf(quality, 0) && quality > 0 && quality <= 1
			}
		}
		// A strict greater-than preserves browser order when weights are equal.
		if valid && quality > bestQuality {
			selected, bestQuality = lang, quality
		}
	}
	return selected
}

func visitorLanguageRange(tag string) string {
	parts := strings.Split(strings.ToLower(tag), "-")
	primary := parts[0]
	if primary != "zh" && !isTranslationLanguage(primary) {
		return ""
	}
	// Accept valid regional/script variants, but never paths or malformed tags.
	for _, part := range parts {
		if len(part) == 0 || len(part) > 8 {
			return ""
		}
		for _, ch := range part {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
				return ""
			}
		}
	}
	return primary
}

func visitorLanguageVary(header http.Header) {
	var fields []string
	seen := map[string]bool{}
	for _, value := range header.Values("Vary") {
		for _, field := range strings.Split(value, ",") {
			field = strings.TrimSpace(field)
			if field == "*" {
				header.Set("Vary", "*")
				return
			}
			key := strings.ToLower(field)
			if field != "" && !seen[key] {
				fields = append(fields, field)
				seen[key] = true
			}
		}
	}
	for _, field := range []string{"Accept-Language", "Cookie"} {
		if !seen[strings.ToLower(field)] {
			fields = append(fields, field)
		}
	}
	header.Set("Vary", strings.Join(fields, ", "))
}

func visitorSearchCrawler(userAgent string) bool {
	userAgent = strings.ToLower(userAgent)
	for _, marker := range []string{
		"googlebot", "google-inspectiontool", "googleother", "adsbot-google", "mediapartners-google",
		"bingbot", "bingpreview", "msnbot", "slurp", "duckduckbot", "baiduspider",
		"yandexbot", "yandeximages", "yandexwebmaster", "sogou web spider", "sogou spider", "sogou inst spider",
		"360spider", "haosouspider", "applebot", "petalbot", "bytespider",
		"oai-searchbot", "chatgpt-user", "perplexitybot", "perplexity-user",
	} {
		if strings.Contains(userAgent, marker) {
			return true
		}
	}
	return false
}
