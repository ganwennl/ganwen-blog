package blog

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestAdminUIElementContracts(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(string(adminUI)))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		for _, attr := range n.Attr {
			if attr.Key == "id" {
				if ids[attr.Val] {
					t.Errorf("duplicate UI id %q", attr.Val)
				}
				ids[attr.Val] = true
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	for _, match := range regexp.MustCompile(`\$\('([A-Za-z][\w-]*)'\)`).FindAllStringSubmatch(string(adminUI), -1) {
		if !ids[match[1]] {
			t.Errorf("JavaScript references missing UI id %q", match[1])
		}
	}
	for _, id := range []string{"view-posts", "view-pages", "view-analytics", "aboutPageShortcut", "editAboutButton", "newPageButton", "editorLoadError", "editorGrid", "analyticsTrend", "analyticsBreakdowns", "analyticsDailyRows", "analyticsRegionRows", "analyticsNetworkRows", "analyticsGeoDatabase", "updateGeoDatabase", "geoDatabaseStatus", "geoDatabaseDataVersion", "geoDatabaseError", "analyticsGeoDatabaseV6", "updateGeoDatabaseV6", "geoDatabaseV6Status", "geoDatabaseV6Version", "geoDatabaseV6DataVersion", "geoDatabaseV6Updated", "geoDatabaseV6Records", "geoDatabaseV6Size", "geoDatabaseV6Source", "geoDatabaseV6Error", "geoDatabaseV6Note"} {
		if !ids[id] {
			t.Errorf("required UI element %q is absent", id)
		}
	}
	for _, token := range []string{`data-lang="zh"`, `data-lang="en"`, `data-lang="nl"`, `data-lang="es"`, `id="visualEditor"`, `id="sourceEditor"`, `id="insertImage"`, `id="insertVideo"`, `UV · 每日匿名会话累计`, `每日明细`, `.embed-mode .sidebar,.embed-mode .theme-control{display:none!important}`, `ZX IP地址数据库`, `更新只检查该仓库新提交中的地址库`} {
		if !strings.Contains(string(adminUI), token) {
			t.Errorf("required editor/statistics contract %q is absent", token)
		}
	}
	if strings.Contains(string(adminUI), `postMessage({type,id},'*')`) || strings.Contains(string(adminUI), `grid-template-columns:repeat(3,minmax(0,1fr)))`) {
		t.Fatal("unsafe message target or malformed responsive grid")
	}
}

func TestAdminUIJavaScriptSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; HTML contracts still run")
	}
	for _, match := range regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(string(adminUI), -1) {
		path := filepath.Join(t.TempDir(), "ui.js")
		if err := os.WriteFile(path, []byte(match[1]), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := exec.CommandContext(ctx, node, "--check", path).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("JavaScript syntax failed: %v\n%s", err, out)
		}
	}
}

func TestAdminUISessionRecovery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; HTML contracts still run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, filepath.Join("..", "..", "tests", "client", "test-admin-session-client.cjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("admin session recovery failed: %v\n%s", err, out)
	}
}

func TestAdminUIPastedMedia(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; HTML contracts still run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, filepath.Join("..", "..", "tests", "client", "test-admin-media-client.cjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("pasted media recovery failed: %v\n%s", err, out)
	}
}

