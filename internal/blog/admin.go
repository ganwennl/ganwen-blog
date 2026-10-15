package blog

import (
	"bytes"
	"container/heap"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:embed admin_ui.html
var adminUI []byte

var errCredentials = errors.New("用户名或密码不正确")
var errBusy = errors.New("请求过多，请稍后重试")

type Admin struct {
	app  *App
	auth *adminAuth
}

func NewAdmin(a *App) *Admin {
	auth := newAdminAuth(a.Config)
	auth.store = a.Store
	return &Admin{app: a, auth: auth}
}
func (a *Admin) Register(mux *http.ServeMux) {
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin/", http.StatusSeeOther) })
	mux.HandleFunc("/admin/", a.serve)
}
func adminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func adminError(w http.ResponseWriter, status int, message string) {
	adminJSON(w, status, map[string]string{"error": message})
}
func storeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrIndependentPageInvalid) || errors.Is(err, ErrIndependentPagePathConflict) {
		adminError(w, 400, err.Error())
	} else if errors.Is(err, ErrConflict) {
		adminError(w, 409, "内容已被其他操作更新，请重新载入后编辑")
	} else if errors.Is(err, ErrNotFound) {
		adminError(w, 404, "文章不存在")
	} else {
		adminError(w, 500, "保存失败，请检查服务日志")
	}
}
func readAdminJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		adminError(w, 415, "请使用 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		adminError(w, 400, "请求格式不正确或内容过大")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		adminError(w, 400, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
func (a *Admin) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.URL.Path == "/admin/ui-i18n.js" {
		serveAdminI18n(w, r)
		return
	}
	if r.URL.Path == "/admin/api/editor-session" && r.Method == http.MethodGet {
		w.Header().Set("Vary", "Cookie")
		_, authenticated, err := a.auth.sessionState(r)
		if err != nil {
			adminError(w, 503, "暂时无法读取登录状态，请稍后重试")
			return
		}
		adminJSON(w, 200, map[string]bool{"authenticated": authenticated})
		return
	}
	if r.URL.Path == "/admin/" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		ancestors := "'none'"
		if r.URL.Query().Get("embed") == "1" {
			ancestors = "'self'"
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' https: http: data: blob:; frame-src 'self'; connect-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors "+ancestors)
		_, _ = w.Write(adminUIPage)
		return
	}
	if r.URL.Path == "/admin/api/session" && r.Method == http.MethodGet {
		s, ok, err := a.auth.sessionState(r)
		if err != nil {
			adminError(w, 503, "暂时无法读取登录状态，请稍后重试")
			return
		}
		if ok {
			adminJSON(w, 200, map[string]any{"authenticated": true, "csrf": s.CSRF, "user": a.app.Config.AdminUser, "configured": configuredAdmin(a.app.Config)})
			return
		}
		csrf, err := a.auth.issueLogin(w, r, a.app.Config)
		if err != nil {
			adminError(w, 503, "暂时无法登录")
			return
		}
		adminJSON(w, 200, map[string]any{"authenticated": false, "csrf": csrf, "configured": configuredAdmin(a.app.Config)})
		return
	}
	if r.URL.Path == "/admin/api/login" && r.Method == http.MethodPost {
		a.login(w, r)
		return
	}
	s, ok, err := a.auth.sessionState(r)
	if err != nil {
		adminError(w, 503, "暂时无法读取登录状态，请稍后重试")
		return
	}
	if !ok {
		adminError(w, 401, "请先登录")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if r.Method != http.MethodPost {
			adminError(w, 405, "此接口仅允许 POST")
			return
		}
		if !sameOrigin(r, a.app.Config) || !sameToken(r.Header.Get("X-CSRF-Token"), s.CSRF) {
			adminError(w, 403, "请求来源或 CSRF 验证失败")
			return
		}
	}
	switch r.URL.Path {
	case "/admin/api/analytics/geoip/update", "/admin/api/analytics/geoip/update-v6":
		if r.Method == http.MethodPost {
			a.app.Analytics.handleGeoUpdate(w, r, r.URL.Path == "/admin/api/analytics/geoip/update-v6")
			return
		}
	case "/admin/api/analytics":
		if r.Method == http.MethodGet {
			a.app.Analytics.handleReport(w, r)
			return
		}
	case "/admin/api/logout":
		if r.Method != http.MethodPost {
			break
		}
		if err := a.auth.logout(w, r, a.app.Config); err != nil {
			adminError(w, 503, "暂时无法退出登录，请稍后重试")
			return
		}
		adminJSON(w, 200, map[string]bool{"ok": true})
		return
	case "/admin/api/posts":
		if r.Method != http.MethodGet {
			break
		}
		posts, err := a.app.Store.ListAdminPosts(r.Context())
		if err != nil {
			storeError(w, err)
			return
		}
		adminJSON(w, 200, posts)
		return
	case "/admin/api/post":
		if r.Method == http.MethodGet {
			a.getPost(w, r)
			return
		}
		if r.Method == http.MethodPost {
			a.savePost(w, r, false)
			return
		}
	case "/admin/api/create":
		if r.Method == http.MethodPost {
			a.savePost(w, r, true)
			return
		}
	case "/admin/api/translation":
		if r.Method == http.MethodPost {
			a.saveTranslation(w, r)
			return
		}
	case "/admin/api/translate":
		if r.Method == http.MethodPost {
			a.queueTranslation(w, r)
			return
		}
	case "/admin/api/queue":
		if r.Method == http.MethodGet {
			if a.app.Translator == nil {
				adminJSON(w, 200, []any{})
				return
			}
			jobs, err := a.app.Translator.Status(r.Context())
			if err != nil {
				adminError(w, 500, "无法读取翻译队列")
				return
			}
			adminJSON(w, 200, jobs)
			return
		}
	case "/admin/api/translation-config":
		if r.Method == http.MethodGet || r.Method == http.MethodPost {
			a.translationConfig(w, r)
			return
		}
	case "/admin/api/settings":
		if r.Method == http.MethodGet || r.Method == http.MethodPost {
			a.settings(w, r)
			return
		}
	case "/admin/api/footer/translate":
		if r.Method == http.MethodPost {
			a.translateFooter(w, r)
			return
		}
	case "/admin/api/upload":
		if r.Method == http.MethodPost {
			a.upload(w, r)
			return
		}
	case "/admin/api/media":
		if r.Method == http.MethodGet {
			a.media(w, r)
			return
		}
	case "/admin/preview":
		if r.Method == http.MethodGet {
			a.preview(w, r)
			return
		}
	}
	adminError(w, 404, "接口不存在或请求方法不正确")
}
func (a *Admin) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, a.app.Config) || !a.auth.loginCSRF(r) {
		adminError(w, 403, "登录请求已失效，请刷新页面后重试")
		return
	}
	if !configuredAdmin(a.app.Config) {
		adminError(w, 503, "暂时无法登录")
		return
	}
	if !a.auth.rateAllowed(r) {
		w.Header().Set("Retry-After", "900")
		adminError(w, 429, "登录尝试过多，请 15 分钟后重试")
		return
	}
	var v struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if !readAdminJSON(w, r, &v) {
		return
	}
	if len(v.Username) > 256 || len(v.Password) > 1024 {
		adminError(w, 400, "账号或密码过长")
		return
	}
	csrf, err := a.auth.authenticate(w, r, a.app.Config, v.Username, v.Password, v.Remember)
	if err != nil {
		if errors.Is(err, errCredentials) {
			adminError(w, 401, err.Error())
		} else {
			adminError(w, 503, "暂时无法登录")
		}
		return
	}
	adminJSON(w, 200, map[string]any{"authenticated": true, "csrf": csrf, "user": a.app.Config.AdminUser})
}
func postID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}
func adminPostResponse(p Post) map[string]any {
	sourceHash := SourceHash(p)
	languages := make([]string, 0, len(blogLanguages))
	for _, language := range publicLanguages() {
		if _, visible := localizedPostWithSourceHash(p, language, sourceHash); visible {
			languages = append(languages, language)
		}
	}
	return map[string]any{"post": p, "source_hash": sourceHash, "public_languages": languages}
}

