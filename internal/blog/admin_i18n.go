package blog

import (
	"bytes"
	"embed"
	"encoding/json"
	"net/http"
)

//go:embed admin_i18n.js admin_messages.json
var adminI18nFiles embed.FS

func adminI18nScript() []byte {
	script, err := adminI18nFiles.ReadFile("admin_i18n.js")
	if err != nil {
		panic(err)
	}
	messages, err := adminI18nFiles.ReadFile("admin_messages.json")
	if err != nil || !json.Valid(messages) {
		panic("invalid admin UI messages")
	}
	return bytes.Replace(script, []byte("__UI_MESSAGES__"), messages, 1)
}

var adminI18nJS = adminI18nScript()

func serveAdminI18n(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	if r.Method == http.MethodGet {
		_, _ = w.Write(adminI18nJS)
	}
}
