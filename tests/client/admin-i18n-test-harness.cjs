'use strict';
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const script=fs.readFileSync(path.join(__dirname,'../../internal/blog/web/admin/admin_i18n.js'),'utf8');
const messages=fs.readFileSync(path.join(__dirname,'../../internal/blog/web/admin/admin_messages.json'),'utf8');

module.exports=function installAdminI18n(context){
  context.window ||= {};
  context.window.document=context.document;
  context.window.localStorage=context.localStorage;
  context.window.location=context.location;
  context.window.navigator={languages:['zh-CN']};
  vm.runInContext(script.replace('__UI_MESSAGES__',messages),context);
  context.ui=context.window.GanwenUI;
  return context.ui;
};
