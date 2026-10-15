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
	if err != nil {
		panic("invalid admin UI messages")
	}
	return expandAdminI18nScript(script, messages)
}

func expandAdminI18nScript(script, messages []byte) []byte {
	if !json.Valid(messages) {
		panic("invalid admin UI messages")
	}
	// This JavaScript also runs inline. A translation must never be able to
	// terminate its script element, even when it contains HTML examples.
	var safeMessages bytes.Buffer
	json.HTMLEscape(&safeMessages, messages)
	return bytes.Replace(script, []byte("__UI_MESSAGES__"), safeMessages.Bytes(), 1)
}

var adminI18nJS = adminI18nScript()

// Keep the UI and its required language bootstrap in one response. A proxy
// challenge or failed secondary asset request must not prevent login startup.
var adminUIPage = adminHTMLWithI18n(adminUI, adminI18nJS)

func adminHTMLWithI18n(page, script []byte) []byte {
	marker := []byte(`<script src="/admin/ui-i18n.js"></script>`)
	if bytes.Count(page, marker) != 1 {
		panic("admin UI language bootstrap marker missing or duplicated")
	}
	inline := make([]byte, 0, len(script)+len("<script></script>"))
	inline = append(inline, "<script>"...)
	inline = append(inline, script...)
	inline = append(inline, "</script>"...)
	return bytes.Replace(page, marker, inline, 1)
}

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
