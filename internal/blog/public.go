package blog

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/xml"
	"errors"
	"fmt"
	stdhtml "html"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

//go:embed web/public/public.html web/public/public.css web/public/public.js web/public/public-theme.js
var publicFiles embed.FS

type publicAsset struct {
	data        []byte
	contentType string
	etag        string
}

var publicAssets = func() map[string]publicAsset {
	assets := make(map[string]publicAsset)
	for _, name := range []string{"public.css", "public.js", "public-theme.js"} {
		data, err := publicFiles.ReadFile("web/public/" + name)
		if err != nil {
			panic(err)
		}
		contentType := "application/javascript; charset=utf-8"
		if strings.HasSuffix(name, ".css") {
			contentType = "text/css; charset=utf-8"
		}
		assets["/assets/"+name] = publicAsset{data: data, contentType: contentType, etag: fmt.Sprintf(`"%x"`, sha256.Sum256(data))}
	}
	return assets
}()

var publicAssetVersion = func() string {
	h := sha256.New()
	for _, name := range []string{"public.css", "public.js", "public-theme.js"} {
		_, _ = h.Write(publicAssets["/assets/"+name].data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}()

var publicTemplate = template.Must(template.New("public.html").Funcs(template.FuncMap{
	"icon":        publicIcon,
	"contactIcon": publicContactIcon,
	"localLink":   publicLocalLink,
	"text":        publicText,
	"languageDir": publicLanguageDirection,
	"languages":   publicLanguages,
	"rankNumber":  func(index int) int { return index + 1 },
	"cardContext": func(page publicView, card publicCard) any {
		return struct {
			Page publicView
			Card publicCard
		}{page, card}
	},
}).ParseFS(publicFiles, "web/public/public.html"))

type publicLink struct {
	Label, URL, Lang string
	SwitchURL        string
	Current          bool
	Count            int
}
type publicCard struct {
	ID                                         int64
	Title, URL, Date, Author, Image, Published string
	HTML                                       template.HTML
	Sticky                                     bool
	Categories                                 []publicLink
	ReadMinutes                                int
	ThumbnailURL                               string
	ImageSrcset                                template.Srcset
}
type publicGroup struct {
	Title, URL string
	Cards      []publicCard
}
type publicPageLink struct {
	Label, URL string
	Current    bool
}
type publicQueryField struct{ Name, Value string }
type publicContactLink struct {
	Kind, Label, Account, URL string
	Icon, FaviconURL          string
	NewTab                    bool
}
type publicView struct {
	FooterText                                                                                                    string
	AssetVersion                                                                                                  string
	Lang, HTMLLang, SiteTitle, SiteDescription, Title, Description, Canonical, Robots                             string
	Heading, Subtitle, Search, CategoryLabel, OlderLabel, NewerLabel, ReadMore, EmptyLabel, MenuLabel             string
	BasePath, HomeURL, HeaderImage, Date, Author, Modified, Published                                             string
	Groups                                                                                                        []publicGroup
	ArchiveCards                                                                                                  []publicCard
	Languages, Categories                                                                                         []publicLink
	Post                                                                                                          *publicCard
	HTML                                                                                                          template.HTML
	Tags                                                                                                          []publicLink
	Older, Newer                                                                                                  string
	IsSearch, IsArticle, IsOverview                                                                               bool
	Columns                                                                                                       [][]publicGroup
	Previous, Next                                                                                                *publicCard
	BackURL, AllArticlesURL, BackLabel, PreviousLabel, NextLabel, PageLabel, GoLabel, ShortcutLabel, ShortcutHelp string
	Page, PageCount                                                                                               int
	Pages                                                                                                         []publicPageLink
	QueryFields                                                                                                   []publicQueryField
	ContactHeading                                                                                                string
	ContactNoticeEnabled                                                                                          bool
	ContactNoticeText                                                                                             string
	Contacts                                                                                                      []publicContactLink
	AvatarURL                                                                                                     string
	FilterTags                                                                                                    []publicLink
	FilterTagsLabel, ClearFiltersLabel, ClearFiltersURL, CategoryAllURL, TagAllURL                                string
	CategoryAllCurrent, TagAllCurrent, FiltersActive                                                              bool
	Featured                                                                                                      *publicCard
	RecentCards                                                                                                   []publicCard
	PostCount                                                                                                     int
	SidebarPopular                                                                                                []publicPopular
	SidebarStats                                                                                                  publicSidebarStats
	SidebarAd                                                                                                     *SidebarAd
	SidebarModules                                                                                                map[string]bool
	SidebarOrder                                                                                                  []string
	ArchiveYears                                                                                                  []publicArchiveYear
	ArchiveAllURL                                                                                                 string
	ArchiveAllCurrent                                                                                             bool
	IsJournal, IsBlogPost, IsPage, HomeCurrent, ArticlesCurrent, AboutCurrent                                     bool
	IsIndependentPage                                                                                             bool
	ShowSidebar                                                                                                   bool
	ShowPublicStats                                                                                               bool
	AboutURL, OGType, SocialImage, SocialImageAlt, SocialCard                                                     string
	SchemaJSON                                                                                                    template.JS
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		lang, _, _ := publicLanguagePath(r.URL.Path)
		http.Error(w, publicText(lang, "请求方法不支持", "Method not allowed", "Methode niet toegestaan", "Método no permitido"), http.StatusMethodNotAllowed)
		return
	}
	if asset, ok := publicAssets[r.URL.Path]; ok {
		w.Header().Set("Content-Type", asset.contentType)
		w.Header().Set("ETag", asset.etag)
		if r.URL.Query().Get("v") == publicAssetVersion {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		http.ServeContent(w, r, path.Base(r.URL.Path), time.Time{}, bytes.NewReader(asset.data))
		return
	}
	if path.Base(r.URL.Path) != "robots.txt" && a.servePublicMedia(w, r) {
		return
	}
	lang, route, ok := publicLanguagePath(r.URL.Path)
	var directPage *Post
	if !ok {
		if r.URL.RawPath != "" || strings.Contains(r.URL.EscapedPath(), "%") {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		p, err := a.Store.GetPublicPageByPath(r.Context(), route)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				publicNotFound(w, publicRequestLanguage(r))
			} else {
				http.Error(w, publicText(lang, "服务暂时不可用", "Service unavailable", "Dienst tijdelijk niet beschikbaar", "Servicio no disponible"), http.StatusServiceUnavailable)
			}
			return
		}
		if _, visible := localizedPost(p, lang); !visible {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		directPage = &p
	}
	q := r.URL.Query()
	if (route == "/" || route == "/index.php") && q.Has("feed") && !q.Has("robots") && !q.Has("sitemap") {
		if feed := q.Get("feed"); feed != "rss2" && feed != "comments-rss2" {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Robots-Tag", "noindex")
		w.WriteHeader(http.StatusGone)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, publicText(lang, "RSS 已停用", "RSS feeds are no longer available.", "RSS-feeds zijn niet meer beschikbaar.", "Las fuentes RSS ya no están disponibles.")+"\n")
		}
		return
	}
	settings, err := a.Store.Settings(r.Context())
	if err != nil {
		http.Error(w, publicText(lang, "服务暂时不可用", "Service unavailable", "Dienst tijdelijk niet beschikbaar", "Servicio no disponible"), http.StatusServiceUnavailable)
		return
	}
	settings = localizedPublicSettings(publicSettings(settings, a.Config), lang)
	if base, err := url.Parse(settings.BaseURL); err == nil && strings.EqualFold(base.Hostname(), "www.ganwen.nl") && strings.EqualFold(strings.Split(r.Host, ":")[0], "ganwen.nl") && !a.Config.Preview {
		http.Redirect(w, r, settings.BaseURL+r.URL.RequestURI(), http.StatusMovedPermanently)
		return
	}
	if a.redirectVisitorLanguage(w, r) {
		return
	}
	if route == "/robots.txt" || (directPage == nil && q.Has("robots")) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if a.Config.Preview {
			_, _ = io.WriteString(w, "User-agent: *\nDisallow: /\n")
		} else {
			_, _ = fmt.Fprintf(w, "User-agent: *\nDisallow: /admin\nDisallow: /wp-admin/\nAllow: /wp-admin/admin-ajax.php\n\nSitemap: %s/?sitemap=index\n", settings.BaseURL)
		}
		return
	}
	posts, err := a.Store.ListPosts(r.Context(), false)
	if err != nil {
		http.Error(w, publicText(lang, "服务暂时不可用", "Service unavailable", "Dienst tijdelijk niet beschikbaar", "Servicio no disponible"), http.StatusServiceUnavailable)
		return
	}
	if route == "/sitemap.xml" || route == "/wp-sitemap.xml" || (directPage == nil && q.Has("sitemap")) {
		a.servePublicSitemap(w, r, settings, posts)
		return
	}
	if directPage == nil && route != "/" && route != "/index.php" {
		publicNotFound(w, publicRequestLanguage(r))
		return
	}
	if q.Get("preview") != "" || q.Has("preview_id") {
		publicNotFound(w, publicRequestLanguage(r))
		return
	}
	view := newPublicView(settings, lang)
	view.IsJournal = publicLayout(r, settings) == "journal"
	populatePublicTaxonomy(&view, settings, posts, q, lang)
	populatePublicNavigation(&view, settings, posts, lang)
	populatePublicArchives(&view, settings, posts, q, lang)
	if a.Config.Preview {
		view.Robots = "noindex, nofollow"
	}
	single := directPage
	idName := ""
	for _, key := range []string{"p", "page_id", "attachment_id"} {
		if q.Has(key) {
			if idName != "" {
				publicNotFound(w, publicRequestLanguage(r))
				return
			}
			idName = key
		}
	}
	if idName != "" {
		if directPage != nil {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		id, err := strconv.ParseInt(q.Get(idName), 10, 64)
		if err != nil || id <= 0 {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		p, err := a.Store.GetPost(r.Context(), id)
		if err != nil || p.Status != "publish" || (idName == "page_id" && p.Type != "page") || (idName == "attachment_id" && p.Type != "attachment") {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		if _, ok := localizedPost(p, lang); !ok {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		single = &p
	}
	if single != nil {
		evidencePage := independentEvidencePage(*single)
		if evidencePage && redirectIndependentEvidenceLanguage(w, r, *single, lang) {
			return
		}
		p, _ := localizedPost(*single, lang)
		card := publicPostCard(p, *single, settings, lang)
		view.Post, view.IsArticle, view.Title, view.Heading = &card, true, publicSEOTitle(p, settings.Title), p.Title
		view.IsBlogPost, view.IsPage = single.Type == "post", single.Type == "page"
		view.IsIndependentPage = single.PublicPath != ""
		if single.Unlisted {
			view.Robots = "noindex, nofollow"
		}
		view.AboutCurrent = view.IsPage && single.ID == 2
		view.ArticlesCurrent = view.IsBlogPost
		view.HTML = safePublicHTMLForLanguage(p.ContentHTML, settings.BaseURL, lang, a.Config.Preview)
		view.Description = publicPostDescription(p)
		view.Canonical = publicPostURL(*single, settings, lang)
		view.Date, view.Author = publicDate(single.PublishedAt, lang), publicAuthorName(single.Author, lang)
		view.Published, view.Modified = publicTimestamp(single.PublishedAt), publicModifiedTimestamp(*single, lang)
		view.Languages = publicPostLanguages(*single, settings)
		if evidencePage {
			for i := range view.Languages {
				if view.Languages[i].Lang == "zh" {
					view.Languages[i].SwitchURL = independentEvidenceChineseSwitch(view.Languages[i].URL)
				}
			}
		}
		view.BackURL = view.AllArticlesURL
		if view.IsIndependentPage {
			view.BackURL = view.HomeURL
			view.BackLabel = publicText(lang, "返回博客", "Back to blog", "Terug naar de blog", "Volver al blog")
		}
		if single.Type == "post" {
			list, _ := filterPublicPosts(posts, settings, url.Values{}, lang)
			publicSortByDate(list)
			for i := range list {
				if list[i].ID != single.ID {
					continue
				}
				if i > 0 {
					p, _ := localizedPost(list[i-1], lang)
					c := publicPostCard(p, list[i-1], settings, lang)
					view.Previous = &c
				}
				if i+1 < len(list) {
					p, _ := localizedPost(list[i+1], lang)
					c := publicPostCard(p, list[i+1], settings, lang)
					view.Next = &c
				}
				break
			}
		}
		for _, term := range single.Tags {
			view.Tags = append(view.Tags, publicLink{Label: publicTermLabel(term.Name, lang), URL: publicTermURL(term, settings, lang)})
		}
	} else {
		filtered, valid := filterPublicPosts(posts, settings, q, lang)
		if !valid {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		if view.IsJournal {
			publicSortByDate(filtered)
		}
		if len(filtered) == 0 && view.FiltersActive {
			view.EmptyLabel = publicText(lang, "没有匹配的文章，可调整分类或标签。", "No matching articles. Try another category or tag.", "Geen passende artikelen. Kies een andere categorie of tag.", "No hay artículos coincidentes. Prueba otra categoría o etiqueta.")
		}
		page := 1
		if q.Has("paged") {
			page, err = strconv.Atoi(q.Get("paged"))
			if err != nil || page < 1 {
				publicNotFound(w, publicRequestLanguage(r))
				return
			}
		}
		if q.Has("s") {
			view.IsSearch = true
			view.Search = q.Get("s")
			view.Robots = "noindex, nofollow"
			view.Heading = publicText(lang, "搜索", "Search", "Zoeken", "Buscar") + ": " + view.Search
		}
		if heading := publicArchiveHeading(settings, posts, q, lang); heading != "" {
			view.Heading = heading
		}
		hasArchiveQuery := len(publicArchiveQuery(q)) != 0
		if hasArchiveQuery {
			view.Title = view.Heading + " | " + settings.Title
		}
		view.Canonical = publicArchiveURL(settings, lang, q)
		if page > 1 && page > (len(filtered)+settings.PostsPerPage-1)/settings.PostsPerPage {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		start := (page - 1) * settings.PostsPerPage
		if page > 1 && start >= len(filtered) {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		end := start + settings.PostsPerPage
		if end > len(filtered) {
			end = len(filtered)
		}
		if page == 1 && !hasArchiveQuery {
			view.IsOverview = true
			view.HomeCurrent = true
			if view.IsJournal {
				populatePublicHome(&view, filtered, settings, lang)
			} else {
				view.Groups = publicGroups(filtered, settings, lang, false, 4)
			}
		} else if start < len(filtered) {
			for _, p := range filtered[start:end] {
				local, _ := localizedPost(p, lang)
				view.ArchiveCards = append(view.ArchiveCards, publicPostCard(local, p, settings, lang))
			}
		}
		view.ArticlesCurrent = !view.IsOverview
		if end < len(filtered) {
			next := clonePublicQuery(q)
			next.Set("paged", strconv.Itoa(page+1))
			view.Older = publicArchiveURL(settings, lang, next)
		}
		if page > 1 {
			prev := clonePublicQuery(q)
			prev.Set("paged", strconv.Itoa(page-1))
			view.Newer = publicArchiveURL(settings, lang, prev)
		}
		if page > 1 {
			view.Title += " | " + publicText(lang, "页码", "Page", "Pagina", "Página") + " " + strconv.Itoa(page)
		}
		view.Page, view.PageCount = page, max(1, (len(filtered)+settings.PostsPerPage-1)/settings.PostsPerPage)
		view.Pages = publicPaginationLinks(settings, lang, q, page, view.PageCount)
		for _, key := range []string{"cat", "category_name", "tag", "author", "m", "post_format", "s"} {
			if q.Has(key) {
				view.QueryFields = append(view.QueryFields, publicQueryField{key, q.Get(key)})
			}
		}
		view.Columns = publicBalancedColumns(view.Groups)
		if !view.IsOverview && len(view.Groups) > 0 {
			view.Columns = [][]publicGroup{view.Groups}
		}
		if view.IsOverview {
			view.Pages = nil
			view.Older = ""
			view.Newer = ""
		}
		for _, language := range publicLanguages() {
			view.Languages = append(view.Languages, publicLink{Label: nativeLanguageName(language), Lang: language, URL: publicArchiveURL(settings, language, q)})
		}
	}
	view.Languages = selectedPublicLanguageFirst(view.Languages, lang)
	view.ShowSidebar = !view.IsArticle || settings.ArticleSidebarEnabled
	if view.ShowSidebar {
		a.populatePublicSidebar(r.Context(), &view, settings, posts)
		a.populatePublicSidebarAd(w, r, &view, settings)
		view.ShowSidebar = len(view.SidebarModules) > 0 || view.SidebarAd != nil
	}
	a.populatePublicImages(&view, settings)
	populatePublicSEO(&view, settings)
	publicLayoutCachePolicy(w.Header())
	if view.Robots != "" {
		w.Header().Set("X-Robots-Tag", view.Robots)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", publicContentSecurityPolicy(view.SchemaJSON))
	if r.Method == http.MethodHead {
		return
	}
	var buf bytes.Buffer
	if err := publicTemplate.ExecuteTemplate(&buf, "public.html", view); err != nil {
		http.Error(w, publicText(lang, "服务暂时不可用", "Service unavailable", "Dienst tijdelijk niet beschikbaar", "Servicio no disponible"), http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write(buf.Bytes())
}

func publicLocalLink(value string) string {
	u, err := url.Parse(value)
	if err != nil {
		return value
	}
	if u.Hostname() == "www.ganwen.nl" || u.Hostname() == "ganwen.nl" {
		s := u.RequestURI()
		if u.Fragment != "" {
			s += "#" + u.Fragment
		}
		return s
	}
	return value
}

func publicLanguagePath(p string) (lang, route string, ok bool) {
	lang, route = "zh", p
	for _, language := range translationLanguages() {
		prefix := "/" + language
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			lang = prefix[1:]
			route = strings.TrimPrefix(p, prefix)
			if route == "" {
				route = "/"
			}
			break
		}
	}
	return lang, route, route == "/" || route == "/index.php" || route == "/robots.txt" || route == "/sitemap.xml" || route == "/wp-sitemap.xml"
}
func publicSettings(s SiteSettings, cfg Config) SiteSettings {
	if s.Title == "" {
		s.Title = "甘文"
	}
	if cfg.BaseURL != "" {
		s.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	} else {
		s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	}
	if s.BaseURL == "" {
		s.BaseURL = "https://www.ganwen.nl"
	}
	if s.PostsPerPage < 1 || s.PostsPerPage > 100 {
		s.PostsPerPage = 10
	}
	return s
}
func newPublicView(s SiteSettings, lang string) publicView {
	s = localizedPublicSettings(s, lang)
	v := publicView{Lang: lang, HTMLLang: lang, SiteTitle: s.Title, SiteDescription: s.Description, Title: s.Title, Description: s.Description, Heading: publicText(lang, "文章", "Articles", "Artikelen", "Artículos"), HomeURL: publicArchiveURL(s, lang, nil), HeaderImage: s.HeaderImage}
	v.AssetVersion = publicAssetVersion
	v.FooterText = publicFooterText(s, lang)
	if avatar := strings.TrimSpace(s.AvatarURL); validAvatarURL(avatar) {
		v.AvatarURL = avatar
	}
	v.ContactHeading = publicText(lang, "保持联系", "Stay in touch", "Blijf in contact", "Sigamos en contacto")
	v.Contacts = publicContactLinks(s.ContactLinks, lang)
	v.ContactNoticeEnabled = s.ContactNoticeEnabled
	v.ContactNoticeText = s.ContactNoticeText
	if lang == "zh" {
		v.HTMLLang = "zh-Hans"
	}
	v.BasePath = publicLanguagePrefix(lang)
	v.AllArticlesURL = publicArchiveURL(s, lang, url.Values{"paged": {"1"}})
	v.BackLabel = publicText(lang, "返回列表", "Back to list", "Terug naar de lijst", "Volver a la lista")
	v.PreviousLabel = publicText(lang, "上一篇", "Previous article", "Vorig artikel", "Artículo anterior")
	v.NextLabel = publicText(lang, "下一篇", "Next article", "Volgend artikel", "Artículo siguiente")
	v.PageLabel = publicText(lang, "页码", "Page", "Pagina", "Página")
	v.GoLabel = publicText(lang, "跳转", "Go", "Ga", "Ir")
	v.ShortcutLabel = publicText(lang, "快捷键", "Keyboard shortcuts", "Sneltoetsen", "Atajos de teclado")
	v.ShortcutHelp = publicText(lang, "Esc 返回列表 · ← / → 切换文章或页码 · / 搜索 · ? 显示快捷键", "Esc: back to list · ← / →: article or page · /: search · ?: shortcuts", "Esc: terug naar lijst · ← / →: artikel of pagina · /: zoeken · ?: sneltoetsen", "Esc: volver a la lista · ← / →: artículo o página · /: buscar · ?: atajos")
	v.CategoryLabel = publicText(lang, "分类", "Categories", "Categorieën", "Categorías")
	v.FilterTagsLabel = publicText(lang, "标签", "Tags", "Tags", "Etiquetas")
	v.ClearFiltersLabel = publicText(lang, "清除筛选", "Clear filters", "Filters wissen", "Quitar filtros")
	v.OlderLabel, v.NewerLabel = publicText(lang, "较早文章", "Older posts", "Oudere berichten", "Artículos anteriores"), publicText(lang, "较新文章", "Newer posts", "Nieuwere berichten", "Artículos recientes")
	v.ReadMore, v.EmptyLabel, v.MenuLabel = publicText(lang, "阅读全文", "Read more", "Lees meer", "Leer más"), publicText(lang, "暂时没有文章", "No articles yet", "Nog geen artikelen", "Aún no hay artículos"), publicText(lang, "菜单", "Menu", "Menu", "Menú")
	for _, term := range s.Categories {
		v.Categories = append(v.Categories, publicLink{Label: publicTermLabel(term.Name, lang), URL: publicTermURL(term, s, lang)})
	}
	return v
}

// WordPress can store a Chinese term slug as percent-encoded UTF-8, while
// net/url already decodes the query. Accept either representation consistently.
func publicTermSlug(slug string) string {
	if decoded, err := url.PathUnescape(slug); err == nil {
		return decoded
	}
	return slug
}

func publicTermMatches(slug, selected string) bool {
	return selected != "" && publicTermSlug(slug) == publicTermSlug(selected)
}

func publicTagIdentity(term Term) string {
	if term.ID > 0 {
		return "id:" + strconv.FormatInt(term.ID, 10)
	}
	return "slug:" + publicTermSlug(term.Slug)
}

func publicCategoryTerms(s SiteSettings, posts []Post) []Term {
	// Keep configured categories even when empty (including original 爱情),
	// then add categories actually used by published blog posts.
	terms := append([]Term{}, s.Categories...)
	seen := make(map[string]bool)
	slugs := make(map[string]bool)
	for _, term := range terms {
		seen[publicTagIdentity(term)] = true
		if term.Slug != "" {
			slugs[publicTermSlug(term.Slug)] = true
		}
	}
	var added []Term
	for _, post := range posts {
		if post.Status != "publish" || post.Type != "post" {
			continue
		}
		for _, term := range post.Categories {
			if strings.TrimSpace(term.Name) == "" || (term.ID <= 0 && strings.TrimSpace(term.Slug) == "") {
				continue
			}
			key := publicTagIdentity(term)
			if seen[key] || (term.Slug != "" && slugs[publicTermSlug(term.Slug)]) {
				continue
			}
			term.Taxonomy = "category"
			added = append(added, term)
			seen[key] = true
			if term.Slug != "" {
				slugs[publicTermSlug(term.Slug)] = true
			}
		}
	}
	sort.Slice(added, func(i, j int) bool {
		if added[i].Name == added[j].Name {
			return publicTagIdentity(added[i]) < publicTagIdentity(added[j])
		}
		return added[i].Name < added[j].Name
	})
	return append(terms, added...)
}

// Settings supplies the original names/order/URLs, but a tag must actually
// belong to a published blog post before it becomes a public navigation item.
func publicFilterTagTerms(s SiteSettings, posts []Post) []Term {
	associated := make(map[string]Term)
	for _, post := range posts {
		if post.Status != "publish" || post.Type != "post" {
			continue
		}
		for _, term := range post.Tags {
			if strings.TrimSpace(term.Slug) == "" || strings.TrimSpace(term.Name) == "" {
				continue
			}
			term.Taxonomy = "post_tag"
			associated[publicTagIdentity(term)] = term
		}
	}
	terms := make([]Term, 0, len(associated))
	for _, term := range s.Tags {
		key := publicTagIdentity(term)
		if _, ok := associated[key]; ok {
			term.Taxonomy = "post_tag"
			terms = append(terms, term)
			delete(associated, key)
		}
	}
	// Published terms missing from settings remain available without exposing
	// names derived from drafts, private posts, attachments or unrelated pages.
	var remaining []Term
	for _, term := range associated {
		remaining = append(remaining, term)
	}
	sort.Slice(remaining, func(i, j int) bool {
		if remaining[i].Name == remaining[j].Name {
			return publicTagIdentity(remaining[i]) < publicTagIdentity(remaining[j])
		}
		return remaining[i].Name < remaining[j].Name
	})
	return append(terms, remaining...)
}

func publicFilterTermURL(term Term, s SiteSettings, lang string, current url.Values) string {
	original := publicTermURL(term, s, lang)
	link, err := url.Parse(original)
	if err != nil {
		return original
	}
	q := clonePublicQuery(current)
	q.Del("paged")
	if term.Taxonomy == "post_tag" || term.Taxonomy == "tag" {
		q.Del("tag")
	} else {
		q.Del("cat")
		q.Del("category_name")
	}
	for key, values := range link.Query() {
		q[key] = append([]string{}, values...)
	}
	result := publicArchiveURL(s, lang, q)
	parsed, _ := url.Parse(result)
	if parsed.Query().Encode() == link.Query().Encode() {
		// Preserve the original Chinese URL, including its percent-escape case,
		// when no other filter needs to be carried to this navigation item.
		return original
	}
	return result
}

func publicFilterResetURL(s SiteSettings, lang string, q url.Values, keys ...string) string {
	next := clonePublicQuery(q)
	for _, key := range keys {
		next.Del(key)
	}
	next.Set("paged", "1")
	return publicArchiveURL(s, lang, next)
}

func populatePublicTaxonomy(view *publicView, s SiteSettings, posts []Post, q url.Values, lang string) {
	var available []Post
	for _, p := range posts {
		if p.Type != "post" {
			continue
		}
		if _, ok := localizedPost(p, lang); ok {
			available = append(available, p)
		}
	}
	view.Categories = nil
	for _, term := range publicCategoryTerms(s, posts) {
		current := (q.Get("cat") != "" && q.Get("cat") == strconv.FormatInt(term.ID, 10)) || publicTermMatches(term.Slug, q.Get("category_name"))
		count := 0
		for _, p := range available {
			for _, category := range p.Categories {
				if (term.ID > 0 && category.ID == term.ID) || (term.Slug != "" && publicTermMatches(category.Slug, term.Slug)) {
					count++
					break
				}
			}
		}
		view.Categories = append(view.Categories, publicLink{Label: publicTermLabel(term.Name, lang), URL: publicFilterTermURL(term, s, lang, q), Current: current, Count: count})
	}
	view.FilterTags = nil
	for _, term := range publicFilterTagTerms(s, available) {
		view.FilterTags = append(view.FilterTags, publicLink{Label: publicTermLabel(term.Name, lang), URL: publicFilterTermURL(term, s, lang, q), Current: publicTermMatches(term.Slug, q.Get("tag"))})
	}
	view.CategoryAllCurrent = q.Get("cat") == "" && q.Get("category_name") == ""
	view.TagAllCurrent = q.Get("tag") == ""
	view.FiltersActive = !view.CategoryAllCurrent || !view.TagAllCurrent
	view.CategoryAllURL = publicFilterResetURL(s, lang, q, "cat", "category_name")
	view.TagAllURL = publicFilterResetURL(s, lang, q, "tag")
	view.ClearFiltersURL = publicFilterResetURL(s, lang, q, "cat", "category_name", "tag")
}

func publicContactLinks(links []ContactLink, lang string) []publicContactLink {
	var contacts []publicContactLink
	seen := make(map[string]bool)
	for _, link := range links {
		link.Kind = strings.ToLower(strings.TrimSpace(link.Kind))
		link.Label = strings.TrimSpace(link.Label)
		link.URL = strings.TrimSpace(link.URL)
		if link.URL == "" || !validContactLink(link) {
			continue
		}
		u, _ := url.Parse(link.URL)
		identity := contactIdentity(link.URL)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		platform, platformLabel := detectContactPlatform(link.URL)
		if (link.KindManual && link.Kind != "email") || (platform == "website" && link.Kind == "mastodon") {
			platform, platformLabel = link.Kind, contactPlatformLabel(link.Kind)
		}
		contact := publicContactLink{Kind: link.Kind, Icon: platform, Label: link.Label, URL: link.URL, NewTab: u.Scheme == "https", Account: publicContactAccount(u, platform)}
		if platform == "website" {
			contact.FaviconURL = (&url.URL{Scheme: "https", Host: u.Host, Path: "/favicon.ico"}).String()
		}
		if contact.Label == "" {
			contact.Label = platformLabel
			if contact.Label == "" && u.Scheme == "mailto" {
				contact.Label = publicText(lang, "邮箱", "Email", "E-mail", "Correo")
			} else if contact.Label == "" {
				contact.Label = publicText(lang, "网站", "Website", "Website", "Web")
			}
		}
		if contact.Label == contact.Account {
			contact.Account = ""
		}
		contacts = append(contacts, contact)
	}
	return contacts
}
func publicLanguagePrefix(lang string) string {
	if isTranslationLanguage(lang) {
		return "/" + lang
	}
	return ""
}
func publicText(lang, zh, en, nl, es string) string {
	if lang != "zh" {
		if value, ok := publicMessage(lang, zh); ok {
			return value
		}
	}
	if lang == "en" {
		return en
	}
	if lang == "nl" {
		return nl
	}
	if lang == "es" {
		return es
	}
	if isTranslationLanguage(lang) {
		return en
	}
	return zh
}
func localizedPost(p Post, lang string) (Post, bool) {
	return localizedPostWithSourceHash(p, lang, "")
}

func localizedPostWithSourceHash(p Post, lang, sourceHash string) (Post, bool) {
	if p.Status != "publish" {
		return Post{}, false
	}
	if lang == "zh" {
		return p, true
	}
	t, ok := p.Translations[lang]
	if !ok || t.Status != "publish" || strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.ContentHTML) == "" {
		return Post{}, false
	}
	if sourceHash == "" {
		sourceHash = SourceHash(p)
	}
	if t.SourceHash != sourceHash {
		return Post{}, false
	}
	p.Title, p.ContentHTML, p.ExcerptHTML, p.SEO = t.Title, t.ContentHTML, t.ExcerptHTML, t.SEO
	return p, true
}
func publicPostURL(p Post, s SiteSettings, lang string) string {
	if route := publicIndependentPagePath(p, lang); route != "" {
		return s.BaseURL + route
	}
	if lang == "zh" && p.CanonicalURL != "" {
		return p.CanonicalURL
	}
	key := "p"
	if p.Type == "page" {
		key = "page_id"
	}
	if p.Type == "attachment" {
		key = "attachment_id"
	}
	return s.BaseURL + publicLanguagePrefix(lang) + "/?" + key + "=" + strconv.FormatInt(p.ID, 10)
}
func publicPostLanguages(p Post, s SiteSettings) []publicLink {
	var links []publicLink
	sourceHash := publicTranslationSourceHash(p)
	for _, lang := range publicLanguages() {
		if _, ok := localizedPostWithSourceHash(p, lang, sourceHash); ok {
			links = append(links, publicLink{Lang: lang, Label: nativeLanguageName(lang), URL: publicPostURL(p, s, lang)})
		}
	}
	return links
}
func publicTranslationSourceHash(p Post) string {
	if len(p.Translations) == 0 {
		return ""
	}
	return SourceHash(p)
}
func publicSEOTitle(p Post, site string) string {
	title := p.Title
	if p.SEO.Title != "" {
		title = p.SEO.Title
	}
	if !strings.HasSuffix(title, " | "+site) {
		title += " | " + site
	}
	return title
}
func publicArchiveQuery(q url.Values) url.Values {
	v := url.Values{}
	for _, key := range []string{"cat", "category_name", "tag", "author", "m", "post_format", "paged", "s"} {
		if q.Has(key) {
			v.Set(key, q.Get(key))
		}
	}
	return v
}
func publicArchiveURL(s SiteSettings, lang string, q url.Values) string {
	v := publicArchiveQuery(q)
	base := s.BaseURL + publicLanguagePrefix(lang) + "/"
	if len(v) > 0 {
		base += "?" + v.Encode()
	}
	return base
}
func publicTermURL(t Term, s SiteSettings, lang string) string {
	if lang == "zh" && t.URL != "" {
		original, err := url.Parse(t.URL)
		if err == nil && !original.IsAbs() {
			if base, err := url.Parse(strings.TrimRight(s.BaseURL, "/") + "/"); err == nil {
				return base.ResolveReference(original).String()
			}
		}
		// WordPress absolute URLs retain their original query/escape spelling.
		return t.URL
	}
	q := url.Values{}
	if t.Taxonomy == "post_tag" || t.Taxonomy == "tag" {
		q.Set("tag", publicTermSlug(t.Slug))
	} else if t.ID > 0 {
		q.Set("cat", strconv.FormatInt(t.ID, 10))
	} else {
		q.Set("category_name", publicTermSlug(t.Slug))
	}
	return publicArchiveURL(s, lang, q)
}
func localizedPublicSettings(s SiteSettings, lang string) SiteSettings {
	if s.Title == "甘文" && lang != "zh" {
		s.Title = "Ganwen"
	}
	if s.Description == "甘文的个人博客" {
		s.Description = publicText(lang, s.Description, "Wen Gan's personal blog", "De persoonlijke blog van Wen Gan", "El blog personal de Wen Gan")
	}
	return s
}

func publicTermLabel(name, lang string) string {
	if lang != "zh" {
		if value, ok := publicMessage(lang, name); ok {
			return value
		}
	}
	labels := map[string][3]string{
		"意境":      {"Reflections", "Gedachten", "Reflexiones"},
		"技术":      {"Technology", "Techniek", "Tecnología"},
		"感触":      {"Life", "Leven", "Vida"},
		"偶遇":      {"Encounters", "Ontmoetingen", "Encuentros"},
		"情感":      {"Feelings", "Gevoelens", "Sentimientos"},
		"爱情":      {"Love", "Liefde", "Amor"},
		"秀逗":      {"Humor", "Humor", "Humor"},
		"风景":      {"Scenery", "Landschappen", "Paisajes"},
		"健康":      {"Health", "Gezondheid", "Salud"},
		"创业":      {"Business", "Ondernemen", "Emprendimiento"},
		"经验":      {"Experience", "Ervaringen", "Experiencias"},
		"音乐":      {"Music", "Muziek", "Música"},
		"习近平":     {"Xi Jinping", "Xi Jinping", "Xi Jinping"},
		"偷渡出境":    {"Illegal departure", "Illegale uitreis", "Salida ilegal del país"},
		"夏津公安":    {"Xiajin Police", "Politie van Xiajin", "Policía de Xiajin"},
		"夏津县":     {"Xiajin County", "District Xiajin", "Condado de Xiajin"},
		"护照激活":    {"Passport activation", "Paspoortactivatie", "Activación del pasaporte"},
		"拆墙运动":    {"Tear Down the Wall movement", "Beweging 'Breek de muur af'", "Movimiento Derribar el Muro"},
		"推墙运动":    {"Push Down the Wall movement", "Beweging 'Duw de muur om'", "Movimiento Derribar el Muro a Empujones"},
		"推墙运动习近平": {"Push Down the Wall movement · Xi Jinping", "Beweging 'Duw de muur om' · Xi Jinping", "Movimiento Derribar el Muro a Empujones · Xi Jinping"},
		"取保候审":    {"Release on bail pending trial", "Vrijlating op borgtocht in afwachting van het proces", "Libertad bajo fianza a la espera de juicio"},
		"甘文":      {"Wen Gan", "Wen Gan", "Wen Gan"},
		"政治":      {"Politics", "Politiek", "Política"},
		"简体中文":    {"Simplified Chinese", "Vereenvoudigd Chinees", "Chino simplificado"},
	}
	if v, ok := labels[name]; ok {
		if lang == "en" {
			return v[0]
		}
		if lang == "nl" {
			return v[1]
		}
		if lang == "es" {
			return v[2]
		}
	}
	return name
}
func clonePublicQuery(q url.Values) url.Values {
	v := url.Values{}
	for k, values := range q {
		v[k] = append([]string{}, values...)
	}
	return v
}

func filterPublicPosts(posts []Post, s SiteSettings, q url.Values, lang string) ([]Post, bool) {
	var list []Post
	cat, author := int64(0), int64(0)
	if q.Has("cat") {
		var err error
		cat, err = strconv.ParseInt(q.Get("cat"), 10, 64)
		if err != nil || cat <= 0 {
			return nil, false
		}
	}
	if q.Has("author") {
		var err error
		author, err = strconv.ParseInt(q.Get("author"), 10, 64)
		if err != nil || author <= 0 {
			return nil, false
		}
	}
	month := q.Get("m")
	if month != "" {
		if _, ok := parsePublicArchiveDate(month); !ok {
			return nil, false
		}
	}
	if format := q.Get("post_format"); format != "" && format != "aside" && format != "video" && format != "standard" && format != "image" && format != "quote" && format != "link" && format != "audio" && format != "gallery" && format != "status" && format != "chat" {
		return nil, false
	}
	knownCat, knownTag, knownAuthor := cat == 0 && q.Get("category_name") == "", q.Get("tag") == "", author == 0
	for _, p := range posts {
		if p.Status != "publish" || p.Type != "post" || p.Unlisted {
			continue
		}
		catMatch := cat == 0 && q.Get("category_name") == ""
		for _, t := range p.Categories {
			if (cat > 0 && t.ID == cat) || publicTermMatches(t.Slug, q.Get("category_name")) {
				catMatch, knownCat = true, true
			}
		}
		tagMatch := q.Get("tag") == ""
		for _, t := range p.Tags {
			if publicTermMatches(t.Slug, q.Get("tag")) {
				tagMatch, knownTag = true, true
			}
		}
		if author == p.AuthorID {
			knownAuthor = true
		}
		if !catMatch || !tagMatch || (author != 0 && p.AuthorID != author) || (month != "" && !publicPostInArchive(p, month)) || (q.Get("post_format") != "" && p.Format != q.Get("post_format")) {
			continue
		}
		local, ok := localizedPost(p, lang)
		if !ok {
			continue
		}
		if search := strings.TrimSpace(q.Get("s")); search != "" && !strings.Contains(strings.ToLower(local.Title+" "+publicPlainText(local.ContentHTML)), strings.ToLower(search)) {
			continue
		}
		list = append(list, p)
	}
	for _, t := range publicCategoryTerms(s, posts) {
		if (cat > 0 && t.ID == cat) || publicTermMatches(t.Slug, q.Get("category_name")) {
			knownCat = true
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Sticky != list[j].Sticky {
			return list[i].Sticky
		}
		return publicPostNewer(list[i], list[j])
	})
	return list, knownCat && knownTag && knownAuthor
}
func publicArchiveHeading(s SiteSettings, posts []Post, q url.Values, lang string) string {
	for _, t := range publicCategoryTerms(s, posts) {
		if strconv.FormatInt(t.ID, 10) == q.Get("cat") || publicTermMatches(t.Slug, q.Get("category_name")) {
			return publicTermLabel(t.Name, lang)
		}
	}
	for _, t := range publicFilterTagTerms(s, posts) {
		if publicTermMatches(t.Slug, q.Get("tag")) {
			return publicTermLabel(t.Name, lang)
		}
	}
	if q.Get("author") != "" {
		for _, p := range posts {
			if strconv.FormatInt(p.AuthorID, 10) == q.Get("author") {
				return publicAuthorName(p.Author, lang)
			}
		}
	}
	if q.Get("m") != "" {
		return publicArchiveDateLabel(q.Get("m"), lang)
	}
	if q.Get("post_format") != "" {
		return q.Get("post_format")
	}
	return ""
}
func publicGroups(posts []Post, s SiteSettings, lang string, filtered bool, limits ...int) []publicGroup {
	var groups []publicGroup
	indexes := map[string]int{}
	for _, p := range posts {
		term := Term{Name: publicText(lang, "文章", "Articles", "Artikelen", "Artículos")}
		if len(p.Categories) > 0 {
			term = p.Categories[0]
		}
		key := term.Name
		if filtered {
			key = "all"
		}
		i, ok := indexes[key]
		if !ok {
			i = len(groups)
			indexes[key] = i
			groupURL := publicArchiveURL(s, lang, url.Values{"paged": {"1"}})
			if len(p.Categories) > 0 {
				groupURL = publicTermURL(term, s, lang)
			}
			groups = append(groups, publicGroup{Title: publicTermLabel(term.Name, lang), URL: groupURL})
		}
		if len(limits) > 0 && limits[0] > 0 && len(groups[i].Cards) >= limits[0] {
			continue
		}
		local, _ := localizedPost(p, lang)
		groups[i].Cards = append(groups[i].Cards, publicPostCard(local, p, s, lang))
	}
	return groups
}
func publicPostCard(local, original Post, s SiteSettings, lang string) publicCard {
	content := publicPlainText(local.ContentHTML)
	excerpt := ""
	if strings.TrimSpace(local.ExcerptHTML) != "" {
		excerpt = publicPlainText(local.ExcerptHTML)
	}
	if strings.TrimSpace(excerpt) == "" {
		excerpt = content
	}
	runes := []rune(strings.TrimSpace(excerpt))
	if len(runes) > 180 {
		excerpt = string(runes[:180]) + "…"
	}
	card := publicCard{ID: original.ID, Title: stdhtml.UnescapeString(local.Title), URL: publicPostURL(original, s, lang), Date: publicDate(original.PublishedAt, lang), Published: publicTimestamp(original.PublishedAt), Author: publicAuthorName(original.Author, lang), Image: publicMediaReference(original.FeaturedImage, s.BaseURL), HTML: template.HTML(stdhtml.EscapeString(excerpt)), Sticky: original.Sticky, ReadMinutes: publicReadMinutesText(content)}
	card.ThumbnailURL = card.Image
	card.ImageSrcset = publicSingleImageSrcset(card.Image)
	for _, term := range original.Categories {
		card.Categories = append(card.Categories, publicLink{Label: publicTermLabel(term.Name, lang), URL: publicTermURL(term, s, lang)})
	}
	return card
}
func publicDate(value, lang string) string {
	t, err := publicTime(value)
	if err != nil {
		return ""
	}
	if lang == "zh" {
		return t.Format("2006年1月2日")
	}
	if lang == "nl" {
		months := []string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}
		return fmt.Sprintf("%d %s %d", t.Day(), months[int(t.Month())-1], t.Year())
	}
	if lang == "es" {
		months := []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"}
		return fmt.Sprintf("%d de %s de %d", t.Day(), months[int(t.Month())-1], t.Year())
	}
	if lang != "en" && isTranslationLanguage(lang) {
		return t.Format("2006-01-02")
	}
	return t.Format("January 2, 2006")
}
func publicTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, time.FixedZone("WP", 8*60*60)); err == nil && t.Year() > 0 {
			return t, nil
		}
	}
	return time.Time{}, errors.New("invalid timestamp")
}
func publicNotFound(w http.ResponseWriter, selected ...string) {
	lang := "zh"
	if len(selected) > 0 {
		lang = selected[0]
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	http.Error(w, "404 — "+publicText(lang, "页面不存在", "Page not found", "Pagina niet gevonden", "Página no encontrada"), http.StatusNotFound)
}

var publicMediaTypes = map[string]string{".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".heic": "image/heic", ".heif": "image/heif", ".ico": "image/x-icon", ".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm", ".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".ogg": "audio/ogg", ".wav": "audio/wav", ".pdf": "application/pdf", ".txt": "text/plain; charset=utf-8"}

func (a *App) servePublicMedia(w http.ResponseWriter, r *http.Request) bool {
	ext := strings.ToLower(path.Ext(r.URL.Path))
	mime, ok := publicMediaTypes[ext]
	if !ok {
		return false
	}
	file, err := publicMediaPath(a.Config.MediaDir, r.URL.Path)
	if err != nil {
		publicNotFound(w, publicRequestLanguage(r))
		return true
	}
	f, err := os.Open(file)
	if err != nil {
		publicNotFound(w, publicRequestLanguage(r))
		return true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		publicNotFound(w, publicRequestLanguage(r))
		return true
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if ext == ".pdf" || ext == ".txt" {
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	return true
}
func publicMediaPath(root, requestPath string) (string, error) {
	if root == "" || strings.ContainsAny(requestPath, "\\\x00") {
		return "", errors.New("invalid media path")
	}
	for _, segment := range strings.Split(requestPath, "/") {
		if segment == ".." || segment == "." || strings.Contains(segment, ":") {
			return "", errors.New("invalid media path")
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	file := filepath.Join(abs, filepath.FromSlash(strings.TrimPrefix(requestPath, "/")))
	real, err := filepath.EvalSymlinks(file)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("media outside root")
	}
	return real, nil
}
func publicMediaReference(value, base string) string {
	u, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if u.Hostname() == "www.ganwen.cn" || u.Hostname() == "ganwen.cn" {
		if strings.HasPrefix(u.Path, "/wp-content/uploads/") {
			b, _ := url.Parse(base)
			u.Scheme, u.Host = b.Scheme, b.Host
		}
	}
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}
func publicPlainText(input string) string {
	return publicTextCache.text(input)
}

func parsePublicPlainText(input string) string {
	nodes, err := html.ParseFragment(strings.NewReader(input), nil)
	if err != nil {
		return stdhtml.UnescapeString(input)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func publicTextAlignment(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), "!important"))
	switch value {
	case "left", "center", "right", "justify":
		return value
	}
	return ""
}

func publicTextFormatting(property, value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), "!important"))
	switch strings.ToLower(strings.TrimSpace(property)) {
	case "font-weight":
		if value == "normal" || value == "bold" {
			return "text-weight-" + value
		}
	case "font-style":
		if value == "normal" || value == "italic" {
			return "text-style-" + value
		}
	case "text-decoration":
		if value == "none" || value == "underline" || value == "line-through" {
			return "text-decoration-" + value
		}
	}
	return ""
}

func safePublicHTML(input, base string, preview ...bool) template.HTML {
	return safePublicHTMLForLanguage(input, base, "zh", preview...)
}

func safePublicHTMLForLanguage(input, base, lang string, preview ...bool) template.HTML {
	local := len(preview) > 0 && preview[0]
	reference := func(value string) string {
		value = publicMediaReference(value, base)
		if local {
			value = publicLocalLink(value)
		}
		return value
	}
	nodes, err := html.ParseFragment(strings.NewReader(input), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return template.HTML(stdhtml.EscapeString(input))
	}
	allowed := map[string]bool{}
	for _, tag := range strings.Fields("p div span section article header footer h1 h2 h3 h4 h5 h6 a img figure figcaption blockquote ul ol li dl dt dd pre code strong b em i u s del ins br hr table thead tbody tfoot tr th td video audio source picture details summary sup sub iframe") {
		allowed[tag] = true
	}
	var clean func(*html.Node)
	clean = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.CommentNode {
				n.RemoveChild(c)
			} else if c.Type == html.ElementNode {
				if !allowed[c.Data] && c.Data != "html" && c.Data != "head" && c.Data != "body" {
					if c.Data == "script" || c.Data == "style" || c.Data == "form" || c.Data == "object" || c.Data == "embed" || c.Data == "input" || c.Data == "button" {
						n.RemoveChild(c)
					} else {
						clean(c)
						for child := c.FirstChild; child != nil; {
							after := child.NextSibling
							c.RemoveChild(child)
							n.InsertBefore(child, c)
							child = after
						}
						n.RemoveChild(c)
					}
				} else {
					var attrs []html.Attribute
					var iframeSrc string
					var alignment string
					var formatting []string
					for _, a := range c.Attr {
						key := strings.ToLower(a.Key)
						if strings.HasPrefix(key, "on") || a.Namespace != "" {
							continue
						}
						if key == "style" {
							for _, declaration := range strings.Split(a.Val, ";") {
								property, value, found := strings.Cut(declaration, ":")
								if found && strings.EqualFold(strings.TrimSpace(property), "text-align") {
									if safe := publicTextAlignment(value); safe != "" {
										alignment = safe
									}
								} else if found {
									if safe := publicTextFormatting(property, value); safe != "" {
										formatting = append(formatting, safe)
									}
								}
							}
							continue
						}
						if key == "align" {
							if safe := publicTextAlignment(a.Val); safe != "" {
								alignment = safe
							}
							continue
						}
						switch key {
						case "href", "src", "poster":
							a.Val = reference(a.Val)
							if a.Val == "" {
								continue
							}
							if key == "src" && c.Data == "iframe" {
								iframeSrc = a.Val
							}
						case "srcset":
							a.Val = publicSafeSrcset(a.Val, base)
							if local {
								var parts []string
								for _, part := range strings.Split(a.Val, ",") {
									fields := strings.Fields(part)
									if len(fields) > 0 {
										fields[0] = publicLocalLink(fields[0])
										parts = append(parts, strings.Join(fields, " "))
									}
								}
								a.Val = strings.Join(parts, ", ")
							}
							if a.Val == "" {
								continue
							}
						case "rel":
							continue
						case "class", "id", "title", "alt", "width", "height", "sizes", "loading", "decoding", "controls", "loop", "muted", "preload", "type", "colspan", "rowspan", "datetime", "cite", "start", "reversed", "open", "target", "allowfullscreen":
						default:
							if !strings.HasPrefix(key, "aria-") {
								continue
							}
						}
						attrs = append(attrs, a)
					}
					c.Attr = attrs
					if alignment != "" {
						formatting = append(formatting, "text-align-"+alignment)
					}
					if len(formatting) > 0 {
						classIndex := -1
						for i := range c.Attr {
							if c.Attr[i].Key == "class" {
								classIndex = i
								break
							}
						}
						if classIndex >= 0 {
							c.Attr[classIndex].Val += " " + strings.Join(formatting, " ")
						} else {
							c.Attr = append(c.Attr, html.Attribute{Key: "class", Val: strings.Join(formatting, " ")})
						}
					}
					if c.Data == "a" {
						c.Attr = append(c.Attr, html.Attribute{Key: "rel", Val: "noopener noreferrer"})
					}
					if c.Data == "img" {
						c.Attr = append(c.Attr, html.Attribute{Key: "loading", Val: "lazy"})
					}
					if c.Data == "iframe" {
						u, e := url.Parse(iframeSrc)
						if e == nil && publicInternalEmbed(u, base) {
							c.Data, c.DataAtom = "a", atom.A
							q := url.Values{}
							for _, key := range []string{"p", "page_id", "attachment_id"} {
								if u.Query().Get(key) != "" {
									q.Set(key, u.Query().Get(key))
								}
							}
							c.Attr = []html.Attribute{{Key: "href", Val: reference(base + "/?" + q.Encode())}, {Key: "class", Val: "embedded-post-link"}}
							for c.FirstChild != nil {
								c.RemoveChild(c.FirstChild)
							}
							label := publicText(lang, "查看引用文章", "View referenced article", "Bekijk het aangehaalde artikel", "Ver el artículo citado")
							c.AppendChild(&html.Node{Type: html.TextNode, Data: label + " →"})
							clean(c)
							c = next
							continue
						}
						if e != nil || u.Scheme != "https" || !(u.Hostname() == "www.youtube.com" || u.Hostname() == "www.youtube-nocookie.com" || u.Hostname() == "player.vimeo.com") {
							n.RemoveChild(c)
							c = next
							continue
						}
						c.Attr = append(c.Attr, html.Attribute{Key: "sandbox", Val: "allow-scripts allow-same-origin allow-presentation"}, html.Attribute{Key: "loading", Val: "lazy"})
					}
					clean(c)
				}
			}
			c = next
		}
	}
	container := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		container.AppendChild(n)
	}
	clean(container)
	var b bytes.Buffer
	for n := container.FirstChild; n != nil; n = n.NextSibling {
		_ = html.Render(&b, n)
	}
	return template.HTML(b.String())
}
func publicInternalEmbed(u *url.URL, base string) bool {
	if u == nil || (u.Scheme != "" && u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	b, err := url.Parse(base)
	if err != nil {
		return false
	}
	if u.Host != "" && u.Hostname() != b.Hostname() && u.Hostname() != "ganwen.nl" && u.Hostname() != "www.ganwen.nl" {
		return false
	}
	if u.Path != "" && u.Path != "/" && u.Path != "/index.php" {
		return false
	}
	for _, key := range []string{"p", "page_id", "attachment_id"} {
		if id, err := strconv.ParseInt(u.Query().Get(key), 10, 64); err == nil && id > 0 {
			return true
		}
	}
	return false
}
func publicSafeSrcset(value, base string) string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		u := publicMediaReference(fields[0], base)
		if u == "" {
			continue
		}
		valid := true
		for _, descriptor := range fields[1:] {
			if len(descriptor) < 2 || !strings.ContainsAny(descriptor[len(descriptor)-1:], "wx") {
				valid = false
				break
			}
			if _, err := strconv.ParseFloat(descriptor[:len(descriptor)-1], 64); err != nil {
				valid = false
				break
			}
		}
		if valid {
			fields[0] = u
			result = append(result, strings.Join(fields, " "))
		}
	}
	return strings.Join(result, ", ")
}

type publicSitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}
type publicSitemap struct {
	XMLName xml.Name           `xml:"urlset"`
	XMLNS   string             `xml:"xmlns,attr"`
	URLs    []publicSitemapURL `xml:"url"`
}
type publicSitemapIndex struct {
	XMLName xml.Name           `xml:"sitemapindex"`
	XMLNS   string             `xml:"xmlns,attr"`
	Maps    []publicSitemapURL `xml:"sitemap"`
}

func publicXML(w http.ResponseWriter, value any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	}
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(value)
}
func (a *App) servePublicSitemap(w http.ResponseWriter, r *http.Request, s SiteSettings, posts []Post) {
	q := r.URL.Query()
	kind := q.Get("sitemap")
	sub := q.Get("sitemap-subtype")
	if kind == "" || kind == "index" {
		index := publicSitemapIndex{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
		for _, item := range []string{"posts-post-1", "posts-page-1", "taxonomies-category-1", "taxonomies-post_tag-1", "taxonomies-post_format-1", "users-1"} {
			index.Maps = append(index.Maps, publicSitemapURL{Loc: s.BaseURL + "/?sitemap=" + item})
		}
		publicXML(w, index)
		return
	}
	if strings.HasPrefix(kind, "posts-") {
		parts := strings.Split(kind, "-")
		if len(parts) != 3 || parts[2] != "1" {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		kind, sub = "posts", parts[1]
	}
	if strings.HasPrefix(kind, "taxonomies-") {
		parts := strings.Split(kind, "-")
		if len(parts) != 3 || parts[2] != "1" {
			publicNotFound(w, publicRequestLanguage(r))
			return
		}
		kind, sub = "taxonomies", parts[1]
	}
	if kind == "users-1" {
		kind = "users"
	}
	if q.Has("paged") && q.Get("paged") != "1" {
		publicNotFound(w, publicRequestLanguage(r))
		return
	}
	set := publicSitemap{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	seen := map[string]bool{}
	add := func(loc, modified string) {
		if loc == "" || seen[loc] {
			return
		}
		seen[loc] = true
		if t, e := publicTime(modified); e == nil {
			modified = t.Format(time.RFC3339)
		} else {
			modified = ""
		}
		set.URLs = append(set.URLs, publicSitemapURL{Loc: loc, LastMod: modified})
	}
	if kind == "posts" && (sub == "post" || sub == "page") {
		if sub == "page" {
			add(s.BaseURL+"/", "")
		}
		for _, p := range posts {
			if p.Status != "publish" || p.Type != sub {
				continue
			}
			sourceHash := publicTranslationSourceHash(p)
			for _, lang := range publicLanguages() {
				if _, ok := localizedPostWithSourceHash(p, lang, sourceHash); ok {
					add(publicPostURL(p, s, lang), publicModifiedTimestamp(p, lang))
				}
			}
		}
	}
	if kind == "taxonomies" && (sub == "category" || sub == "post_tag" || sub == "post_format") {
		for _, p := range posts {
			if p.Status != "publish" || p.Type != "post" {
				continue
			}
			sourceHash := publicTranslationSourceHash(p)
			for _, lang := range publicLanguages() {
				if _, ok := localizedPostWithSourceHash(p, lang, sourceHash); !ok {
					continue
				}
				if sub == "post_format" {
					if p.Format != "" && p.Format != "standard" {
						add(publicArchiveURL(s, lang, url.Values{"post_format": {p.Format}}), "")
					}
					continue
				}
				terms := p.Categories
				if sub == "post_tag" {
					terms = p.Tags
				}
				for _, t := range terms {
					add(publicTermURL(t, s, lang), "")
				}
			}
		}
	}
	if kind == "users" {
		for _, p := range posts {
			if p.Status != "publish" || p.Type != "post" || p.AuthorID <= 0 {
				continue
			}
			sourceHash := publicTranslationSourceHash(p)
			for _, lang := range publicLanguages() {
				if _, ok := localizedPostWithSourceHash(p, lang, sourceHash); ok {
					add(publicArchiveURL(s, lang, url.Values{"author": {strconv.FormatInt(p.AuthorID, 10)}}), "")
				}
			}
		}
	}
	if !(kind == "posts" && (sub == "post" || sub == "page")) && !(kind == "taxonomies" && (sub == "category" || sub == "post_tag" || sub == "post_format")) && kind != "users" {
		publicNotFound(w, publicRequestLanguage(r))
		return
	}
	publicXML(w, set)
}

func publicContactIcon(kind string) template.HTML {
	brand := map[string]string{
		"gmail":     `<path fill="#4285f4" d="M2 6v14h4V9L2 6Z"/><path fill="#34a853" d="M18 9v11h4V6l-4 3Z"/><path fill="#ea4335" d="m6 9 6 4.5L18 9V4l-6 4.5L6 4v5Z"/><path fill="#fbbc04" d="m18 4 4-3v5l-4 3V4Z"/><path fill="#c5221f" d="M2 1v5l4 3V4L2 1Z"/>`,
		"outlook":   `<path fill="#1479c9" d="M10 4h13v15H10V4Z"/><path fill="#28a8ea" d="m10 8 6.5 4L23 8v11H10V8Z"/><path fill="#0364b8" d="M1 5h13v15H1V5Z"/><path fill="#fff" fill-rule="evenodd" d="M7.5 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8Zm0 1.6a2.4 2.4 0 1 1 0 4.8 2.4 2.4 0 0 1 0-4.8Z"/>`,
		"yahoo":     `<path fill="#a079ff" d="M1 4h5l4 7 4-7h5l-7 11v5H8v-5L1 4Zm19 0h3l-1 11h-2V4Zm0 13h3v3h-3v-3Z"/>`,
		"icloud":    `<path fill="#329ef0" d="M7 19a5 5 0 0 1-1-9.9 6 6 0 0 1 11.7-.8A5.4 5.4 0 1 1 18 19H7Z"/>`,
		"proton":    `<path fill="#9275ff" d="M2 5h20v15H2V5Zm3 2 7 6 7-6H5Zm-1 3v8h16v-8l-8 6-8-6Z"/>`,
		"instagram": `<rect x="3" y="3" width="18" height="18" rx="5" fill="none" stroke="#d36aa8" stroke-width="2"/><circle cx="12" cy="12" r="4" fill="none" stroke="#d36aa8" stroke-width="2"/><circle cx="17.5" cy="6.5" r="1.2" fill="#d36aa8"/>`,
		"youtube":   `<rect x="1" y="4" width="22" height="16" rx="5" fill="#f34141"/><path fill="#fff" d="m10 8 7 4-7 4V8Z"/>`,
		"facebook":  `<path fill="#438fef" d="M13 23v-9h3l.5-4H13V7.5c0-1 .3-1.5 1.5-1.5H17V2.2c-.5-.1-2-.2-3.5-.2C10 2 8 4.1 8 7.4V10H5v4h3v9h5Z"/>`,
		"linkedin":  `<rect x="1" y="1" width="22" height="22" rx="3" fill="#167ab8"/><path fill="#fff" d="M5 9h3v10H5V9Zm1.5-4a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3ZM10 9h3v1.3A3.5 3.5 0 0 1 16 9c2.5 0 3.5 1.5 3.5 4V19h-3v-5.5c0-1.2-.5-1.9-1.5-1.9s-2 .7-2 2V19h-3V9Z"/>`,
	}
	if content := brand[kind]; content != "" {
		return template.HTML(`<svg class="contact-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">` + content + `</svg>`)
	}
	paths := map[string]string{
		"x":        "M18.9 2H22l-6.8 7.8L23.2 22h-6.3l-4.9-6.4L6.4 22H3.3l7.3-8.4L.8 2h6.5l4.4 5.8L18.9 2ZM17.8 20h1.7L6.4 3.9H4.6L17.8 20Z",
		"email":    "M3 4h18a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2Zm0 2 9 7 9-7H3Z",
		"telegram": "M21.7 2.4 2.4 9.9c-1.3.5-1.3 1.2-.2 1.6l4.9 1.5L18.5 6c.5-.3 1-.1.6.3l-9.2 8.3-.4 5.4c.6 0 .9-.3 1.2-.6l2.4-2.3 5 3.7c.9.5 1.5.3 1.7-.9l3.1-15.7c.3-1.4-.5-2-1.2-1.8Z",
		"github":   "M12 .8a11.2 11.2 0 0 0-3.5 21.8c.6.1.8-.2.8-.5v-2.2c-3.3.7-4-1.4-4-1.4-.5-1.3-1.3-1.7-1.3-1.7-1.1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1.1 1.8 2.7 1.3 3.4 1 .1-.8.4-1.3.8-1.6-2.7-.3-5.5-1.3-5.5-5.7 0-1.3.5-2.3 1.2-3.1-.1-.3-.5-1.6.1-3.2 0 0 1-.3 3.2 1.2a11 11 0 0 1 5.8 0c2.2-1.5 3.2-1.2 3.2-1.2.6 1.6.2 2.9.1 3.2.8.8 1.2 1.8 1.2 3.1 0 4.4-2.8 5.4-5.5 5.7.4.4.8 1.1.8 2.2v3.2c0 .3.2.6.8.5A11.2 11.2 0 0 0 12 .8Z",
		"mastodon": "M12 1c-5.5 0-9.6 1.5-9.6 6.6v6.5c0 5.8 3.8 8.9 8.8 8.9 2.3 0 4.1-.5 5.5-1.3l-.2-2.3c-1.5.6-3 .9-5 .9-2.6 0-4.3-.9-4.5-3.4 1.7.4 3.3.6 5.1.6 4.2 0 8.9-.6 9.5-5.5.2-1.7.2-3 .2-4.4C21.8 2.5 17.5 1 12 1Zm-5.8 5.5c.5-1.1 1.3-1.6 2.5-1.6 1.4 0 2.5.6 3.3 1.8.8-1.2 1.9-1.8 3.3-1.8 1.2 0 2 .5 2.5 1.6.3.7.5 1.5.5 2.5v5.7h-2.7V9.2c0-1.2-.5-1.8-1.5-1.8-1.1 0-1.6.7-1.6 2.1v3h-1V9.5c0-1.4-.5-2.1-1.6-2.1-1 0-1.5.6-1.5 1.8v5.5H5.7V9c0-1 .2-1.8.5-2.5Z",
		"website":  "M12 1a11 11 0 1 0 0 22 11 11 0 0 0 0-22Zm-1 2h2l2 7H9l2-7ZM3 10a9 9 0 0 1 5-6l-1 6H3Zm0 4h4l1 6a9 9 0 0 1-5-6Zm6 0h6l-2 7h-2l-2-7Zm8 0h4a9 9 0 0 1-5 6l1-6Zm-1-10a9 9 0 0 1 5 6h-4l-1-6Z",
	}
	if paths[kind] == "" {
		return ""
	}
	return template.HTML(`<svg class="contact-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="currentColor" fill-rule="evenodd" d="` + paths[kind] + `"/></svg>`)
}

func publicIcon(name string) template.HTML {
	paths := map[string]string{
		"home":    "M12 2 1 11h3v11h6v-7h4v7h6V11h3L12 2Z",
		"code":    "m8 4-8 8 8 8 2-2-6-6 6-6-2-2Zm8 0-2 2 6 6-6 6 2 2 8-8-8-8Zm-3-2-4 20h3l4-20h-3Z",
		"article": "M3 2h18v20H3V2Zm3 4v3h12V6H6Zm0 6v2h12v-2H6Zm0 5v2h8v-2H6Z",
		"globe":   "M12 1a11 11 0 1 0 0 22 11 11 0 0 0 0-22Zm-1 2h2l2 7H9l2-7ZM3 10a9 9 0 0 1 5-6l-1 6H3Zm0 4h4l1 6a9 9 0 0 1-5-6Zm6 0h6l-2 7h-2l-2-7Zm8 0h4a9 9 0 0 1-5 6l1-6Zm-1-10a9 9 0 0 1 5 6h-4l-1-6Z",
		"chat":    "M2 2h20v15H9l-7 5V2Zm4 5v2h12V7H6Zm0 5v2h9v-2H6Z",
		"book":    "M2 2h9v18H4l-2 2V2Zm11 0h9v20l-2-2h-7V2Z",
		"search":  "M10 1a9 9 0 1 0 5.8 15.9L22 23l2-2-6.1-6.2A9 9 0 0 0 10 1Zm0 3a6 6 0 1 1 0 12 6 6 0 0 1 0-12Z",
		"menu":    "M2 4h20v3H2V4Zm0 7h20v3H2v-3Zm0 7h20v3H2v-3Z",
		"sun":     "M11 0h2v4h-2V0Zm0 20h2v4h-2v-4ZM0 11h4v2H0v-2Zm20 0h4v2h-4v-2ZM3.5 2.1 6.3 5 4.9 6.4 2.1 3.5l1.4-1.4ZM19.1 17.6l2.8 2.9-1.4 1.4-2.8-2.9 1.4-1.4ZM2.1 20.5l2.8-2.9 1.4 1.4-2.8 2.9-1.4-1.4ZM17.7 5l2.8-2.9 1.4 1.4-2.8 2.9L17.7 5ZM12 6a6 6 0 1 0 0 12 6 6 0 0 0 0-12Zm0 2a4 4 0 1 1 0 8 4 4 0 0 1 0-8Z",
		"moon":    "M20.9 15.4A9 9 0 0 1 8.6 3.1a9.5 9.5 0 1 0 12.3 12.3ZM6 5.7a11 11 0 0 0 12.3 12.3A7.5 7.5 0 1 1 6 5.7Z",
		"monitor": "M3 3h18a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-8v2h4v2H7v-2h4v-2H3a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Zm0 2v12h18V5H3Z",
		"edit":    "m16 2 6 6-13 13-7 1 1-7L16 2Zm0 3L5 16l-.5 3.5L8 19 19 8l-3-3Z",
		"close":   "m5 3 7 7 7-7 2 2-7 7 7 7-2 2-7-7-7 7-2-2 7-7-7-7 2-2Z",
	}
	return template.HTML(`<svg class="icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="currentColor" d="` + paths[name] + `"/></svg>`)
}
