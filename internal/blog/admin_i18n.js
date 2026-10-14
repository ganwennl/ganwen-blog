(function(global){
  'use strict';
  // Only explicitly marked application text is translated. Editor/user content is never traversed.
  const messages=__UI_MESSAGES__;
  const languages=[['zh','中文'],['nl','Nederlands'],['en','English'],['hi','हिन्दी'],['es','Español'],['ar','العربية'],['fr','Français'],['bn','বাংলা'],['pt','Português'],['id','Bahasa Indonesia'],['ur','اردو']];
  const key='ganwen.admin-ui-language.v1';
  function valid(value){return languages.some(([code])=>code===value)}
  function preferred(){
    let saved='';try{saved=global.localStorage.getItem(key)||''}catch{}
    if(valid(saved))return saved;
    const cookies=(global.document?.cookie||'').split(';').map(v=>v.trim());
    for(const name of ['ganwen_admin_ui_language=','ganwen_language=']){const cookie=cookies.find(v=>v.startsWith(name));if(cookie){const value=cookie.slice(name.length);if(valid(value))return value}}
    for(const language of global.navigator?.languages||[global.navigator?.language||'zh']){const code=language.toLowerCase().split('-')[0];if(valid(code))return code}
    return 'zh';
  }
  const lang=preferred(),locale=lang==='zh'?'zh-CN':lang;
  function t(source,values=[]){
    const dictionary=messages[lang]||messages.en||{};
    const text=Object.hasOwn(dictionary,source)?dictionary[source]:Object.hasOwn(messages.en||{},source)?messages.en[source]:source;
    return String(text).replace(/\{(\d+)\}/g,(match,n)=>Number(n)<values.length?String(values[Number(n)]):match);
  }
  function error(message){
    // Keep the legacy API message contract while showing accurate UI guidance
    // for the ten supported translation targets.
    if(message==='译文支持英语、荷兰语和西班牙语')return t('不支持该语言');
    if(typeof message==='string'&&Object.hasOwn(messages.zh||{},message))return t(message);
    return t('请求失败，请稍后重试');
  }
  function concat(...parts){
    let text='';
    for(const part of parts){const value=String(part);if(lang!=='zh'&&/[\p{L}\p{N}\p{M}]$/u.test(text)&&/^[\p{L}\p{N}]/u.test(value))text+=' ';text+=value}
    return text;
  }
  function ordered(){return languages.filter(([code])=>code===lang).concat(languages.filter(([code])=>code!==lang))}
  function apply(root){
    if(global.document?.documentElement){global.document.documentElement.lang=locale;global.document.documentElement.dir=['ar','ur'].includes(lang)?'rtl':'ltr'}
    root.querySelectorAll('[data-ui-key]').forEach(node=>{node.textContent=t(node.dataset.uiKey)});
    for(const attr of ['title','placeholder','aria-label'])root.querySelectorAll('[data-ui-'+attr+']').forEach(node=>node.setAttribute(attr,t(node.getAttribute('data-ui-'+attr))));
  }
  function persist(value){
    if(!valid(value))return false;
    let saved=false;try{global.localStorage.setItem(key,value);saved=true}catch{}
    try{global.document.cookie='ganwen_admin_ui_language='+value+'; Path=/admin; Max-Age=31536000; SameSite=Lax'+(global.location?.protocol==='https:'?'; Secure':'');saved=saved||(global.document.cookie||'').split(';').some(v=>v.trim()==='ganwen_admin_ui_language='+value)}catch{}
    return saved;
  }
  function bindPicker(node,canLeave){
    for(const [code,name] of ordered()){const option=global.document.createElement('option');option.value=code;option.textContent=name;option.lang=code;option.dir=['ar','ur'].includes(code)?'rtl':'ltr';option.selected=code===lang;node.append(option)}
    node.onchange=()=>{const next=node.value;if(next===lang)return;if(!canLeave()){node.value=lang;return}if(persist(next))global.location.reload();else{node.value=lang;global.alert(t('请求失败，请稍后重试'))}};
  }
  function date(value,options={}){const d=new Date(value);return Number.isNaN(d.getTime())?String(value||''):d.toLocaleString(locale,options)}
  global.GanwenUI=Object.freeze({lang,locale,t,error,concat,ordered,apply,persist,bindPicker,date});
})(typeof window==='undefined'?globalThis:window);