// These tests execute the shipped JavaScript with a small DOM contract double.
// No browser, server, production database, or translation service is started.
func TestAdminUIFilteringAnalyticsAndEmbeddedEditor(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; HTML contracts still run")
	}
	scripts := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(string(adminUI), -1)
	if len(scripts) != 2 {
		t.Fatalf("expected theme and application scripts, got %d", len(scripts))
	}
	source := strings.TrimSuffix(strings.TrimSpace(scripts[1][1]), "start();")
	encoded, _ := json.Marshal(source)
	runner := `const assert=require('node:assert/strict'),vm=require('node:vm');
class FakeNode{
 constructor(tag='div'){if(tag==='template')this.content=new FakeNode('fragment');this.tagName=tag;this.value='';this.textContent='';this.children=[];this.dataset={};this.style={};this.disabled=false;this.checked=false;this.open=false;const names=new Set();this.classList={add:(...v)=>v.forEach(x=>names.add(x)),remove:(...v)=>v.forEach(x=>names.delete(x)),contains:v=>names.has(v),toggle:(v,on)=>{const next=on===undefined?!names.has(v):on;if(next)names.add(v);else names.delete(v);return next}}}
 append(...nodes){this.children.push(...nodes)}replaceChildren(...nodes){this.children=[...nodes]}setAttribute(k,v){this[k]=String(v)}removeAttribute(k){delete this[k]}addEventListener(){}querySelector(){return null}querySelectorAll(){return []}focus(){}click(){if(!this.disabled&&this.onclick)return this.onclick()}showModal(){this.open=true}close(){this.open=false}
}
const nodes=new Map(),get=id=>{if(!nodes.has(id))nodes.set(id,new FakeNode());return nodes.get(id)},events={},routes=[],messages=[],timers=new Map(),resizeObservers=[];let nextTimer=0;
const navs=['posts','pages','analytics','media','translation','settings'].map(view=>{const n=new FakeNode('button');n.dataset.view=view;return n}),sections=['posts','pages','analytics','editor','media','translation','settings'].map(view=>{const n=new FakeNode('section');n.id='view-'+view;return n}),langs=['zh','en','nl','es'].map(lang=>{const n=new FakeNode('button');n.dataset.lang=lang;return n}),ranges=['7','30','90','custom'].map(days=>{const n=new FakeNode('button');n.dataset.analyticsDays=days;return n});
const document={documentElement:{dataset:{theme:'light',themeMode:'light'}},getElementById:get,createElement:tag=>new FakeNode(tag),createElementNS:(_,tag)=>new FakeNode(tag),querySelector:()=>new FakeNode(),querySelectorAll:selector=>selector==='.nav'?navs:selector==='main>section'?sections:selector==='[data-lang]'?langs:selector==='[data-analytics-days]'?ranges:[],addEventListener:(name,handler)=>events['document:'+name]=handler};
const parent={postMessage:(payload,origin)=>messages.push({payload,origin})};
const window={parent,confirm:()=>true,prompt:()=>null,addEventListener:(name,handler)=>events[name]=handler};
const context={assert,document,window,parent,messages,routes,timers,resizeObservers,events,URL,URLSearchParams,location:{origin:'https://ganwen.nl',href:'https://ganwen.nl/admin/?embed=1&edit=2&editor_session=fixture-instance',search:'?embed=1&edit=2&editor_session=fixture-instance'},history:{replaceState:(_,__,route)=>routes.push(route)},matchMedia:()=>({matches:false,addEventListener(){}}),localStorage:{setItem(){}},FormData:class{},ResizeObserver:class{constructor(callback){this.callback=callback;resizeObservers.push(this)}observe(target){this.target=target}},getComputedStyle:target=>target.computedStyle||{paddingLeft:'0px',paddingRight:'0px'},setTimeout:callback=>{const id=++nextTimer;timers.set(id,callback);return id},clearTimeout:id=>timers.delete(id),console,fetch:async()=>{throw Error('Unexpected network request')}};
vm.createContext(context);
window.document=document;window.localStorage=context.localStorage;window.location=context.location;window.navigator={languages:["zh-CN"]};
vm.runInContext(UI_LOCALE_SCRIPT,context);
vm.runInContext(SOURCE,context);
vm.runInContext(TESTS,context).catch(error=>{console.error(error);process.exitCode=1});
`
	tests := `
(async()=>{
 assert.equal(messages.length,1);assert.equal(messages[0].payload.type,'ganwen-editor-ready');assert.equal(messages[0].payload.id,2);assert.equal(messages[0].payload.session,'fixture-instance');assert.equal(messages[0].origin,location.origin);messages.length=0;
 const posts=[{id:1,type:'post',title:'旅行日志',status:'publish',categories:[{name:'旅行'}],tags:[]},{id:3,type:'post',title:'旅行草稿',status:'draft',categories:[],tags:[]},{id:4,type:'post',title:'私人旅行',status:'private',categories:[],tags:[]},{id:2,type:'page',title:'關於甘文',status:'publish',categories:[],tags:[]},{id:5,type:'post',title:'待审',status:'pending',categories:[],tags:[]},{id:6,type:'attachment',title:'旅行照片',status:'publish'}];
 state.posts=posts;renderPosts();
 assert.equal($('navPostsCount').textContent,'4');assert.equal($('navPagesCount').textContent,'1');assert.equal($('aboutPageTitle').textContent,'關於甘文');assert.equal($('postRows').children.length,4);assert.equal($('pageRows').children.length,1);
 $('postSearch').value='旅行';$('postSearch').oninput();assert.equal($('postRows').children.length,4,'search is deferred');for(const [id,callback] of timers){timers.delete(id);callback()}assert.equal($('postRows').children.length,3);
 const published=$('postStats').children.find(button=>button.dataset.status==='publish');published.click();assert.equal($('statusFilter').value,'publish');assert.equal($('postRows').children.length,1);assert.equal($('postStats').children.find(button=>button.dataset.status==='publish').children[0].textContent,'1');assert.ok($('postStats').children.find(button=>button.dataset.status==='publish').className.split(' ').includes('active'));
 $('clearPostFilters').click();assert.equal($('postSearch').value,'');assert.equal($('statusFilter').value,'');assert.equal($('postRows').children.length,4);assert.equal($('pageRows').children.length,1);
 assert.equal(filterDocuments(posts,'post','旅行','draft')[0].id,3);assert.equal(filterDocuments(posts,'page','旅行').length,0);assert.equal(documentCounts(posts,'post','旅行')['private'],1);
 const manyPosts=Array.from({length:123},(_,i)=>({id:i+100,type:'post',title:'Article '+i,status:i%2?'draft':'publish',categories:[],tags:[]}));state.posts=manyPosts;renderPosts();assert.equal($('postRows').children.length,50);assert.equal($('postPagination').children[0].disabled,true);assert.equal($('postPagination').children[1].textContent,'第 1 / 3 页');const visited=new Set();for(let page=0;page<3;page++){for(const row of $('postRows').children)visited.add(row.children[0].children[0].textContent);if(page<2)$('postPagination').children[2].click()}assert.equal(visited.size,123,'all articles remain reachable');assert.equal($('postRows').children.length,23);assert.equal($('postPagination').children[2].disabled,true);$('postSearch').value='Article 122';$('postSearch').oninput();$('postSearch').oninput();assert.equal(timers.size,1,'search bursts coalesce');for(const [id,callback] of timers){timers.delete(id);callback()}assert.equal($('postRows').children.length,1);assert.equal($('postRows').children[0].children[0].children[0].textContent,'Article 122','search covers later pages');assert.equal($('postPagination').children.length,0);$('clearPostFilters').click();$('statusFilter').value='draft';$('statusFilter').onchange();assert.equal($('postPagination').children[1].textContent,'第 1 / 2 页');assert.equal($('postStats').children.find(button=>button.dataset.status==='draft').children[0].textContent,'61');$('clearPostFilters').click();state.posts=posts;renderPosts();
 // Searches must consume an old pager click before applying a page delta.
 const mixedDocuments=['post','page'].flatMap((type,index)=>Array.from({length:151},(_,i)=>({id:index*1000+i+1,type,title:type+' '+(i<100?'Group ':'Other ')+i,status:i%2?'draft':'publish',categories:[],tags:[]})));
 state.posts=mixedDocuments;renderPosts();
 const flushDocumentSearch=()=>{for(const [id,callback] of [...timers]){timers.delete(id);callback()}};
 for(const type of ['post','page']){
  const page=type==='page',prefix=page?'page':'post',search=$(prefix+'Search'),filter=$(page?'pageStatusFilter':'statusFilter'),clear=$(page?'clearPageFilters':'clearPostFilters'),pager=$(prefix+'Pagination'),rows=$(prefix+'Rows'),other=page?'post':'page';
  clear.click();$(other+'Pagination').children[2].click();const otherPage=documentPages[other];
  pager.children[2].click();assert.equal(documentPages[type],2);pager.children[0].click();assert.equal(documentPages[type],1);
  pager.children[2].click();pager.children[2].click();assert.equal(documentPages[type],3);
  search.value=type+' G';search.oninput();search.value=type+' Group';search.oninput();assert.equal(timers.size,1);
  pager.children[0].click();assert.equal(pager.children[1].textContent,'第 1 / 2 页','old previous click must keep a pending search on its first page');assert.equal(rows.children[0].children[0].children[0].textContent,type+' Group 0');assert.equal(timers.size,0);flushDocumentSearch();assert.equal(search.value,type+' Group');
  pager.children[2].click();assert.equal(pager.children[1].textContent,'第 2 / 2 页');assert.equal(rows.children[0].children[0].children[0].textContent,type+' Group 50');pager.children[0].click();assert.equal(documentPages[type],1);
  clear.click();pager.children[2].click();search.value=type+' Group';search.oninput();pager.children[2].click();assert.equal(pager.children[1].textContent,'第 1 / 2 页','old next click must first apply the pending search');assert.equal(timers.size,0);assert.equal(rows.children[0].children[0].children[0].textContent,type+' Group 0');
  clear.click();pager.children[2].click();search.value='no matching document';search.oninput();pager.children[2].click();assert.equal(rows.children.length,1);assert.equal(rows.children[0].children[0].className,'empty');assert.equal(pager.children.length,0);assert.equal(timers.size,0);
  clear.click();pager.children[2].click();pager.children[2].click();search.value=type+' Other 150';search.oninput();pager.children[0].click();assert.equal(rows.children.length,1);assert.equal(rows.children[0].children[0].children[0].textContent,type+' Other 150');assert.equal(pager.children.length,0);assert.equal(documentPages[type],1);assert.equal(timers.size,0);
  clear.click();pager.children[2].click();search.value=type+' Group';search.oninput();filter.value='draft';filter.onchange();assert.equal(rows.children.length,50);assert.equal(pager.children.length,0);assert.equal(timers.size,0);assert.equal(search.value,type+' Group');
  clear.click();search.value=type+' Group';search.oninput();clear.click();flushDocumentSearch();assert.equal(search.value,'');assert.equal(filter.value,'');assert.equal(rows.children.length,50);assert.equal(pager.children[1].textContent,'第 1 / 4 页');assert.equal(documentPages[other],otherPage,'search and pager state must stay scoped to one document type');
 }
 $('postSearch').value='post Group';$('postSearch').oninput();$('pageSearch').value='page Other 150';$('pageSearch').oninput();assert.equal(timers.size,2);renderDocumentList('post');assert.equal(timers.size,1,'rendering posts must retain the pending page search');flushDocumentSearch();assert.equal($('pageRows').children[0].children[0].children[0].textContent,'page Other 150');assert.equal(timers.size,0);$('clearPostFilters').click();$('clearPageFilters').click();state.posts=posts;renderPosts();
 assert.equal(validEditID('2'),2);for(const input of ['2x','0','-1','1.5','9007199254740993'])assert.equal(validEditID(input),0);
 assert.equal(analyticsQuery(7),'analytics?days=7');assert.equal(analyticsQuery('custom','2026-10-01','2026-10-08'),'analytics?start=2026-10-01&end=2026-10-08');assert.throws(()=>analyticsQuery('custom','2026-02-30','2026-10-08'));assert.throws(()=>analyticsQuery('custom','2026-10-09','2026-10-08'));assert.throws(()=>analyticsQuery(14));
 const emptyGeo={ready:false,version:'',updated_at:'',records:0,bytes:0,source:'https://github.com/metowolf/qqwry.dat',ipv4_only:true},emptyGeoV6={...emptyGeo,source:'https://github.com/itbdw/ip-database',ipv4_only:false},empty={from:'2026-10-01',to:'2026-10-08',timezone:'UTC',pv:0,uv_daily_sum:0,trend:[],top_pages:[],sources:[],countries:[],languages:[],devices:[],browsers:[],os:[],regions:[],networks:[],geo_database:emptyGeo,geo_database_v6:emptyGeoV6};assert.equal(validateAnalytics(empty),empty);assert.throws(()=>validateAnalytics({...empty,uv_daily_sum:undefined}));assert.throws(()=>validateAnalytics({...empty,sources:null}));assert.throws(()=>validateAnalytics({...empty,regions:null}));assert.throws(()=>validateAnalytics({...empty,geo_database:null}));assert.throws(()=>validateAnalytics({...empty,geo_database_v6:null}));assert.throws(()=>validateAnalytics({...empty,geo_database_v6:emptyGeo}));
 assert.equal(safeAnalyticsPath('/?page_id=2'),'/?page_id=2');for(const path of ['https://evil.invalid','//evil.invalid','/\\evil','javascript:alert(1)'])assert.equal(safeAnalyticsPath(path),'');
 const dimension=analyticsDimensionRows([{label:'direct',pv:3},{label:'direct',pv:2},{label:'unknown',pv:0}]);assert.equal(dimension.length,1);assert.equal(dimension[0].pv,5);
 renderAnalytics(empty);assert.equal($('analyticsPV').textContent,'0');assert.equal($('analyticsUV').textContent,'0');assert.equal($('analyticsTrend').children[0].textContent,'此期间暂无访问数据。');assert.equal($('analyticsBreakdowns').children.length,8);assert.equal($('analyticsTopPages').children[0].children[0].textContent,'此期间暂无页面访问。');assert.equal($('geoDatabaseStatus').textContent,'未安装');assert.equal($('geoDatabaseV6Status').textContent,'未安装');assert.equal($('geoDatabaseV6Source').children[1].textContent,'ZX IP地址数据库');assert.equal($('geoDatabaseV6Source').children[1].href,'https://github.com/itbdw/ip-database');assert.ok($('geoDatabaseV6Note').textContent.includes('新提交'));assert.ok(!$('geoDatabaseNote').textContent.includes('IPv6 国家由 Cloudflare'));assert.equal($('analyticsRegionRows').children[0].children[0].textContent,'此期间暂无地区数据。');assert.equal($('analyticsNetworkRows').children[0].children[0].textContent,'此期间暂无运营商数据。');
 const geo={...emptyGeo,ready:true,version:'2026-10-08',updated_at:'2026-10-08T12:34:56Z',records:120,bytes:1024},real={...empty,pv:12,uv_daily_sum:6,trend:[{date:'2026-10-01',pv:8,uv:3},{date:'2026-10-02',pv:4,uv:3}],top_pages:[{path:'/?page_id=2',title:'关于甘文',post_id:2,pv:12}],sources:[{label:'direct',pv:12}],countries:[{label:'NL',pv:12}],languages:[{label:'zh',pv:12}],devices:[{label:'desktop',pv:12}],browsers:[{label:'Firefox',pv:12}],os:[{label:'Windows',pv:12}],regions:[{label:'广东省深圳市南山区',pv:8},{label:'unknown',pv:4}],networks:[{label:'中国电信 / 企业网络',pv:8},{label:'en',pv:4}],geo_database:geo};renderAnalytics(real);assert.equal($('analyticsPV').textContent,'12');assert.equal($('analyticsUV').textContent,'6');assert.equal($('analyticsTrend').children[0].tagName,'svg');assert.equal($('analyticsDailyRows').children[0].children[0].textContent,'2026-10-02');assert.equal($('analyticsTopPages').children[0].children[1].children[0].href,'/?page_id=2');assert.equal($('analyticsRegionRows').children[0].children[0].textContent,'广东省深圳市南山区');assert.equal($('analyticsRegionRows').children[0].children[2].textContent,'66.7%');assert.equal($('analyticsNetworkRows').children[1].children[0].textContent,'en');assert.equal($('analyticsNetworkRows').children[1].children[2].textContent,'33.3%');assert.equal(countryLabel('NL'),'荷兰');assert.equal(countryLabel('unknown'),'未知');assert.equal(countryLabel('not-a-country'),'not-a-country');assert.equal(dimensionLabel('US','countries'),'美国');assert.equal($('geoDatabaseStatus').textContent,'已就绪');assert.equal($('geoDatabaseVersion').textContent,'文件版本：2026-10-08');assert.equal($('geoDatabaseSize').textContent,'大小：1.0 KiB');assert.ok($('geoDatabaseUpdated').textContent.endsWith('UTC'));assert.equal($('geoDatabaseSource').children[1].href,'https://github.com/metowolf/qqwry.dat');assert.ok($('geoDatabaseNote').textContent.includes('IPv6'));const allRegions=Array.from({length:12},(_,i)=>({label:'完整地区 '+i,pv:1}));assert.equal(analyticsDimensionRows(allRegions,Infinity).length,12);renderGeoDatabase({...geo,source:'javascript:alert(1)',error:'库校验失败'});assert.equal($('geoDatabaseSource').children[1].tagName,'span');assert.equal($('geoDatabaseError').textContent,'库校验失败');renderGeoDatabase(geo);
 assert.equal($('geoDatabaseDataVersion').textContent,'库内版本：—');assert.equal($('geoDatabaseV6DataVersion').textContent,'库内版本：—');renderGeoDatabase({...geo,data_version:'20260930'});assert.equal($('geoDatabaseDataVersion').textContent,'库内版本：20260930');assert.equal($('geoDatabaseVersion').textContent,'文件版本：2026-10-08');renderGeoDatabase(geo);
 const chart=$('analyticsTrend'),responsiveTrend=Array.from({length:90},(_,i)=>({date:new Date(Date.UTC(2026,7,1+i)).toISOString().slice(0,10),pv:i%8,uv:i%4}));assert.equal(chart.children[0].viewBox,'0 0 1000 280');chart.clientWidth=335;chart.computedStyle={paddingLeft:'12.5px',paddingRight:'12.5px'};renderAnalyticsTrend(responsiveTrend);assert.equal(chart.children[0].viewBox,'0 0 310 240');let dateTicks=chart.children[0].children.filter(child=>child['data-chart-date']);assert.equal(dateTicks.length,4);assert.equal(dateTicks[0]['data-chart-date'],responsiveTrend[0].date);assert.equal(dateTicks.at(-1)['data-chart-date'],responsiveTrend.at(-1).date);assert.ok(chart.children[0].children.filter(child=>child.tagName==='text').every(child=>Number(child['font-size'])>=11));
 chart.clientWidth=1225;renderAnalyticsTrend(responsiveTrend);assert.equal(chart.children[0].viewBox,'0 0 1200 280');dateTicks=chart.children[0].children.filter(child=>child['data-chart-date']);assert.ok(dateTicks.length>=7&&dateTicks.length<=10);
 state.analytics={...real,trend:responsiveTrend};state.authenticated=true;state.view='analytics';chart.clientWidth=3525;assert.equal(resizeObservers.length,1);assert.equal(resizeObservers[0].target,chart);resizeObservers[0].callback();events.resize();events.resize();assert.equal(timers.size,1);const resizeCallback=[...timers.values()][0];timers.clear();resizeCallback();assert.equal(chart.children[0].viewBox,'0 0 3500 320');assert.equal(chart.children[0].children.filter(child=>child['data-chart-date']).length,10);const stableChart=chart.children[0];redrawAnalyticsTrend();assert.equal(chart.children[0],stableChart);chart.clientWidth=0;redrawAnalyticsTrend();assert.equal(chart.children[0],stableChart);chart.clientWidth=335;redrawAnalyticsTrend();assert.equal(chart.children[0].viewBox,'0 0 310 240');state.view='posts';
 let updates=0,reportLoads=0,releaseUpdate;const updatedGeo={...geo,version:'2026-10-09'};api=async(path,body)=>{if(path==='analytics/geoip/update'){updates++;assert.equal(Object.keys(body).length,0);return new Promise(resolve=>releaseUpdate=resolve)}if(path.startsWith('analytics?')){reportLoads++;return {...real,geo_database:updatedGeo}}throw Error('Unexpected path '+path)};state.authenticated=false;await updateGeoDatabase();assert.equal(updates,0);state.authenticated=true;state.view='analytics';const pendingUpdate=updateGeoDatabase();await updateGeoDatabase();assert.equal(updates,1);assert.equal($('updateGeoDatabase').disabled,true);assert.equal($('updateGeoDatabase').textContent,'更新中…');releaseUpdate({geo_database:updatedGeo});await pendingUpdate;assert.equal(reportLoads,1);assert.equal($('updateGeoDatabase').disabled,false);assert.equal(state.geoUpdating,false);assert.equal($('geoDatabaseVersion').textContent,'文件版本：2026-10-09');api=async()=>{throw Error('更新失败，旧库继续使用')};await updateGeoDatabase();assert.equal($('geoDatabaseError').textContent,'更新失败，旧库继续使用');assert.equal($('geoDatabaseVersion').textContent,'文件版本：2026-10-09');assert.equal($('geoDatabaseStatus').textContent,'已就绪');assert.equal(state.geoUpdating,false);state.view='posts';
 let v6Updates=0,releaseV6;const updatedGeoV6={...emptyGeoV6,ready:true,version:'git-40bd5c0e5ead',data_version:'20200506',records:321,bytes:2048,updated_at:'2026-10-08T16:00:00Z'};api=async(path,body)=>{if(path==='analytics/geoip/update-v6'){v6Updates++;assert.equal(Object.keys(body).length,0);return new Promise(resolve=>releaseV6=resolve)}if(path.startsWith('analytics?'))return {...real,geo_database:updatedGeo,geo_database_v6:updatedGeoV6};throw Error('Unexpected path '+path)};state.authenticated=false;await updateGeoDatabase(true);assert.equal(v6Updates,0);state.authenticated=true;state.geoUpdating=true;state.view='analytics';const pendingV6=$('updateGeoDatabaseV6').click();await $('updateGeoDatabaseV6').click();assert.equal(v6Updates,1);assert.equal(state.geoUpdatingV6,true);assert.equal(state.geoUpdating,true);assert.equal($('updateGeoDatabaseV6').disabled,true);releaseV6({geo_database_v6:updatedGeoV6});await pendingV6;assert.equal(state.geoUpdatingV6,false);assert.equal(state.geoUpdating,true);assert.equal($('updateGeoDatabaseV6').disabled,false);assert.equal($('updateGeoDatabaseV6').textContent,'更新 IPv6 库');assert.equal($('geoDatabaseV6Status').textContent,'已就绪');assert.equal($('geoDatabaseV6Version').textContent,'文件版本：git-40bd5c0e5ead');assert.equal($('geoDatabaseV6Records').textContent,'索引：321');assert.equal($('geoDatabaseV6Size').textContent,'大小：2.0 KiB');assert.equal($('geoDatabaseVersion').textContent,'文件版本：2026-10-09');state.geoUpdating=false;const v4Error=$('geoDatabaseError').textContent;api=async()=>{throw Error('IPv6 更新失败，旧库继续使用')};await updateGeoDatabase(true);assert.equal($('geoDatabaseV6Error').textContent,'IPv6 更新失败，旧库继续使用');assert.equal($('geoDatabaseV6Version').textContent,'文件版本：git-40bd5c0e5ead');assert.equal($('geoDatabaseError').textContent,v4Error);assert.equal(state.geoUpdatingV6,false);state.view='posts';
 assert.equal($('geoDatabaseV6DataVersion').textContent,'库内版本：20200506');assert.equal($('geoDatabaseV6Version').textContent,'文件版本：git-40bd5c0e5ead');assert.ok($('geoDatabaseV6Updated').textContent.startsWith('安装/更新时间：'));assert.ok($('geoDatabaseV6Updated').textContent.includes('2026'));assert.ok($('geoDatabaseV6Updated').textContent.endsWith('UTC'));
 state.authenticated=true;state.post={id:2,type:'page',version:3,title:'關於甘文',canonical_url:'https://www.ganwen.nl/?page_id=2',content_html:'<p>原文</p>',status:'publish',translations:{}};state.returnView='pages';state.hash='hash';showEditor();assert.equal($('editorHeading').textContent,'编辑页面 #2');assert.equal($('savePostButton').textContent,'保存页面');assert.equal($('backPosts').textContent,'取消');assert.equal(state.raw,'<p>原文</p>');
 let closed=0;const originalClose=closeEditor;closeEditor=()=>{closed++};handleEditorCloseRequest({origin:'https://evil.invalid',source:parent,data:{type:'ganwen-editor-close-request',id:2,session:'fixture-instance'}});handleEditorCloseRequest({origin:location.origin,source:{},data:{type:'ganwen-editor-close-request',id:2,session:'fixture-instance'}});handleEditorCloseRequest({origin:location.origin,source:parent,data:{type:'ganwen-editor-close-request',id:3,session:'fixture-instance'}});handleEditorCloseRequest({origin:location.origin,source:parent,data:{type:'ganwen-editor-close-request',id:2,session:'previous-instance'}});assert.equal(closed,0);handleEditorCloseRequest({origin:location.origin,source:parent,data:{type:'ganwen-editor-close-request',id:2,session:'fixture-instance'}});assert.equal(closed,1);closeEditor=originalClose;
 state.dirty=true;$('postTitle').value='尚未保存的页面标题';window.confirm=()=>false;assert.equal(closeEditor(),false);assert.equal(messages.length,0);window.confirm=()=>true;assert.equal(closeEditor(),true);assert.equal(messages[0].payload.type,'ganwen-editor-close');assert.equal(messages[0].payload.id,2);assert.equal(messages[0].payload.session,'fixture-instance');assert.equal(messages[0].origin,location.origin);messages.length=0;
 state.dirty=true;state.lang='zh';$('postTitle').value='修改标题';let received;api=async(path,body)=>{received={path,body};throw Error('save rejected')};await saveEditor();assert.equal(received.body.type,'page');assert.equal(received.body.canonical_url,'https://www.ganwen.nl/?page_id=2');assert.equal(state.post.title,'關於甘文');assert.equal(state.dirty,true);assert.equal(state.saving,false);assert.equal(messages.length,0);
 api=async(path,body)=>({post:{...body,version:4},source_hash:'newhash'});await saveEditor();assert.equal(state.post.title,'修改标题');assert.equal(state.dirty,false);assert.equal(messages.length,1);assert.equal(messages[0].payload.type,'ganwen-editor-saved');assert.equal(messages[0].origin,location.origin);messages.length=0;
 state.authenticated=false;notifyEditorParent('ganwen-editor-saved');assert.equal(messages.length,0);const previous=state.post;await openPost(2);assert.equal(state.post,previous);assert.equal(messages.length,0);
 state.authenticated=true;state.embed=false;newDocument('page');assert.equal(state.post.type,'page');assert.equal(state.returnView,'pages');assert.equal($('editorHeading').textContent,'新建页面');assert.equal($('backPosts').textContent,'← 返回页面');
 let saveKeys=0;state.view='editor';$('savePostButton').onclick=()=>saveKeys++;handleEditorShortcut({key:'s',ctrlKey:true,preventDefault(){}});assert.equal(saveKeys,1);state.authenticated=false;handleEditorShortcut({key:'s',ctrlKey:true,preventDefault(){}});assert.equal(saveKeys,1);
})()
`
	encodedTests, _ := json.Marshal(tests)
	encodedI18n, _ := json.Marshal(string(adminI18nJS))
	runner = strings.ReplaceAll(runner, "UI_LOCALE_SCRIPT", string(encodedI18n))
	runner = strings.ReplaceAll(runner, "SOURCE", string(encoded))
	runner = strings.ReplaceAll(runner, "TESTS", string(encodedTests))
	path := filepath.Join(t.TempDir(), "ui-contract.js")
	if err := os.WriteFile(path, []byte(runner), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, node, path).CombinedOutput(); err != nil {
		t.Fatalf("UI behavior contracts failed: %v\n%s", err, out)
	}
}
