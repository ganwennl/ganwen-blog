package blog

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
)

func publicRequestLanguage(r *http.Request) string {
	lang, _, _ := publicLanguagePath(r.URL.Path)
	return lang
}

//go:embed public_messages.json
var publicMessagesJSON []byte

var publicMessages = func() map[string]map[string]string {
	var messages map[string]map[string]string
	if err := json.Unmarshal(publicMessagesJSON, &messages); err != nil {
		panic("invalid public language messages")
	}
	return messages
}()

func publicMessage(lang, key string) (string, bool) {
	value, ok := publicMessages[lang][key]
	return value, ok && value != ""
}

func publicLanguageDirection(lang string) string {
	if lang == "ar" || lang == "ur" {
		return "rtl"
	}
	return "ltr"
}

func publicAuthorName(name, lang string) string {
	if lang != "zh" && (name == "甘" || name == "甘文" || strings.EqualFold(name, "ganwen")) {
		return "Wen Gan"
	}
	return name
}

// Keep published-language filtering and canonical ordering intact; only the
// current view's already available links move to the front.
func selectedPublicLanguageFirst(links []publicLink, selected string) []publicLink {
	ordered := make([]publicLink, 0, len(links))
	for _, link := range links {
		if link.Lang == selected {
			ordered = append(ordered, link)
		}
	}
	for _, link := range links {
		if link.Lang != selected {
			ordered = append(ordered, link)
		}
	}
	return ordered
}