func (a *Admin) getPost(w http.ResponseWriter, r *http.Request) {
	id, err := postID(r)
	if err != nil {
		adminError(w, 400, "文章编号不正确")
		return
	}
	p, err := a.app.Store.GetPost(r.Context(), id)
	if err != nil {
		storeError(w, err)
		return
	}
	adminJSON(w, 200, adminPostResponse(p))
}
func validAdminPost(p Post) error {
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 2000 {
		return errors.New("请填写标题，最多 2000 字节")
	}
	switch p.Status {
	case "draft", "publish", "private", "pending", "future":
	default:
		return errors.New("文章状态不正确")
	}
	if len(p.ContentHTML) > 4<<20 || len(p.ExcerptHTML) > 1<<20 {
		return errors.New("正文或摘要过大")
	}
	if len(p.Categories) > 100 || len(p.Tags) > 100 {
		return errors.New("分类或标签过多")
	}
	if len(p.SEO.Title) > 2000 || len(p.SEO.Description) > 8000 {
		return errors.New("SEO 内容过长")
	}
	for _, terms := range [][]Term{p.Categories, p.Tags} {
		for _, t := range terms {
			if len(t.Name) > 1000 || len(t.Slug) > 1000 || len(t.URL) > 4000 {
				return errors.New("分类或标签过长")
			}
		}
	}
	return nil
}
func (a *Admin) savePost(w http.ResponseWriter, r *http.Request, create bool) {
	var raw json.RawMessage
	if !readAdminJSON(w, r, &raw) {
		return
	}
	var input Post
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil || json.Unmarshal(raw, &fields) != nil || fields == nil {
		adminError(w, 400, "请求格式不正确或内容过大")
		return
	}
	if err := validAdminPost(input); err != nil {
		adminError(w, 400, err.Error())
		return
	}
	p := input
	changed := true
	if !create {
		old, err := a.app.Store.GetPost(r.Context(), input.ID)
		if err != nil {
			storeError(w, err)
			return
		}
		p = old
		p.Version = input.Version
		p.Title = input.Title
		p.ContentHTML = input.ContentHTML
		p.ExcerptHTML = input.ExcerptHTML
		p.Status = input.Status
		// Old clients editing a page must preserve its address and visibility.
		if _, supplied := fields["public_path"]; supplied {
			p.PublicPath = input.PublicPath
		}
		if _, supplied := fields["unlisted"]; supplied {
			p.Unlisted = input.Unlisted
		}
		p.Categories = input.Categories
		p.Tags = input.Tags
		p.FeaturedImage = input.FeaturedImage
		p.Sticky = input.Sticky
		p.SEO = input.SEO
		p.ModifiedAt = time.Now().UTC().Format(time.RFC3339)
		changed = SourceHash(old) != SourceHash(p) || old.Status != p.Status
	} else {
		if input.Type != "" && input.Type != "post" && input.Type != "page" {
			adminError(w, 400, "请选择文章或页面")
			return
		}
		p.ID = 0
		p.Version = 0
		p.CanonicalURL = ""
		p.Type = "post"
		if input.Type == "page" {
			p.Type = "page"
		}
		p.Translations = nil
		p.SourceHTML = p.ContentHTML
		p.Author = a.app.Config.AdminUser
		p.PublishedAt = time.Now().UTC().Format(time.RFC3339)
		p.ModifiedAt = p.PublishedAt
	}
	var err error
	if create {
		err = a.app.Store.NewPost(r.Context(), &p)
	} else {
		err = a.app.Store.SavePost(r.Context(), &p)
	}
	if err != nil {
		storeError(w, err)
		return
	}
	warning := ""
	if changed && a.app.Translator != nil {
		if err := a.app.Translator.QueueAutomatic(r.Context(), p); err != nil {
			warning = "文章已保存，翻译排队失败，请稍后重新加入队列"
		}
	}
	result := adminPostResponse(p)
	result["warning"] = warning
	adminJSON(w, 200, result)
}
func (a *Admin) saveTranslation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID          int64       `json:"id"`
		Version     int64       `json:"version"`
		Language    string      `json:"language"`
		Translation Translation `json:"translation"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if !isTranslationLanguage(input.Language) {
		adminError(w, 400, "译文支持英语、荷兰语和西班牙语")
		return
	}
	t := input.Translation
	if t.Status != "review" && t.Status != "publish" && t.Status != "draft" {
		adminError(w, 400, "译文状态不正确")
		return
	}
	if strings.TrimSpace(t.Title) == "" || len(t.Title) > 2000 || len(t.ContentHTML) > 8<<20 || len(t.ExcerptHTML) > 1<<20 || len(t.SEO.Title) > 2000 || len(t.SEO.Description) > 8000 {
		adminError(w, 400, "译文为空或超过长度限制")
		return
	}
	p, err := a.app.Store.GetPost(r.Context(), input.ID)
	if err != nil {
		storeError(w, err)
		return
	}
	if p.Version != input.Version {
		storeError(w, ErrConflict)
		return
	}
	if t.Status == "publish" && t.SourceHash != SourceHash(p) {
		adminError(w, 409, "原文已更改，请核对译文后确认与当前原文一致")
		return
	}
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if p.Translations == nil {
		p.Translations = make(map[string]Translation)
	}
	p.Translations[input.Language] = t
	if err = a.app.Store.SavePost(r.Context(), &p); err != nil {
		storeError(w, err)
		return
	}
	adminJSON(w, 200, adminPostResponse(p))
}
func (a *Admin) queueTranslation(w http.ResponseWriter, r *http.Request) {
	var v struct {
		ID    int64 `json:"id"`
		Force bool  `json:"force"`
	}
	if !readAdminJSON(w, r, &v) {
		return
	}
	p, err := a.app.Store.GetPost(r.Context(), v.ID)
	if err != nil {
		storeError(w, err)
		return
	}
	if a.app.Translator == nil {
		adminError(w, 503, "翻译服务尚未启用")
		return
	}
	if err = a.app.Translator.Queue(r.Context(), p, v.Force); err != nil {
		if errors.Is(err, errTranslationDisabled) {
			adminError(w, 400, "外部翻译已暂停。当前为仅人工翻译模式；可手动编辑译文，或在翻译设置中配置所需服务并启用手动或自动模式。")
			return
		}
		adminError(w, 400, "翻译服务未配置或当前无法加入队列")
		return
	}
	adminJSON(w, 200, map[string]bool{"ok": true})
}
func (a *Admin) translationConfig(w http.ResponseWriter, r *http.Request) {
	if a.app.Translator == nil {
		adminError(w, 503, "翻译服务尚未启用")
		return
	}
	if r.Method == http.MethodGet {
		c, err := a.app.Translator.Configuration(r.Context())
		if err != nil {
			adminError(w, 500, "无法读取翻译配置")
			return
		}
		c.APIKey = ""
		adminJSON(w, 200, c)
		return
	}
	var c TranslationConfig
	if !readAdminJSON(w, r, &c) {
		return
	}
	if len(c.Endpoint) > 4000 || len(c.Model) > 256 || len(c.APIKey) > 8192 {
		adminError(w, 400, "配置内容过长")
		return
	}
	if err := a.app.Translator.Configure(r.Context(), c); err != nil {
		adminError(w, 400, "翻译配置不正确，请检查服务地址和模型")
		return
	}
	adminJSON(w, 200, map[string]bool{"ok": true})
}
func (a *Admin) settings(w http.ResponseWriter, r *http.Request) {
	current, err := a.app.Store.Settings(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		dailyViews := publicDailyDisplayTarget(current)
		current.PublicDailyViews = &dailyViews
		virtualViews := publicVirtualViewsEnabled(current)
		current.PublicVirtualViewsEnabled = &virtualViews
		showStats := publicStatsEnabled(current)
		current.PublicStatsEnabled = &showStats
		if current.SidebarAds.IntervalSeconds == 0 {
			current.SidebarAds.IntervalSeconds = 8
		}
		if current.SidebarAds.Items == nil {
			current.SidebarAds.Items = []SidebarAd{}
		}
		adminJSON(w, 200, current)
		return
	}
	var input struct {
		SiteSettings
		AvatarURL                 *string                       `json:"avatar_url"`
		DefaultLayout             json.RawMessage               `json:"default_layout"`
		ArticleSidebarEnabled     json.RawMessage               `json:"article_sidebar_enabled"`
		FooterText                json.RawMessage               `json:"footer_text"`
		FooterTranslations        *map[string]FooterTranslation `json:"footer_translations"`
		SidebarAds                json.RawMessage               `json:"sidebar_ads"`
		PublicDailyViews          json.RawMessage               `json:"public_daily_views"`
		PublicVirtualViewsEnabled json.RawMessage               `json:"public_virtual_views_enabled"`
		PublicStatsEnabled        json.RawMessage               `json:"public_stats_enabled"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	s := input.SiteSettings
	if strings.TrimSpace(s.Title) == "" || len(s.Title) > 1000 || len(s.Description) > 8000 || s.PostsPerPage < 1 || s.PostsPerPage > 100 {
		adminError(w, 400, "站点标题、描述或分页设置不正确")
		return
	}
	if s.ContactLinks != nil {
		links, err := validateContactLinks(s.ContactLinks)
		if err != nil {
			adminError(w, 400, err.Error())
			return
		}
		current.ContactLinks = links
	}
	if input.AvatarURL != nil {
		avatar := strings.TrimSpace(*input.AvatarURL)
		if avatar != "" && !validAvatarURL(avatar) {
			adminError(w, 400, "头像请使用上传目录内的图片，或完整 HTTPS 图片地址")
			return
		}
		current.AvatarURL = avatar
	}
	current.Title = s.Title
	current.Description = s.Description
	current.PostsPerPage = s.PostsPerPage
	current.HeaderImage = s.HeaderImage
	if len(input.ArticleSidebarEnabled) > 0 {
		var enabled *bool
		if err := json.Unmarshal(input.ArticleSidebarEnabled, &enabled); err != nil || enabled == nil {
			adminError(w, 400, "文章侧栏开关不正确")
			return
		}
		current.ArticleSidebarEnabled = *enabled
	}
	if len(input.PublicVirtualViewsEnabled) > 0 {
		enabled, err := parsedPublicVirtualViewsEnabled(input.PublicVirtualViewsEnabled)
		if err != nil {
			adminError(w, 400, err.Error())
			return
		}
		current.PublicVirtualViewsEnabled = enabled
	}
	if len(input.PublicStatsEnabled) > 0 {
		enabled, err := parsedPublicVirtualViewsEnabled(input.PublicStatsEnabled)
		if err != nil {
			adminError(w, 400, err.Error())
			return
		}
		current.PublicStatsEnabled = enabled
	}
	if len(input.PublicDailyViews) > 0 {
		dailyViews, err := parsedPublicDailyViews(input.PublicDailyViews)
		if err != nil {
			adminError(w, 400, err.Error())
			return
		}
		current.PublicDailyViews = dailyViews
	}
	if len(input.SidebarAds) > 0 {
		ads, err := patchedSidebarAds(current.SidebarAds, input.SidebarAds)
		if err != nil {
			adminError(w, 400, err.Error())
			return
		}
		current.SidebarAds = ads
	}
	if len(input.DefaultLayout) > 0 {
		var layout *string
		if err := json.Unmarshal(input.DefaultLayout, &layout); err != nil || layout == nil || (*layout != "" && *layout != "journal" && *layout != "classic") {
			adminError(w, 400, "请选择日志阅读或经典卡片作为默认版式")
			return
		}
		current.DefaultLayout = *layout
		if current.DefaultLayout == "" {
			current.DefaultLayout = "journal"
		}
	}
	if len(input.FooterText) > 0 {
		var footer *string
		if err := json.Unmarshal(input.FooterText, &footer); err != nil || (footer != nil && !validFooterText(*footer)) {
			adminError(w, 400, "页脚请使用不超过 4000 字的纯文本")
			return
		}
		current.FooterText = footer
	}
	if input.FooterTranslations != nil {
		translations, err := validatedFooterTranslations(*input.FooterTranslations, current)
		if err != nil {
			adminError(w, 400, err.Error())
			return
		}
		current.FooterTranslations = translations
	}
	if err = a.app.Store.SaveSettings(r.Context(), current); err != nil {
		storeError(w, err)
		return
	}
	adminJSON(w, 200, current)
}

