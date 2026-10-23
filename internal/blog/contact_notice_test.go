package blog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestAdminContactNoticeSaveCompatibilityAndDisable(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	input := map[string]any{"title": "联系设置", "posts_per_page": 10}
	code, raw := c.request("GET", "settings", nil, "", "")
	var saved SiteSettings
	if code != 200 || json.Unmarshal(raw, &saved) != nil || saved.ContactNoticeEnabled || saved.ContactNoticeText != "" {
		t.Fatalf("legacy settings did not default to disabled: %d %s", code, raw)
	}
	text := "  请先说明来意。\n请勿发送广告。\t<script>alert(1)</script> 😀  "
	input["contact_notice_enabled"], input["contact_notice_text"] = true, text
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != 200 || json.Unmarshal(raw, &saved) != nil || !saved.ContactNoticeEnabled || saved.ContactNoticeText != text {
		t.Fatalf("contact notice failed to save literally: %d %s", code, raw)
	}
	code, raw = c.request("GET", "settings", nil, "", "")
	if code != 200 || json.Unmarshal(raw, &saved) != nil || !saved.ContactNoticeEnabled || saved.ContactNoticeText != text {
		t.Fatalf("saved notice did not round trip: %d %s", code, raw)
	}
	delete(input, "contact_notice_enabled")
	delete(input, "contact_notice_text")
	input["title"] = "旧客户端更新"
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != 200 || json.Unmarshal(raw, &saved) != nil || !saved.ContactNoticeEnabled || saved.ContactNoticeText != text {
		t.Fatalf("old client erased contact notice: %d %s", code, raw)
	}
	input["contact_notice_text"] = strings.Repeat("😀", contactNoticeMaxRunes)
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	if code != 200 || json.Unmarshal(raw, &saved) != nil || !saved.ContactNoticeEnabled || saved.ContactNoticeText != input["contact_notice_text"] {
		t.Fatalf("text-only Unicode-limit update failed: %d %s", code, raw)
	}
	delete(input, "contact_notice_text")
	input["contact_notice_enabled"] = false
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	saved = SiteSettings{}
	if code != 200 || json.Unmarshal(raw, &saved) != nil || saved.ContactNoticeEnabled || saved.ContactNoticeText == "" {
		t.Fatalf("explicit disable erased reusable text: %d %s", code, raw)
	}
	input["contact_notice_text"] = ""
	code, raw = c.request("POST", "settings", input, c.server.URL, c.csrf)
	stored, err := c.app.Store.Settings(ctx)
	if code != 200 || err != nil || stored.ContactNoticeEnabled || stored.ContactNoticeText != "" {
		t.Fatalf("disabled notice could not be cleared: %d %s %v", code, raw, err)
	}
}

func TestAdminContactNoticeRejectsInvalidWithoutChangingSettings(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	baseline := SiteSettings{Title: "原设置", BaseURL: "https://www.ganwen.nl", PostsPerPage: 10, ContactNoticeEnabled: true, ContactNoticeText: "请先说明来意。"}
	if err := c.app.Store.SaveSettings(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	before, _ := c.app.Store.GetValue(ctx, "site")
	for _, test := range []struct {
		name, field, message string
		value                any
	}{
		{"null switch", "contact_notice_enabled", "联系提示开关不正确", nil},
		{"string switch", "contact_notice_enabled", "联系提示开关不正确", "false"},
		{"number switch", "contact_notice_enabled", "联系提示开关不正确", 0},
		{"null text", "contact_notice_text", "联系提示请使用不超过 4000 字的纯文本", nil},
		{"array text", "contact_notice_text", "联系提示请使用不超过 4000 字的纯文本", []string{"notice"}},
		{"empty text", "contact_notice_text", "启用联系提示前请填写提示内容", ""},
		{"blank text", "contact_notice_text", "启用联系提示前请填写提示内容", " \t\r\n\u3000"},
		{"long text", "contact_notice_text", "联系提示请使用不超过 4000 字的纯文本", strings.Repeat("联", contactNoticeMaxRunes+1)},
		{"control text", "contact_notice_text", "联系提示请使用不超过 4000 字的纯文本", "提示\x00内容"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"title": "不应保存", "posts_per_page": 8, test.field: test.value}
			code, raw := c.request("POST", "settings", input, c.server.URL, c.csrf)
			if code != 400 || !strings.Contains(string(raw), test.message) {
				t.Fatalf("invalid notice accepted or wrong validation error: %d %s", code, raw)
			}
			if after, err := c.app.Store.GetValue(ctx, "site"); err != nil || after != before {
				t.Fatal("failed notice update changed stored settings")
			}
		})
	}
	code, _ := c.request("POST", "settings", map[string]any{"title": "未经授权", "posts_per_page": 8, "contact_notice_enabled": false}, c.server.URL, "invalid-csrf")
	if code != 403 {
		t.Fatalf("notice setting bypassed CSRF: %d", code)
	}
	if after, _ := c.app.Store.GetValue(ctx, "site"); after != before {
		t.Fatal("CSRF failure changed contact notice")
	}
}

