package blog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

func TestAdminI18nMessagesCoverAllLanguagesAndParameters(t *testing.T) {
	raw, err := adminI18nFiles.ReadFile("web/admin/admin_messages.json")
	if err != nil {
		t.Fatal(err)
	}
	var messages map[string]map[string]string
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	params := regexp.MustCompile(`\{\d+\}`)
	for _, lang := range publicLanguages() {
		dictionary := messages[lang]
		if len(dictionary) != len(messages["zh"]) {
			t.Fatalf("%s dictionary has %d keys, want %d", lang, len(dictionary), len(messages["zh"]))
		}
		for key := range messages["zh"] {
			value := dictionary[key]
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s missing UI key %q", lang, key)
			}
			want, got := params.FindAllString(key, -1), params.FindAllString(value, -1)
			for _, p := range want {
				if !strings.Contains(value, p) {
					t.Fatalf("%s loses parameter %s in %q", lang, p, key)
				}
			}
			if len(want) != len(got) {
				t.Fatalf("%s changes parameter count in %q", lang, key)
			}
		}
	}
	if bytes.Contains(adminI18nJS, []byte("__UI_MESSAGES__")) {
		t.Fatal("unexpanded UI dictionary")
	}
}

func TestAdminStaticTextAndAttributesHaveExplicitDictionaryKeys(t *testing.T) {
	raw, _ := adminI18nFiles.ReadFile("web/admin/admin_messages.json")
	var messages map[string]map[string]string
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(bytes.NewReader(adminUI))
	if err != nil {
		t.Fatal(err)
	}
	var visit func(*html.Node, bool)
	visit = func(n *html.Node, marked bool) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for _, attr := range n.Attr {
			if strings.HasPrefix(attr.Key, "data-ui-") {
				if _, ok := messages["zh"][attr.Val]; !ok {
					t.Fatalf("missing explicit UI key %q", attr.Val)
				}
				marked = marked || attr.Key == "data-ui-key"
			}
		}
		if n.Type == html.TextNode && !marked && strings.IndexFunc(n.Data, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
			t.Fatalf("unmarked application text %q", n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c, marked)
		}
	}
	visit(doc, false)
}

func TestAdminUILanguageSelectionClient(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, node, filepath.Join("..", "..", "tests", "client", "test-admin-i18n-client.cjs")).CombinedOutput(); err != nil {
		t.Fatalf("UI language selection failed: %v\n%s", err, out)
	}
}

func TestAdminI18nIsStaticWithoutSessionOrPrivateData(t *testing.T) {
	// No configured app, authentication store or database is required for static UI text.
	a := &Admin{}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := httptest.NewRecorder()
		a.serve(w, httptest.NewRequest(method, "/admin/ui-i18n.js", nil))
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/javascript") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("static UI response: %d / %v", w.Code, w.Header())
		}
		if method == http.MethodGet && !bytes.Equal(w.Body.Bytes(), adminI18nJS) || method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatal("static UI body mismatch")
		}
	}
	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest(http.MethodPost, "/admin/ui-i18n.js", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal("static UI script accepted a mutation")
	}
}

func TestAdminInlineMessagesCannotTerminateScript(t *testing.T) {
	messages := []byte("{\"zh\":{\"example\":\"</script><script>globalThis.injected=true</script>&<>\u2028\u2029\"}}")
	expanded := expandAdminI18nScript([]byte("globalThis.messages=__UI_MESSAGES__;"), messages)
	for _, unsafe := range []string{"<", ">", "&", "\u2028", "\u2029"} {
		if bytes.Contains(expanded, []byte(unsafe)) {
			t.Fatalf("inline dictionary retains unsafe delimiter %q", unsafe)
		}
	}
	encoded := bytes.TrimSuffix(bytes.TrimPrefix(expanded, []byte("globalThis.messages=")), []byte(";"))
	var original, restored map[string]map[string]string
	if err := json.Unmarshal(messages, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &restored); err != nil || !reflect.DeepEqual(original, restored) {
		t.Fatal("inline escaping changes translated text")
	}
	page := adminHTMLWithI18n([]byte(`<html><head><script src="/admin/ui-i18n.js"></script></head></html>`), expanded)
	if bytes.Count(page, []byte("<script>")) != 1 || bytes.Count(page, []byte("</script>")) != 1 {
		t.Fatal("dictionary escaped its inline script element")
	}
}