func validAvatarURL(raw string) bool {
	if raw == "" || len(raw) > 2048 || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 || strings.ContainsAny(raw, "\\#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	if u.Scheme == "" {
		if u.Host != "" || !strings.HasPrefix(raw, "/wp-content/uploads/") || !strings.HasPrefix(u.Path, "/wp-content/uploads/") {
			return false
		}
	} else if !validContactLink(ContactLink{Kind: "website", URL: raw}) {
		return false
	}
	if path.Clean(u.Path) != u.Path || strings.Contains(u.Path, "\\") {
		return false
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || strings.IndexFunc(decoded, unicode.IsControl) >= 0 || strings.Contains(decoded, "\\") {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif":
		return true
	}
	return false
}

func validContactKind(kind string) bool {
	switch kind {
	case "x", "email", "telegram", "github", "mastodon", "website", "instagram", "youtube", "facebook", "linkedin":
		return true
	}
	return false
}

// Shared with public rendering so old or imported settings cannot bypass URL checks.
func validContactLink(link ContactLink) bool {
	if !validContactKind(link.Kind) || utf8.RuneCountInString(link.Label) > 80 || len(link.URL) == 0 || len(link.URL) > 2048 {
		return false
	}
	if strings.IndexFunc(link.Label, unicode.IsControl) >= 0 || strings.IndexFunc(link.URL, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 || strings.Contains(link.URL, "\\") {
		return false
	}
	u, err := url.Parse(link.URL)
	if err != nil || u.User != nil {
		return false
	}
	if link.Kind == "email" {
		if u.Scheme != "mailto" || u.Host != "" || u.Path != "" || u.Opaque == "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(link.URL, "#") {
			return false
		}
		address, err := url.PathUnescape(u.Opaque)
		if err != nil || strings.ContainsAny(address, "\"<>(),;:\\") || strings.IndexFunc(address, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
			return false
		}
		parsed, err := mail.ParseAddress(address)
		return err == nil && parsed.Name == "" && parsed.Address == address
	}
	if u.Scheme != "https" || !u.IsAbs() || u.Opaque != "" || u.Host == "" || u.Hostname() == "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	decoded, err := url.PathUnescape(link.URL)
	return err == nil && strings.IndexFunc(decoded, unicode.IsControl) < 0
}

func validateContactLinks(input []ContactLink) ([]ContactLink, error) {
	if len(input) > 12 {
		return nil, errors.New("联系方式最多 12 条")
	}
	links := make([]ContactLink, 0, len(input))
	seen := make(map[string]bool, len(input))
	for _, link := range input {
		if !validContactKind(link.Kind) {
			return nil, errors.New("联系方式类型不正确")
		}
		link.Label = strings.TrimSpace(link.Label)
		link.URL = strings.TrimSpace(link.URL)
		if utf8.RuneCountInString(link.Label) > 80 || len(link.URL) > 2048 || strings.IndexFunc(link.Label, unicode.IsControl) >= 0 {
			return nil, errors.New("联系标签最多 80 字，地址最多 2048 字节")
		}
		if link.URL == "" {
			continue
		}
		if !validContactLink(link) {
			return nil, errors.New("联系方式请使用完整 HTTPS 地址；邮箱仅允许 mailto: 单个邮箱，不含附加参数")
		}
		identity := contactIdentity(link.URL)
		if seen[identity] {
			return nil, errors.New("联系方式地址重复")
		}
		seen[identity] = true
		links = append(links, link)
	}
	return links, nil
}
func (a *Admin) preview(w http.ResponseWriter, r *http.Request) {
	id, err := postID(r)
	if err != nil {
		adminError(w, 400, "文章编号不正确")
		return
	}
	p, err := a.app.Store.GetPost(r.Context(), id)
	if err != nil {
		storeError(w, err)
		return
	}
	title, body := p.Title, p.ContentHTML
	lang := r.URL.Query().Get("lang")
	if lang == "" || lang == "zh" {
		lang = "zh-Hans"
	} else if isTranslationLanguage(lang) {
		tr, ok := p.Translations[lang]
		if !ok {
			adminError(w, 404, "尚未保存该语言译文")
			return
		}
		title, body = tr.Title, tr.ContentHTML
	} else {
		adminError(w, 400, "不支持该语言")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin; default-src 'none'; img-src 'self' https: http: data:; media-src 'self' https: http:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	_, _ = fmt.Fprintf(w, "<!doctype html><html lang=\"%s\"><meta charset=\"utf-8\"><meta name=\"robots\" content=\"noindex,nofollow\"><meta name=\"viewport\" content=\"width=device-width\"><title>%s</title><style>body{max-width:850px;margin:48px auto;padding:0 24px;font:18px/1.8 system-ui;color:#222}img,video{max-width:100%%;height:auto}h1{font-size:32px}.text-align-left,.has-text-align-left{text-align:left}.text-align-center,.has-text-align-center{text-align:center}.text-align-right,.has-text-align-right{text-align:right}.text-align-justify,.has-text-align-justify{text-align:justify}.text-weight-normal{font-weight:400}.text-weight-bold{font-weight:700}.text-style-normal{font-style:normal}.text-style-italic{font-style:italic}.text-decoration-none{text-decoration:none}.text-decoration-underline{text-decoration:underline}.text-decoration-line-through{text-decoration:line-through}</style><h1>%s</h1><article>%s</article></html>", lang, htmlEscape(title), htmlEscape(title), body)
}
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
	return r.Replace(s)
}

func uploadKind(data []byte, name string) (string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".gif" && ext != ".webp" && ext != ".mp4" {
		return "", errors.New("仅支持 PNG、JPEG、GIF、WebP 和 MP4")
	}
	mime := http.DetectContentType(data)
	want := ""
	switch mime {
	case "image/png":
		want = ".png"
	case "image/jpeg":
		want = ".jpg"
	case "image/gif":
		want = ".gif"
	case "image/webp":
		want = ".webp"
	}
	if ext == ".mp4" {
		if !validMP4(data) {
			return "", errors.New("MP4 文件结构不正确")
		}
		return ".mp4", nil
	}
	if want == "" || (ext != want && !(want == ".jpg" && ext == ".jpeg")) {
		return "", errors.New("文件内容与扩展名不符")
	}
	if want == ".webp" {
		if !validWebP(data) {
			return "", errors.New("WebP 文件结构不正确")
		}
		return want, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 20_000_000 {
		return "", errors.New("图片损坏或尺寸过大")
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return "", errors.New("图片内容不完整")
	}
	return want, nil
}
func validWebP(b []byte) bool {
	return validWebPDepth(b, 0)
}
func validWebPDepth(b []byte, depth int) bool {
	if depth > 1 {
		return false
	}
	if len(b) < 30 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(b[4:8]))+8 != uint64(len(b)) {
		return false
	}
	imageChunk := false
	for off := 12; off < len(b); {
		if off+8 > len(b) {
			return false
		}
		n := uint64(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		end := uint64(off) + 8 + n
		if end > uint64(len(b)) {
			return false
		}
		kind := string(b[off : off+4])
		payload := b[off+8 : int(end)]
		switch kind {
		case "VP8 ":
			if len(payload) <= 10 || payload[0]&1 != 0 || payload[0]&16 == 0 || !bytes.Equal(payload[3:6], []byte{0x9d, 0x01, 0x2a}) {
				return false
			}
			partition := int(uint32(payload[0])|uint32(payload[1])<<8|uint32(payload[2])<<16) >> 5
			if partition < 1 || partition > len(payload)-10 {
				return false
			}
			w := binary.LittleEndian.Uint16(payload[6:8]) & 0x3fff
			h := binary.LittleEndian.Uint16(payload[8:10]) & 0x3fff
			if w == 0 || h == 0 || uint64(w)*uint64(h) > 20_000_000 {
				return false
			}
			imageChunk = true
		case "VP8L":
			if len(payload) <= 5 || payload[0] != 0x2f {
				return false
			}
			bits := binary.LittleEndian.Uint32(payload[1:5])
			w := (bits & 0x3fff) + 1
			h := ((bits >> 14) & 0x3fff) + 1
			if bits>>29 != 0 || uint64(w)*uint64(h) > 20_000_000 {
				return false
			}
			imageChunk = true
		case "VP8X":
			if len(payload) != 10 {
				return false
			}
			w := uint64(payload[4]) | uint64(payload[5])<<8 | uint64(payload[6])<<16
			h := uint64(payload[7]) | uint64(payload[8])<<8 | uint64(payload[9])<<16
			if (w+1)*(h+1) > 20_000_000 {
				return false
			}
		case "ANMF":
			if len(payload) < 24 || payload[15]&0xfc != 0 {
				return false
			}
			frame := append([]byte("RIFF\x00\x00\x00\x00WEBP"), payload[16:]...)
			binary.LittleEndian.PutUint32(frame[4:8], uint32(len(frame)-8))
			if !validWebPDepth(frame, depth+1) {
				return false
			}
			imageChunk = true
		}
		off = int(end)
		if n&1 != 0 {
			off++
		}
		if off > len(b) {
			return false
		}
	}
	return imageChunk
}
func validMP4(b []byte) bool {
	ftyp, moov, mdat := false, false, false
	for off := 0; off < len(b); {
		if off+8 > len(b) {
			return false
		}
		n := uint64(binary.BigEndian.Uint32(b[off : off+4]))
		header := uint64(8)
		if n == 1 {
			if off+16 > len(b) {
				return false
			}
			n = binary.BigEndian.Uint64(b[off+8 : off+16])
			header = 16
		}
		if n == 0 {
			n = uint64(len(b) - off)
		}
		if n < header || n > uint64(len(b)-off) {
			return false
		}
		kind := string(b[off+4 : off+8])
		switch kind {
		case "ftyp":
			if off != 0 || n < header+8 {
				return false
			}
			brands := b[off+int(header) : off+int(n)]
			for k := 0; k+4 <= len(brands); k += 4 {
				if k == 4 {
					continue
				}
				brand := string(brands[k : k+4])
				if brand == "isom" || brand == "iso2" || brand == "iso5" || brand == "iso6" || brand == "mp41" || brand == "mp42" || brand == "avc1" || brand == "M4V " || brand == "dash" {
					ftyp = true
				}
			}
		case "moov":
			moov = validMP4Movie(b[off+int(header) : off+int(n)])
		case "mdat":
			mdat = n > header
		}
		off += int(n)
	}
	return ftyp && moov && mdat
}

func validMP4Movie(b []byte) bool {
	movie, track := false, false
	for off := 0; off < len(b); {
		if off+8 > len(b) {
			return false
		}
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		if n < 8 || n > len(b)-off {
			return false
		}
		kind := string(b[off+4 : off+8])
		payload := b[off+8 : off+n]
		if kind == "mvhd" {
			if len(payload) < 100 || (payload[0] != 0 && payload[0] != 1) {
				return false
			}
			if payload[0] == 1 && len(payload) < 112 {
				return false
			}
			offset := 12
			if payload[0] == 1 {
				offset = 20
			}
			if binary.BigEndian.Uint32(payload[offset:offset+4]) == 0 {
				return false
			}
			movie = true
		}
		if kind == "trak" {
			if len(payload) < 16 {
				return false
			}
			tkhd, mdia := false, false
			for at := 0; at < len(payload); {
				if at+8 > len(payload) {
					return false
				}
				size := int(binary.BigEndian.Uint32(payload[at : at+4]))
				if size < 8 || size > len(payload)-at {
					return false
				}
				box := string(payload[at+4 : at+8])
				if box == "tkhd" {
					tkhd = size >= 92
				}
				if box == "mdia" {
					mdia = size >= 16
				}
				at += size
			}
			track = tkhd && mdia
		}
		off += n
	}
	return movie && track
}
func (a *Admin) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 33<<20)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		adminError(w, 400, "上传失败，文件上限为 32 MiB")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		adminError(w, 400, "请选择文件")
		return
	}
	defer f.Close()
	if strings.ContainsAny(header.Filename, "/\\") || strings.ContainsRune(header.Filename, 0) {
		adminError(w, 400, "文件名不正确")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil || len(data) > 32<<20 || len(data) == 0 {
		adminError(w, 400, "文件为空或超过 32 MiB")
		return
	}
	ext, err := uploadKind(data, header.Filename)
	if err != nil {
		adminError(w, 400, err.Error())
		return
	}
	if r.URL.Query().Get("purpose") == "avatar" && ext == ".mp4" {
		adminError(w, 400, "头像仅支持图片")
		return
	}
	root, err := filepath.Abs(a.app.Config.MediaDir)
	if err != nil || strings.TrimSpace(a.app.Config.MediaDir) == "" {
		adminError(w, 500, "媒体目录尚未配置")
		return
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		adminError(w, 500, "无法创建媒体目录")
		return
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		adminError(w, 500, "无法解析媒体目录")
		return
	}
	sub := filepath.Join("wp-content", "uploads", time.Now().UTC().Format("2006"), time.Now().UTC().Format("01"))
	dir := filepath.Join(root, sub)
	if err = os.MkdirAll(dir, 0755); err != nil {
		adminError(w, 500, "无法创建上传目录")
		return
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		adminError(w, 500, "无法解析上传目录")
		return
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		adminError(w, 400, "上传目录超出媒体边界")
		return
	}
	token, err := randomToken()
	if err != nil {
		adminError(w, 500, "无法生成文件名")
		return
	}
	name := token + ext
	out, err := os.OpenFile(filepath.Join(resolved, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		adminError(w, 500, "无法写入媒体文件")
		return
	}
	_, writeErr := out.Write(data)
	closeErr := out.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(filepath.Join(resolved, name))
		adminError(w, 500, "媒体文件未完整写入")
		return
	}
	mediaURL := "/" + filepath.ToSlash(filepath.Join(sub, name))
	adminJSON(w, 201, map[string]any{"url": mediaURL, "name": header.Filename, "bytes": len(data), "type": http.DetectContentType(data)})
}

