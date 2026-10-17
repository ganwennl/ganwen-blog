'use strict';
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const template=fs.readFileSync(path.join(__dirname,'../../internal/blog/web/admin/admin_i18n.js'),'utf8');
const messages=JSON.parse(fs.readFileSync(path.join(__dirname,'../../internal/blog/web/admin/admin_messages.json'),'utf8'));
const codes=['zh','nl','en','hi','es','ar','fr','bn','pt','id','ur'];

function load({saved='',cookie='',browser=['zh-CN'],dictionary=messages,blocked=false}={}){
  let currentCookie=cookie,reloads=0;
  const writes=[],alerts=[];
  const document={documentElement:{},querySelectorAll(){return []},createElement(){return {}}};
  Object.defineProperty(document,'cookie',{get:()=>currentCookie,set:value=>{writes.push(value);currentCookie=value.split(';')[0]}});
  const localStorage={getItem(){if(blocked)throw Error('blocked');return saved},setItem(k,v){if(blocked)throw Error('blocked');writes.push([k,v]);saved=v}};
  const window={document,localStorage,navigator:{languages:browser},location:{protocol:'https:',reload(){reloads++}},alert:v=>alerts.push(v)};
  const context={window,Intl,Date};vm.createContext(context);
  vm.runInContext(template.replace('__UI_MESSAGES__',JSON.stringify(dictionary)),context);
  return {ui:window.GanwenUI,document,writes,alerts,get reloads(){return reloads}};
}

test('all eleven selected interface languages lead the stable native-name list and set their own direction',()=>{
  for(const lang of codes){
    const c=load({saved:lang});c.ui.apply(c.document);
    assert.deepEqual(Array.from(c.ui.ordered(),row=>row[0]),[lang,...codes.filter(code=>code!==lang)]);
    assert.equal(c.document.documentElement.lang,lang==='zh'?'zh-CN':lang);
    assert.equal(c.document.documentElement.dir,['ar','ur'].includes(lang)?'rtl':'ltr');
    assert.equal(c.ui.lang,lang);
  }
});

test('saved admin preference wins over visitor/article locale; invalid values never become markup or a language',()=>{
  assert.equal(load({saved:'fr',cookie:'ganwen_language=nl',browser:['en']}).ui.lang,'fr');
  assert.equal(load({saved:'../../bad',cookie:'ganwen_admin_ui_language=pt; ganwen_language=en'}).ui.lang,'pt');
  assert.equal(load({cookie:'ganwen_language=ur'}).ui.lang,'ur');
  assert.equal(load({browser:['xx-Unknown','hi-IN']}).ui.lang,'hi');
  assert.equal(load({saved:'<script>',cookie:'ganwen_language=../fr',browser:['zz']}).ui.lang,'zh');
});

test('language changes respect unsaved-content refusal and persist only admin UI preference',()=>{
  const c=load({saved:'zh'}),picker={children:[],append(n){this.children.push(n)}};
  let mayLeave=false;c.ui.bindPicker(picker,()=>mayLeave);
  assert.equal(picker.children.length,11);assert.equal(picker.children[0].value,'zh');
  picker.value='ar';picker.onchange();assert.equal(picker.value,'zh');assert.equal(c.reloads,0);assert.equal(c.writes.length,0);
  mayLeave=true;picker.value='ar';picker.onchange();assert.equal(c.reloads,1);
  assert.ok(c.writes.some(v=>Array.isArray(v)&&v[0]==='ganwen.admin-ui-language.v1'&&v[1]==='ar'));
  assert.ok(c.writes.some(v=>typeof v==='string'&&v.startsWith('ganwen_admin_ui_language=ar; Path=/admin;')&&v.includes('; Secure')));
  assert.ok(c.writes.every(v=>typeof v!=='string'||!v.startsWith('ganwen_language=')),'article visitor locale must remain separate');
});

