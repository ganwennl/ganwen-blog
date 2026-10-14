package blog

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

func TestAdditionalLanguageMessagesCoverPublicInterface(t *testing.T) {
	required := []string{
		"没有匹配的文章，可调整分类或标签。", "搜索", "页码", "文章", "保持联系", "返回列表", "上一篇", "下一篇", "跳转", "快捷键",
		"Esc 返回列表 · ← / → 切换文章或页码 · / 搜索 · ? 显示快捷键", "分类", "标签", "清除筛选", "较早文章", "较新文章", "阅读全文", "暂时没有文章", "菜单", "邮箱", "网站",
		"首页", "关于", "语言", "外观", "浅色", "深色", "跟随系统", "搜索文章", "编辑文章", "回到顶部", "全部", "全部文章", "置顶", "关闭", "文章编辑器", "管理",
		"版式", "日志阅读", "经典卡片", "跳至正文", "精选阅读", "最近更新", "按时间阅读", "阅读约", "分钟", "文章总数", "浏览全部文章", "关于作者", "文章目录",
		"博客侧栏", "输入关键词", "热门文章", "近30天", "次浏览", "暂无浏览记录", "站点统计", "今日浏览", "近365天浏览", "历史总访问", "广告", "查看详情", "推广方", "联系",
		"意境", "技术", "感触", "偶遇", "情感", "爱情", "秀逗", "风景", "健康", "创业", "经验", "音乐",
		"中国", "加密货币", "区块链", "新冠肺炎", "金融", "防火长城",
		"习近平", "偷渡出境", "夏津公安", "夏津县", "护照激活", "拆墙运动", "推墙运动", "推墙运动习近平", "取保候审", "甘文", "政治", "甘文的个人博客", "查看引用文章", "简体中文",
	}
	for _, lang := range []string{"hi", "ar", "fr", "bn", "pt", "id", "ur"} {
		for _, key := range required {
			value, ok := publicMessage(lang, key)
			if !ok || strings.TrimSpace(value) == "" || strings.IndexFunc(value, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
				t.Errorf("%s: missing/untranslated interface message %q", lang, key)
			}
			if got := publicText(lang, key, "English fallback", "Dutch fallback", "Spanish fallback"); got != value {
				t.Errorf("%s: interface dictionary bypassed for %q", lang, key)
			}
		}
	}
}

func TestPublicSanitizerLocalizesEmbeddedPostLinks(t *testing.T) {
	const base = "https://www.ganwen.nl"
	const input = `<iframe src="https://www.ganwen.nl/?p=1653&amp;embed=true#secret" title="Referenced article"></iframe>`
	for _, test := range []struct{ lang, label string }{
		{"zh", "查看引用文章"},
		{"nl", "Bekijk het aangehaalde artikel"},
		{"en", "View referenced article"},
		{"hi", "संदर्भित लेख देखें"},
		{"es", "Ver el artículo citado"},
		{"ar", "عرض المقال المشار إليه"},
		{"fr", "Voir l’article cité"},
		{"bn", "উল্লেখিত লেখাটি দেখুন"},
		{"pt", "Ver o artigo citado"},
		{"id", "Lihat artikel yang dirujuk"},
		{"ur", "حوالہ دیا گیا مضمون دیکھیں"},
	} {
		t.Run(test.lang, func(t *testing.T) {
			for _, preview := range []bool{false, true} {
				out := string(safePublicHTMLForLanguage(input, base, test.lang, preview))
				links := publicMetadataNodes(t, out, "a", "embedded-post-link")
				if len(links) != 1 || strings.Contains(out, "<iframe") {
					t.Fatal("internal embed was not converted to one article link")
				}
				wantURL := base + "/?p=1653"
				if preview {
					wantURL = "/?p=1653"
				}
				if got := publicMetadataAttr(links[0], "href"); got != wantURL {
					t.Fatalf("referenced original article URL changed: %q, want %q", got, wantURL)
				}
				label := publicMetadataText(links[0])
				if label != test.label+" →" {
					t.Fatalf("referenced article label = %q, want %q", label, test.label+" →")
				}
				if test.lang != "zh" && strings.IndexFunc(label, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
					t.Fatal("foreign reference link contains untranslated Chinese")
				}
				if test.lang == "zh" && out != string(safePublicHTML(input, base, preview)) {
					t.Fatal("legacy Chinese sanitizer wrapper changed")
				}
			}
		})
	}
}

func TestPublicEmbeddedPostLinksLocalizedInPages(t *testing.T) {
	a, settings, post := publicMetadataFixture(t)
	embed := `<iframe src="` + settings.BaseURL + `/?p=1653&amp;embed=true#secret"></iframe>`
	post.ContentHTML += embed
	for _, lang := range translationLanguages() {
		translation := post.Translations[lang]
		translation.ContentHTML += embed
		translation.SourceHash = SourceHash(post)
		post.Translations[lang] = translation
	}
	if err := a.Store.SavePost(context.Background(), &post); err != nil {
		t.Fatal(err)
	}
	get := publicMetadataClient(t, a)
	for _, lang := range translationLanguages() {
		t.Run(lang, func(t *testing.T) {
			checkLink := func(body string) {
				t.Helper()
				links := publicMetadataNodes(t, body, "a", "embedded-post-link")
				if len(links) != 1 {
					t.Fatalf("expected one embedded article link, got %d", len(links))
				}
				if got := publicMetadataAttr(links[0], "href"); got != settings.BaseURL+"/?p=1653" {
					t.Fatalf("reference target no longer uses original article URL: %q", got)
				}
				label := publicMetadataText(links[0])
				want := publicText(lang, "查看引用文章", "View referenced article", "Bekijk het aangehaalde artikel", "Ver el artículo citado") + " →"
				if label != want || strings.IndexFunc(label, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
					t.Fatalf("page reference label was not localized: %q, want %q", label, want)
				}
			}
			prefix := publicLanguagePrefix(lang)
			checkLink(get(prefix + "/?p=" + strconv.FormatInt(post.ID, 10)))
		})
	}
}

