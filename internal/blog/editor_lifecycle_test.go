package blog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFrontEditorSaveExpiryLogoutAndReopen(t *testing.T) {
	c := adminTestSetup(t, true)
	ctx := context.Background()
	p := c.newPost("publish")
	other := c.newPost("publish")
	canonical, archive := p.CanonicalURL, p.SourceHTML
	u, err := url.Parse(canonical)
	if err != nil {
		t.Fatal(err)
	}
	publicBody := func() string {
		t.Helper()
		response, err := c.client.Get(c.server.URL + u.RequestURI())
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("public article read: %d %v", response.StatusCode, err)
		}
		return string(raw)
	}
	readArticle := func() Post {
		t.Helper()
		code, raw := c.request(http.MethodGet, "post?id="+fmtID(p.ID), nil, "", "")
		var result struct {
			Post Post `json:"post"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &result) != nil {
			t.Fatalf("editor reopen: %d %s", code, raw)
		}
		return result.Post
	}
	save := func(input Post) Post {
		t.Helper()
		code, raw := c.request(http.MethodPost, "post", input, c.server.URL, c.csrf)
		var result struct {
			Post Post `json:"post"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &result) != nil {
			t.Fatalf("editor save: %d %s", code, raw)
		}
		if result.Post.Version != input.Version+1 || result.Post.CanonicalURL != canonical || result.Post.SourceHTML != archive {
			t.Fatal("save changed the version, canonical or original archive contract")
		}
		return result.Post
	}
	assertUnchanged := func(want Post, revision string) {
		t.Helper()
		stored, err := c.app.Store.GetPost(ctx, p.ID)
		current, revErr := c.app.Store.GetValue(ctx, postsRevisionKey)
		if err != nil || revErr != nil || !reflect.DeepEqual(stored, want) || current != revision {
			t.Fatal("rejected save changed the article or invalidated its revision")
		}
	}

	if !strings.Contains(publicBody(), p.Title) {
		t.Fatal("initial public article missing")
	}
	if code, _ := c.request(http.MethodGet, "post?id="+fmtID(p.ID), nil, "", ""); code != http.StatusUnauthorized {
		t.Fatal("anonymous reader entered the editing API")
	}
	c.login()
	input := readArticle()
	input.Title = "前台保存后的新标题"
	input.ContentHTML = `<p>保存后的正文 <strong>仍可继续编辑</strong></p><img src="/wp-content/uploads/old.jpg" alt="保留媒体">`
	saved := save(input)
	if !reflect.DeepEqual(readArticle(), saved) || !strings.Contains(publicBody(), input.Title) || !strings.Contains(publicBody(), "保存后的正文") {
		t.Fatal("reopened editor or warm public reader did not see the saved content")
	}
	revision, err := c.app.Store.GetValue(ctx, postsRevisionKey)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := c.request(http.MethodPost, "post", input, c.server.URL, c.csrf); code != http.StatusConflict {
		t.Fatal("duplicate stale save did not reject its old version")
	}
	assertUnchanged(saved, revision)
	invalid := saved
	invalid.Title = ""
	if code, _ := c.request(http.MethodPost, "post", invalid, c.server.URL, c.csrf); code != http.StatusBadRequest {
		t.Fatal("invalid save was not rejected")
	}
	assertUnchanged(saved, revision)

	for key, session := range storedTestAdminSessions(t, c.app.Store) {
		session.ExpiresAt = time.Now().Add(-time.Second).Unix()
		raw, _ := json.Marshal(session)
		if err := c.app.Store.SetValue(ctx, key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	draft := saved
	draft.Title = "重新登录后保留的草稿"
	draft.ContentHTML = "<p>失效期间继续保留的修改</p>"
	if code, _ := c.request(http.MethodPost, "post", draft, c.server.URL, c.csrf); code != http.StatusUnauthorized {
		t.Fatal("expired editor session accepted a write")
	}
	assertUnchanged(saved, revision)
	code, raw := c.request(http.MethodGet, "session", nil, "", "")
	var session struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &session) != nil || session.Authenticated || session.CSRF == "" {
		t.Fatal("expired session did not provide a real new login challenge")
	}
	c.csrf = session.CSRF
	c.login()
	saved = save(draft)
	if !reflect.DeepEqual(readArticle(), saved) || !strings.Contains(publicBody(), draft.Title) {
		t.Fatal("reauthenticated save or subsequent editor read lost the retained content")
	}
	if code, _ := c.request(http.MethodPost, "logout", map[string]any{}, c.server.URL, c.csrf); code != http.StatusOK {
		t.Fatal("logout failed")
	}
	if code, _ := c.request(http.MethodGet, "post?id="+fmtID(p.ID), nil, "", ""); code != http.StatusUnauthorized {
		t.Fatal("logout retained article editing authority")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.client.Jar = jar
	code, raw = c.request(http.MethodGet, "session", nil, "", "")
	if code != http.StatusOK || json.Unmarshal(raw, &session) != nil || session.Authenticated {
		t.Fatal("new browser did not start anonymous")
	}
	c.csrf = session.CSRF
	c.login()
	if !reflect.DeepEqual(readArticle(), saved) {
		t.Fatal("fresh browser login reopened stale content")
	}
	unchanged, err := c.app.Store.GetPost(ctx, other.ID)
	if err != nil || !reflect.DeepEqual(unchanged, other) {
		t.Fatal("editing one article changed another article")
	}
}

func TestEditorPublicLanguagesFollowActualRoutes(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	ctx := context.Background()
	p := c.newPost("publish")
	p.Translations = make(map[string]Translation)
	for _, language := range translationLanguages() {
		p.Translations[language] = Translation{Title: "Translated " + language, ContentHTML: "<p>Translated body</p>", Status: "publish", SourceHash: SourceHash(p)}
	}
	if err := c.app.Store.SavePost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	decode := func(code int, raw []byte, expected []string) Post {
		t.Helper()
		var result struct {
			Post      Post     `json:"post"`
			Languages []string `json:"public_languages"`
		}
		if code != http.StatusOK || json.Unmarshal(raw, &result) != nil || !reflect.DeepEqual(result.Languages, expected) {
			t.Fatalf("public language contract: %d %s", code, raw)
		}
		for _, language := range publicLanguages() {
			path := publicLanguagePrefix(language) + "/?p=" + fmtID(p.ID)
			response, err := c.client.Get(c.server.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			visible := false
			for _, current := range expected {
				visible = visible || current == language
			}
			want := http.StatusNotFound
			if visible {
				want = http.StatusOK
			}
			if response.StatusCode != want {
				t.Fatalf("%s public route status %d, want %d", language, response.StatusCode, want)
			}
		}
		return result.Post
	}
	code, raw := c.request(http.MethodGet, "post?id="+fmtID(p.ID), nil, "", "")
	p = decode(code, raw, publicLanguages())
	p.Title = "原文已更新，旧译文应撤下"
	code, raw = c.request(http.MethodPost, "post", p, c.server.URL, c.csrf)
	p = decode(code, raw, []string{"zh"})
	translation := Translation{Title: "Reviewed English", ContentHTML: "<p>Reviewed current content</p>", Status: "publish", SourceHash: SourceHash(p)}
	code, raw = c.request(http.MethodPost, "translation", map[string]any{"id": p.ID, "version": p.Version, "language": "en", "translation": translation}, c.server.URL, c.csrf)
	p = decode(code, raw, []string{"zh", "en"})
	p.Status = "private"
	code, raw = c.request(http.MethodPost, "post", p, c.server.URL, c.csrf)
	decode(code, raw, []string{})
}