test('blocked local storage can retain the admin choice in its scoped cookie',()=>{
  const c=load({blocked:true});assert.equal(c.ui.persist('id'),true);
  assert.equal(load({blocked:true,cookie:'ganwen_admin_ui_language=id'}).ui.lang,'id');
  assert.equal(c.ui.persist('invalid'),false);
});

test('explicit static nodes and attributes translate as text; user titles, categories and editor body stay intact',()=>{
  const c=load({saved:'en',dictionary:{zh:{'标题':'标题','提示':'提示'},en:{'标题':'<b>Title</b>','提示':'Help "quoted"'}}});
  const staticNode={dataset:{uiKey:'标题'},textContent:'标题'},attributes={title:'提示'};
  const attrNode={getAttribute:()=> '提示',setAttribute:(k,v)=>attributes[k]=v};
  const user={title:'标题',category:'提示',body:'<p>标题</p>',value:'提示'};
  const root={querySelectorAll(selector){return selector==='[data-ui-key]'?[staticNode]:selector==='[data-ui-title]'?[attrNode]:[]}};
  c.ui.apply(root);
  assert.equal(staticNode.textContent,'<b>Title</b>');assert.equal(attributes.title,'Help "quoted"');
  assert.deepEqual(user,{title:'标题',category:'提示',body:'<p>标题</p>',value:'提示'});
});

test('translated templates support language-specific parameter order and never interpret parameter HTML',()=>{
  const c=load({saved:'fr',dictionary:{zh:{'显示 {0}–{1} / {2} 条':'显示 {0}–{1} / {2} 条'},fr:{'显示 {0}–{1} / {2} 条':'{2} éléments : {0} à {1}'}}});
  assert.equal(c.ui.t('显示 {0}–{1} / {2} 条',['<img>',5,20]),'20 éléments : <img> à 5');
});

test('translated dynamic labels keep word boundaries while Chinese compositions stay unchanged',()=>{
  assert.equal(load({saved:'en'}).ui.concat('Edit','Pages'),'Edit Pages');
  assert.equal(load({saved:'ar'}).ui.concat('نشر','منشور'),'نشر منشور');
  assert.equal(load({saved:'zh'}).ui.concat('编辑','页面'),'编辑页面');
  assert.equal(load({saved:'en'}).ui.concat('Details: ',5,' items'),'Details: 5 items');
});

test('known API errors use the selected dictionary while unexpected server text uses a translated fallback',()=>{
  const c=load({saved:'nl',dictionary:{zh:{'请先登录':'请先登录','请求失败，请稍后重试':'请求失败，请稍后重试'},nl:{'请先登录':'Log eerst in','请求失败，请稍后重试':'Probeer het later opnieuw'}}});
  assert.equal(c.ui.error('请先登录'),'Log eerst in');assert.equal(c.ui.error('unrecognized internal details'),'Probeer het later opnieuw');
  const en=load({saved:'en'});assert.equal(en.ui.error('译文支持英语、荷兰语和西班牙语'),en.ui.t('不支持该语言'));
  for(const lang of codes){const active=load({saved:lang});for(const key of ['页面地址已被其他页面使用','广告间隔须为 3 至 120 秒','页脚译文版本不正确，请重新编辑或翻译']){assert.equal(active.ui.error(key),active.ui.t(key));assert.notEqual(active.ui.error(key),active.ui.t('请求失败，请稍后重试'))}}
});

test('date formatting follows interface locale without changing the input or article language',()=>{
  const value='2026-10-14T17:25:00Z',c=load({saved:'fr'});
  const expected=new Date(value).toLocaleString('fr',{timeZone:'UTC',year:'numeric',month:'long',day:'numeric'});
  assert.equal(c.ui.date(value,{timeZone:'UTC',year:'numeric',month:'long',day:'numeric'}),expected);
  assert.equal(c.ui.date(''), '');assert.equal(value,'2026-10-14T17:25:00Z');
});