func TestSaveSettingsValidatesContactNotice(t *testing.T) {
	a := publicTestApp(t)
	ctx := context.Background()
	baseline, err := a.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := a.Store.GetValue(ctx, "site")
	for _, bad := range []SiteSettings{
		{ContactNoticeEnabled: true},
		{ContactNoticeText: strings.Repeat("😀", contactNoticeMaxRunes+1)},
		{ContactNoticeText: "invalid\xff"},
		{ContactNoticeText: "invalid\x7f"},
	} {
		bad.Title, bad.BaseURL = "不应保存", baseline.BaseURL
		if err := a.Store.SaveSettings(ctx, bad); err == nil {
			t.Fatal("direct SaveSettings accepted invalid contact notice")
		}
		if after, _ := a.Store.GetValue(ctx, "site"); after != before {
			t.Fatal("invalid direct save changed site settings")
		}
	}
}

func TestContactNoticeImportsValidateBeforeWriting(t *testing.T) {
	for _, fields := range []string{
		`"contact_notice_enabled":true`,
		`"contact_notice_enabled":null`,
		`"contact_notice_enabled":"true","contact_notice_text":"notice"`,
		`"contact_notice_text":null`,
		`"contact_notice_text":7`,
		`"contact_notice_text":"invalid\u0000"`,
		`"contact_notice_text":"` + strings.Repeat("界", contactNoticeMaxRunes+1) + `"`,
	} {
		t.Run(fields[:min(len(fields), 55)], func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenStore(filepath.Join(dir, "blog.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			before, _ := store.DatabaseSnapshot(ctx)
			site := `{"title":"待导入","base_url":"https://www.ganwen.nl","posts_per_page":10,` + fields + `}`
			posts := []Post{{ID: 42, Type: "post", Status: "publish", Title: "不能部分导入"}}
			backup := DatabaseBackup{Format: "ganwen-blog-database", Version: 1, Posts: posts, Values: []DatabaseValue{{Key: "unrelated", Value: "preserve"}, {Key: "site", Value: site}}}
			if err := store.RestoreDatabase(ctx, backup); err == nil {
				t.Fatal("database restore accepted invalid contact notice")
			}
			backupFile := filepath.Join(dir, "backup.json")
			raw, _ := json.Marshal(backup)
			if err := os.WriteFile(backupFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.ImportDatabaseFile(ctx, backupFile, "", dir); err == nil {
				t.Fatal("database file import accepted invalid contact notice")
			}
			exportFile := filepath.Join(dir, "wordpress.json")
			raw, _ = json.Marshal(map[string]any{"settings": json.RawMessage(site), "posts": posts})
			if err := os.WriteFile(exportFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ImportFile(ctx, exportFile); err == nil {
				t.Fatal("WordPress import accepted invalid contact notice")
			}
			after, err := store.DatabaseSnapshot(ctx)
			if err != nil || !reflect.DeepEqual(before.Posts, after.Posts) || !reflect.DeepEqual(before.Values, after.Values) || !reflect.DeepEqual(before.Analytics, after.Analytics) {
				t.Fatal("rejected import partially wrote database rows")
			}
		})
	}
}

func TestContactNoticeImportsPreserveValidAndLegacySettings(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, wordpress := range []bool{false, true} {
			dir := t.TempDir()
			store, err := OpenStore(filepath.Join(dir, "blog.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			settings := SiteSettings{Title: "导入设置", BaseURL: "https://www.ganwen.nl", PostsPerPage: 8, ContactNoticeEnabled: enabled}
			if enabled {
				settings.ContactNoticeText = "请先说明来意。\n拒绝广告。😀"
			}
			var raw []byte
			if wordpress {
				raw, _ = json.Marshal(Export{Settings: settings, Posts: []Post{{ID: 42, Type: "post", Status: "publish", Title: "可导入"}}})
			} else {
				site, _ := json.Marshal(settings)
				raw, _ = json.Marshal(DatabaseBackup{Format: "ganwen-blog-database", Version: 1, Values: []DatabaseValue{{Key: "site", Value: string(site)}}})
			}
			file := filepath.Join(dir, "import.json")
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if wordpress {
				_, err = store.ImportFile(ctx, file)
			} else {
				err = store.ImportDatabaseFile(ctx, file, "", dir)
			}
			if err != nil {
				t.Fatal("valid contact notice import rejected", err)
			}
			got, err := store.Settings(ctx)
			if err != nil || !reflect.DeepEqual(got, settings) {
				t.Fatalf("import changed notice settings: %+v %v", got, err)
			}
		}
	}
	for _, legacy := range []string{`{"title":7}`, `{"unknown_legacy_field":true}`, `legacy-site-value`} {
		if err := validateContactNoticeSettingsJSON([]byte(legacy)); err != nil {
			t.Fatal("contact notice validation changed unrelated legacy restore contract", err)
		}
	}
}

func TestPublicContactNoticeRequiresConsentAcrossRoutesAndLayouts(t *testing.T) {
	a := publicTestApp(t)
	ctx := context.Background()
	settings, err := a.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.ContactLinks = []ContactLink{
		{Kind: "x", URL: "https://x.com/ganwen_nl"},
		{Kind: "email", URL: "mailto:ganwen94@gmail.com"},
		{Kind: "website", URL: "https://example.com/contact?one=1&two=2"},
	}
	settings.ContactNoticeText = "请先说明来意。\n<script>alert(1)</script> & 请勿发送广告。😀"
	routes := []string{"/", "/?p=1447", "/?cat=13", "/?page_id=2", "/en/?p=1447"}
	for _, lang := range publicLanguages() {
		if lang != "zh" {
			routes = append(routes, "/"+lang+"/")
		}
	}
	for _, enabled := range []bool{false, true} {
		settings.ContactNoticeEnabled = enabled
		if err := a.Store.SaveSettings(ctx, settings); err != nil {
			t.Fatal(err)
		}
		mode := "disabled"
		if enabled {
			mode = "enabled"
		}
		for _, layout := range []string{"journal", "classic"} {
			for _, route := range routes {
				t.Run(mode+"/"+layout+route, func(t *testing.T) {
					w := publicWidgetRequest(a, route, layout)
					body := w.Body.String()
					if w.Code != 200 || !strings.Contains(body, `data-layout="`+layout+`"`) {
						t.Fatalf("unexpected response/layout: %d", w.Code)
					}
					doc, err := html.Parse(strings.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					var notice *html.Node
					dialogs, contacts := 0, 0
					var visit func(*html.Node)
					visit = func(n *html.Node) {
						if n.Type == html.ElementNode {
							if n.Data == "dialog" && widgetHasClass(n, "contact-notice-dialog") {
								dialogs++
							}
							if widgetAttribute(n, "id") == "contact-notice-text" {
								notice = n
							}
							if kind := widgetAttribute(n, "data-contact-kind"); kind != "" {
								contacts++
								var address string
								for _, contact := range settings.ContactLinks {
									if contact.Kind == kind {
										address = contact.URL
									}
								}
								if address == "" {
									t.Fatalf("unexpected contact kind %q", kind)
								}
								if enabled {
									if n.Data != "button" || widgetAttribute(n, "type") != "button" || widgetAttribute(n, "href") != "" || widgetAttribute(n, "data-contact-url") != address || widgetAttribute(n, "aria-haspopup") != "dialog" {
										t.Errorf("contact %s can bypass consent or lost its destination", kind)
									}
								} else if n.Data != "a" || widgetAttribute(n, "href") != address || widgetAttribute(n, "data-contact-url") != "" || widgetAttribute(n, "rel") != "noopener noreferrer" {
									t.Errorf("disabled contact %s changed direct navigation", kind)
								}
							}
							if enabled && n.Data == "a" {
								for _, contact := range settings.ContactLinks {
									if widgetAttribute(n, "href") == contact.URL {
										t.Error("direct contact href bypasses consent")
									}
								}
							}
						}
						for child := n.FirstChild; child != nil; child = child.NextSibling {
							visit(child)
						}
					}
					visit(doc)
					if contacts != len(settings.ContactLinks) {
						t.Fatalf("expected %d contacts, got %d", len(settings.ContactLinks), contacts)
					}
					if enabled {
						if dialogs != 1 || notice == nil || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") || strings.Contains(body, "<script>alert(1)</script>") {
							t.Fatal("missing dialog or unescaped warning text")
						}
						var text strings.Builder
						for child := notice.FirstChild; child != nil; child = child.NextSibling {
							if child.Type != html.TextNode {
								t.Fatal("contact notice contains executable/markup content")
							}
							text.WriteString(child.Data)
						}
						if text.String() != settings.ContactNoticeText {
							t.Fatalf("notice text or line breaks changed: %q", text.String())
						}
					} else if dialogs != 0 || notice != nil {
						t.Fatal("disabled notice still rendered a dialog")
					}
				})
			}
		}
	}
}
