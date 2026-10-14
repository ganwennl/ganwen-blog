package blog

import (
	_ "embed"
	"encoding/json"
	"strings"
)

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