func TestAdminInlineBootstrapWithoutExternalAsset(t *testing.T) {
	// This is the actual anonymous HTML response, not a separately installed
	// test dictionary. No app, authentication store or private database is used.
	w := httptest.NewRecorder()
	(&Admin{}).serve(w, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("admin HTML response lost its normal headers: %d / %v", w.Code, w.Header())
	}
	page := w.Body.Bytes()
	if !bytes.Equal(page, adminUIPage) || bytes.Contains(page, []byte(`src="/admin/ui-i18n.js"`)) || bytes.Contains(page, []byte("__UI_MESSAGES__")) {
		t.Fatal("admin HTML still depends on an external language bootstrap")
	}
	i18nAt, mainAt := bytes.Index(page, adminI18nJS), bytes.Index(page, []byte("const ui=window.GanwenUI;"))
	if i18nAt < 0 || i18nAt >= mainAt || bytes.Index(page, []byte(`<meta charset="utf-8">`)) > 1024 {
		t.Fatal("inline language bootstrap or early UTF-8 declaration is misplaced")
	}
	t.Logf("served_admin_ui_sha256=%x admin_i18n_sha256=%x", sha256.Sum256(page), sha256.Sum256(adminI18nJS))
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; real HTML bootstrap contracts still run")
	}
	dir := t.TempDir()
	pagePath, runnerPath := filepath.Join(dir, "admin.html"), filepath.Join(dir, "inline-startup.cjs")
	if err := os.WriteFile(pagePath, page, 0600); err != nil {
		t.Fatal(err)
	}
	const runner = `
'use strict';
const fs=require('node:fs'),assert=require('node:assert/strict'),vm=require('node:vm');
const html=fs.readFileSync(process.argv[2],'utf8');
assert.equal(/<script\b[^>]*\bsrc\s*=/i.test(html),false,'bootstrap must not request an external script');
const scripts=[...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(m=>m[1]);
assert.equal(scripts.length,3,'inline language, theme and application scripts are required');
const attributes=new Map([...html.matchAll(/<[^>]*\bid="([^"]+)"[^>]*>/g)].map(m=>[m[1],m[0]]));
class Node{
 constructor(markup=''){
  Object.assign(this,{value:'',textContent:'',checked:false,disabled:false,open:false,dataset:{},style:{},children:[],contentDocument:{documentElement:{dataset:{}},body:{innerHTML:''}}});
  const names=new Set((markup.match(/\bclass="([^"]*)"/)?.[1]||'').split(/\s+/).filter(Boolean));
  this.classList={add:(...items)=>items.forEach(x=>names.add(x)),remove:(...items)=>items.forEach(x=>names.delete(x)),contains:x=>names.has(x),toggle:(x,on)=>{on=on??!names.has(x);on?names.add(x):names.delete(x);return on}};
 }
 addEventListener(name,fn){this['on'+name]=fn}setAttribute(key,value){this[key]=value}removeAttribute(key){delete this[key]}append(...nodes){this.children.push(...nodes)}replaceChildren(...nodes){this.children=nodes}querySelector(){return null}close(){this.open=false}focus(){}
}
(async()=>{
 for(const lang of ['zh','nl','en','hi','es','ar','fr','bn','pt','id','ur']){
  const nodes=new Map(),get=id=>{if(!nodes.has(id))nodes.set(id,new Node(attributes.get(id)));return nodes.get(id)},calls=[];
  const document={cookie:'',documentElement:new Node(),getElementById:get,createElement:()=>new Node(),createElementNS:()=>new Node(),querySelector:()=>new Node(),querySelectorAll:()=>[],addEventListener(){}};
  document.documentElement.dataset={theme:'light',themeMode:'light'};
  const context={document,window:{parent:{postMessage(){}},confirm:()=>true,addEventListener(){}},location:{origin:'https://ganwen.test',href:'https://ganwen.test/admin/',search:''},history:{replaceState(){}},localStorage:{getItem:key=>key==='ganwen.admin-ui-language.v1'?lang:null,setItem(){}},matchMedia:()=>({matches:false,addEventListener(){}}),ResizeObserver:class{observe(){}},getComputedStyle:()=>({paddingLeft:'0px',paddingRight:'0px'}),setTimeout:()=>1,clearTimeout(){},FormData:class{},URL,URLSearchParams,TextEncoder,console,
   fetch:async(url)=>{calls.push(url);assert.equal(url,'/admin/api/session','all external assets and unexpected requests are unavailable');return {ok:true,status:200,json:async()=>({authenticated:false,configured:true,csrf:''})}}
  };
  Object.assign(context.window,{document,localStorage:context.localStorage,location:context.location,navigator:{languages:[lang]}});
  vm.createContext(context);for(const script of scripts)vm.runInContext(script,context);
  await vm.runInContext('startupPromise',context);
  assert.deepEqual(calls,['/admin/api/session']);assert.equal(context.window.GanwenUI.lang,lang);
  assert.equal(get('startup').classList.contains('hidden'),true);assert.equal(get('login').classList.contains('hidden'),false);assert.equal(get('loginButton').disabled,false);
  assert.equal(get('adminUILanguage').children.length,11);assert.equal(get('adminUILanguage').children[0].value,lang);
  assert.equal(document.documentElement.dir,['ar','ur'].includes(lang)?'rtl':'ltr');
 }
 console.log('11-language inline bootstrap reaches anonymous login without external assets');
})().catch(error=>{console.error(error);process.exitCode=1});
`
	if err := os.WriteFile(runnerPath, []byte(runner), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, node, runnerPath, pagePath).CombinedOutput(); err != nil {
		t.Fatalf("inline startup failed: %v\n%s", err, out)
	} else {
		t.Log(strings.TrimSpace(string(out)))
	}
}