type mediaItem struct {
	URL        string `json:"url"`
	Name       string `json:"name"`
	Bytes      int64  `json:"bytes"`
	Type       string `json:"type"`
	ModifiedAt string `json:"modified_at"`
}

type recentMediaItem struct {
	item     mediaItem
	modified time.Time
}

func mediaNewer(a, b recentMediaItem) bool {
	if a.modified.Equal(b.modified) {
		return a.item.URL < b.item.URL
	}
	return a.modified.After(b.modified)
}

// Keep the least recent item at the root so scanning the whole archive only
// retains the newest 1000 entries, including uploads in later directories.
type recentMediaHeap []recentMediaItem

func (h recentMediaHeap) Len() int           { return len(h) }
func (h recentMediaHeap) Less(i, j int) bool { return mediaNewer(h[j], h[i]) }
func (h recentMediaHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *recentMediaHeap) Push(item any)     { *h = append(*h, item.(recentMediaItem)) }
func (h *recentMediaHeap) Pop() any {
	last := len(*h) - 1
	item := (*h)[last]
	(*h)[last] = recentMediaItem{}
	*h = (*h)[:last]
	return item
}

func (a *Admin) media(w http.ResponseWriter, r *http.Request) {
	items := []mediaItem{}
	if r.Context().Err() != nil {
		return
	}
	root := filepath.Join(a.app.Config.MediaDir, "wp-content", "uploads")
	if strings.TrimSpace(a.app.Config.MediaDir) == "" {
		adminJSON(w, 200, items)
		return
	}
	recent := make(recentMediaHeap, 0, 1000)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if err != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		kind := ""
		switch ext {
		case ".png":
			kind = "image/png"
		case ".jpg", ".jpeg":
			kind = "image/jpeg"
		case ".gif":
			kind = "image/gif"
		case ".webp":
			kind = "image/webp"
		case ".mp4":
			kind = "video/mp4"
		default:
			return nil
		}
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, e := filepath.Rel(a.app.Config.MediaDir, path)
		if e != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		for i := range parts {
			parts[i] = url.PathEscape(parts[i])
		}
		candidate := recentMediaItem{
			item:     mediaItem{URL: "/" + strings.Join(parts, "/"), Name: entry.Name(), Bytes: info.Size(), Type: kind, ModifiedAt: info.ModTime().UTC().Format(time.RFC3339)},
			modified: info.ModTime(),
		}
		if len(recent) < cap(recent) {
			heap.Push(&recent, candidate)
		} else if mediaNewer(candidate, recent[0]) {
			recent[0] = candidate
			heap.Fix(&recent, 0)
		}
		return nil
	})
	if r.Context().Err() != nil {
		return
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		adminError(w, 500, "无法读取媒体目录")
		return
	}
	sort.Slice(recent, func(i, j int) bool { return mediaNewer(recent[i], recent[j]) })
	for _, candidate := range recent {
		items = append(items, candidate.item)
	}
	adminJSON(w, 200, items)
}

// The origin used by tests and command-line clients must match the request host.
func adminOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