func TestAllLanguagesRenderIdentityTaxonomiesAndMetadata(t *testing.T) {
	a, settings, post := publicMetadataFixture(t)
	post.Author = "甘"
	if err := a.Store.SavePost(context.Background(), &post); err != nil {
		t.Fatal(err)
	}
	get := publicMetadataClient(t, a)
	for _, lang := range translationLanguages() {
		t.Run(lang, func(t *testing.T) {
			prefix := publicLanguagePrefix(lang)
			body := get(prefix + "/?p=" + strconv.FormatInt(post.ID, 10))
			if !strings.Contains(body, "Wen Gan") || strings.Contains(body, " · 甘") {
				t.Fatal("foreign author identity was not localized")
			}
			for _, tag := range publicMetadataNodes(t, body, "footer", "post-tags") {
				if strings.IndexFunc(publicMetadataText(tag), func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
					t.Fatal("article tags contain untranslated Chinese")
				}
			}
			home := get(prefix + "/")
			localized := localizedPublicSettings(settings, lang)
			if publicMetadataDescription(t, home) != localized.Description || localized.Title != "Ganwen" {
				t.Fatal("foreign metadata does not preserve the brand and localize the description")
			}
		})
	}
}

func TestForeignAuthorNameDoesNotRenameOtherPeople(t *testing.T) {
	for _, lang := range publicLanguages() {
		for _, name := range []string{"甘", "甘文", "ganwen", "Ganwen", "GANWEN", "GaNwEn"} {
			want := "Wen Gan"
			if lang == "zh" {
				want = name
			}
			if got := publicAuthorName(name, lang); got != want {
				t.Fatalf("%s/%s: %q", lang, name, got)
			}
		}
		for _, name := range []string{"Other Author", "Wen Gan", "ganwen fan", "Ganwen Zhang"} {
			if got := publicAuthorName(name, lang); got != name {
				t.Fatalf("unrelated author %q changed to %q", name, got)
			}
		}
	}
}

func TestPublicSimplifiedChineseTagAndLoginAliasLocalizedHTTP(t *testing.T) {
	a, settings, post := publicMetadataFixture(t)
	term := Term{ID: 999, Name: "简体中文", Slug: "简体中文", Taxonomy: "post_tag", URL: settings.BaseURL + "/?tag=" + url.QueryEscape("简体中文")}
	post.Tags = []Term{term}
	post.Author = "ganwen"
	for _, lang := range translationLanguages() {
		translation := post.Translations[lang]
		translation.SourceHash = SourceHash(post)
		post.Translations[lang] = translation
	}
	if err := a.Store.SavePost(context.Background(), &post); err != nil {
		t.Fatal(err)
	}
	get := publicMetadataClient(t, a)
	for _, test := range []struct{ lang, label string }{
		{"nl", "Vereenvoudigd Chinees"},
		{"en", "Simplified Chinese"},
		{"hi", "सरलीकृत चीनी"},
		{"es", "Chino simplificado"},
		{"ar", "الصينية المبسطة"},
		{"fr", "Chinois simplifié"},
		{"bn", "সরলীকৃত চীনা"},
		{"pt", "Chinês simplificado"},
		{"id", "Mandarin sederhana"},
		{"ur", "آسان چینی"},
	} {
		t.Run(test.lang, func(t *testing.T) {
			if got := publicTermLabel(term.Name, test.lang); got != test.label {
				t.Fatalf("new tag label = %q, want %q", got, test.label)
			}
			prefix := publicLanguagePrefix(test.lang)
			article := get(prefix + "/?p=" + strconv.FormatInt(post.ID, 10))
			if !strings.Contains(publicMetadataOneText(t, article, "p", "post-meta"), "Wen Gan") {
				t.Fatal("foreign login alias was not displayed as Wen Gan")
			}
			if localizedPublicSettings(settings, test.lang).Title != "Ganwen" {
				t.Fatal("author localization changed the blog brand")
			}
			for _, body := range []string{get(prefix + "/"), article, get(prefix + "/?tag=" + url.QueryEscape(term.Slug))} {
				found := false
				for _, link := range publicMetadataNodes(t, body, "a", "") {
					if publicMetadataAttr(link, "lang") != "" {
						continue
					}
					isTagLink := false
					for parent := link.Parent; parent != nil; parent = parent.Parent {
						classes := " " + publicMetadataAttr(parent, "class") + " "
						if strings.Contains(classes, " tag-tabs ") || strings.Contains(classes, " post-tags ") {
							isTagLink = true
							break
						}
					}
					if !isTagLink {
						continue
					}
					parsed, err := url.Parse(publicMetadataAttr(link, "href"))
					if err != nil || parsed.Query().Get("tag") != term.Slug {
						continue
					}
					label := strings.TrimPrefix(publicMetadataText(link), "#")
					if label != test.label || strings.IndexFunc(label, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
						t.Fatalf("public new tag contains untranslated or incorrect label: %q", label)
					}
					found = true
				}
				if !found {
					t.Fatal("published tag absent from home/article/filter navigation")
				}
			}
		})
	}
	if publicTermLabel(term.Name, "zh") != term.Name {
		t.Fatal("original Chinese tag changed")
	}
}