func TestSelectedPublicLanguageOnlyReordersAvailableLinks(t *testing.T) {
	links := []publicLink{{Lang: "zh", URL: "/original"}, {Lang: "en", URL: "/en/translation"}}
	copyBefore := append([]publicLink(nil), links...)
	got := selectedPublicLanguageFirst(links, "en")
	if got[0].Lang != "en" || got[1].Lang != "zh" || !reflect.DeepEqual(links, copyBefore) {
		t.Fatal("selection changed links or canonical source order")
	}
	if got := selectedPublicLanguageFirst(links, "ar"); !reflect.DeepEqual(got, links) {
		t.Fatal("selection exposed an unavailable translation")
	}
}

func TestPublicErrorsFollowRouteLanguage(t *testing.T) {
	for _, lang := range publicLanguages() {
		for _, key := range []string{"页面不存在", "请求方法不支持", "服务暂时不可用", "RSS 已停用"} {
			if _, ok := publicMessage(lang, key); !ok {
				t.Fatalf("%s lacks public error %q", lang, key)
			}
		}
		w := httptest.NewRecorder()
		(&App{}).handlePublic(w, httptest.NewRequest(http.MethodPost, publicLanguagePrefix(lang)+"/", nil))
		want, _ := publicMessage(lang, "请求方法不支持")
		if w.Code != http.StatusMethodNotAllowed || strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("%s method error did not use route language", lang)
		}
		w = httptest.NewRecorder()
		publicNotFound(w, lang)
		want, _ = publicMessage(lang, "页面不存在")
		if w.Code != http.StatusNotFound || strings.TrimSpace(w.Body.String()) != "404 — "+want {
			t.Fatalf("%s not-found error did not use selected language", lang)
		}
	}
}
