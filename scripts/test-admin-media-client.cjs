'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '../internal/blog/admin_ui.html'), 'utf8');
const helperSource = html.slice(html.indexOf('const editorHelperSelector='), html.indexOf('function stripEditorHelpers('));
const documentHelperSource = ['independentDocument', 'documentReturnView', 'documentNoun', 'validPublicPath'].map(name => html.match(new RegExp('function ' + name + '\\([^\\r\\n]+'))[0]).join('\n');
const actionSource = html.slice(html.indexOf('function documentActionLabel('),html.indexOf('function validPublicPath('));
const mediaSource = documentHelperSource + '\n' + actionSource + '\n' + helperSource + html.slice(html.indexOf('// Keep pasted image bytes'), html.indexOf('// End pasted image handling.'));
const saveSource = html.match(/async function saveEditor\(\)\{[^\r\n]+/)[0];
const manualSource = [html.match(/function chooseMedia\(kind\)\{[^\r\n]+/)[0], html.match(/\$\('mediaFile'\)\.onchange=[^\r\n]+/)[0]].join('\n');
const pixel = 'data:image/png;base64,iVBORw0KGgo=';
const uploaded = '/wp-content/uploads/2026/10/pasted.png';
const decode = text => text.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
const escape = text => String(text).replace(/&/g, '&amp;').replace(/"/g, '&quot;');

// This DOM contract double preserves surrounding markup and image attributes.
// Native browser paste, selection and rendering are verified separately.
class ImageMarkup {
  constructor(markup = '') {this.innerHTML = markup;}
  set innerHTML(markup) {this.raw = markup; this.images = null;}
  get innerHTML() {
    if (!this.images) return this.raw;
    let result = this.raw;
    for (const image of [...this.images].reverse()) result = result.slice(0, image.offset) + image.markup + result.slice(image.offset + image.originalLength);
    return result;
  }
  querySelectorAll(selector) {
    this.raw = this.innerHTML;
    this.images = [...this.raw.matchAll(/<(?:img|source)\b(?:[^"'<>]|"[^"]*"|'[^']*')*>/gi)].map(match => {
      const image = {offset: match.index, originalLength: match[0].length, markup: match[0]};
      const attribute = name => new RegExp('\\s' + name + '\\s*=\\s*(?:"([^"]*)"|\'([^\']*)\'|([^\\s>]+))', 'i');
      image.hasAttribute = name => attribute(name).test(image.markup);
      image.getAttribute = name => {const found = image.markup.match(attribute(name)); return found ? decode(found[1] ?? found[2] ?? found[3]) : null;};
      image.setAttribute = (name, value) => {image.markup = image.hasAttribute(name) ? image.markup.replace(attribute(name), ' ' + name + '="' + escape(value) + '"') : image.markup.replace(/>$/, ' ' + name + '="' + escape(value) + '">');};
      image.removeAttribute = name => {image.markup = image.markup.replace(attribute(name), '');};
      return image;
    });
    return selector === 'img' ? this.images.filter(image => /^<img\b/i.test(image.markup)) : this.images;
  }
}

function client(respond = async () => ({url: uploaded, type: 'image/png'})) {
  const nodes = new Map(), calls = [], notices = [], timers = [], revoked = [], storedFiles = new Map();
  let objectSequence = 0;
  const body = new ImageMarkup();
  const get = id => {if (!nodes.has(id)) nodes.set(id, {value: '', disabled: false, inert: false, selectionStart: 0, selectionEnd: 0, selectionDirection: 'none', children: [], click() {}, querySelector() {return null;}, replaceChildren(...children) {this.children = children;}, append(...children) {this.children.push(...children);}, prepend(...children) {this.children.unshift(...children);}, setSelectionRange(start, end, direction) {Object.assign(this, {selectionStart: start, selectionEnd: end, selectionDirection: direction});}}); return nodes.get(id);};
  get('visualEditor').contentDocument = {body};
  get('postTitle').value = 'Article';
  const state = {authenticated: true, user: 'editor', lang: 'zh', mode: 'source', view: 'editor', loadPostGeneration: 1, post: {id: 4, type: 'post', version: 7, title: 'Article', translations: {}, source_html: 'historical archive'}, editorGeneration: 2, editorReady: false, raw: '', visualBaseline: '', dirty: true, saving: false};
  class BrowserURL extends URL {
    static createObjectURL(file) {const source = 'blob:https://ganwen.test/' + ++objectSequence; storedFiles.set(source, file); return source;}
    static revokeObjectURL(source) {revoked.push(source); storedFiles.delete(source);}
  }
  const context = {
    state, $: get, Blob, FormData, Uint8Array, TextEncoder, atob, URL: BrowserURL, location: {origin: 'https://ganwen.test'},
    document: {createElement(tag) {
      if (tag === 'template') {const content = new ImageMarkup(); return {content, set innerHTML(value) {content.innerHTML = value;}, get innerHTML() {return content.innerHTML;}};}
      if (tag === 'img') return {src: '', alt: '', get outerHTML() {return '<img src="' + escape(this.src) + '" alt="' + escape(this.alt) + '">';}};
      return {children: [], append(...children) {this.children.push(...children);}, get outerHTML() {return '<' + tag + '>' + this.children.map(child => child.outerHTML || '').join('') + '</' + tag + '>';}};
    }},
    el: (tag, text) => {const node = context.document.createElement(tag); if (text !== undefined) node.textContent = text; return node;},
    getContent: () => state.mode === 'source' ? get('sourceEditor').value : body.innerHTML,
    refreshEditorDirty: () => {state.dirty = true;},
    invalidateVisualContent: () => {state.dirty = true; state.visualRevision = (state.visualRevision || 0) + 1;},
    setTimeout: callback => {timers.push(callback); return timers.length;},
    notify: (message, bad) => notices.push({message, bad}),
    rememberEditorSelection() {},
    command: (_, markup) => {body.innerHTML += markup; state.dirty = true;},
    fetch: async source => {if (!storedFiles.has(source)) throw Error('Unknown temporary image'); return {ok: true, blob: async () => storedFiles.get(source)};},
    api: async (endpoint, data) => {calls.push({endpoint, data}); return respond(endpoint, data, calls);},
    capture: () => ({...state.post, title: get('postTitle').value, content_html: context.getContent(), excerpt_html: get('excerpt').value, status: 'draft', categories: [], tags: [], seo: {title: '', description: ''}, source_hash: 'source-hash'}),
    setAdminRoute() {}, showEditor() {state.editorGeneration++;}, notifyEditorParent() {}, rememberSaveInteraction() {return null;}, restoreSaveInteraction() {}
  };
  vm.createContext(context);
  require("./admin-i18n-test-harness.cjs")(context);
  vm.runInContext(mediaSource + '\n' + manualSource + '\n' + saveSource, context);
  return {state, get, body, calls, notices, timers, revoked, storedFiles, context, run: source => vm.runInContext(source, context)};
}

async function drain(c) {while (c.timers.length) c.timers.shift()(); for (let i = 0; i < 5; i++) await new Promise(resolve => setImmediate(resolve));}
function setDraft(c, content, excerpt = '') {c.get('sourceEditor').value = content; c.get('excerpt').value = excerpt; c.state.raw = content; c.body.innerHTML = content;}

function startManualImage(c) {
  c.state.mode = 'visual'; c.state.editorReady = true;
  c.run("chooseMedia('image')");
  c.get('mediaFile').files = [new Blob(['fixture-image'], {type: 'image/png'})];
  return c.get('mediaFile').onchange();
}

test('manual media response cannot revive an editor discarded during its upload', async () => {
  let finish;
  const c = client(() => new Promise(resolve => finish = resolve));
  setDraft(c, '<p>Original</p>');
  const pending = startManualImage(c);
  c.state.view = 'posts'; c.state.loadPostGeneration++; c.state.dirty = false;
  finish({url: uploaded, type: 'image/png', name: 'fixture.png'});
  await pending;
  assert.equal(c.body.innerHTML, '<p>Original</p>');
  assert.equal(c.state.dirty, false);
});

test('manual media upload prevents a premature save and restores saving after insertion', async () => {
  let finish;
  const c = client(() => new Promise(resolve => finish = resolve));
  setDraft(c, '<p>Original</p>');
  const pending = startManualImage(c);
  assert.equal(c.get('savePostButton').disabled, true);
  assert.equal(c.get('savePostButton').textContent, '媒体上传中…');
  assert.match(c.get('savePostButton').title, /媒体正在上传/);
  await c.run('saveEditor()');
  assert.deepEqual(c.calls.map(call => call.endpoint), ['upload']);
  finish({url: uploaded, type: 'image/png', name: 'fixture.png'});
  await pending;
  assert.match(c.body.innerHTML, /pasted\.png/);
  assert.equal(c.get('savePostButton').disabled, false);
  assert.equal(c.get('savePostButton').textContent, '保存文章');
  assert.equal(c.get('savePostButton').title, '');
});

test('manual image finishing after a mode switch inserts into source without dropping current text', async () => {
  let finish;
  const c = client(() => new Promise(resolve => finish = resolve));
  setDraft(c, '<p>Original</p>');
  const pending = startManualImage(c);
  c.state.mode = 'source';
  c.get('sourceEditor').value = '<p>Before</p><p>Typed while uploading</p>';
  c.get('sourceEditor').selectionStart = c.get('sourceEditor').selectionEnd = 13;
  finish({url: uploaded, type: 'image/png', name: 'fixture.png'});
  await pending;
  const current = c.get('sourceEditor').value;
  assert.ok(current.startsWith('<p>Before</p>'));
  assert.match(current, /pasted\.png/);
  assert.ok(current.endsWith('<p>Typed while uploading</p>'));
  assert.equal(c.state.dirty, true);
});

test('an earlier manual upload cannot clear the next open file picker target', async () => {
  let finish;
  const c = client(() => new Promise(resolve => finish = resolve));
  const pending = startManualImage(c);
  c.run("chooseMedia('image')");
  const nextTarget = c.state.uploadTarget;
  finish({url: uploaded, type: 'image/png', name: 'fixture.png'});
  await pending;
  assert.equal(c.state.uploadTarget, nextTarget);
});

test('saving a previously oversized draft uploads data images and excludes archive/all-language bodies from its request', async () => {
  const c = client(async (endpoint, data) => endpoint === 'upload' ? {url: uploaded, type: 'image/png'} : {post: {...data, version: 8}, source_hash: 'new-hash'});
  c.state.post.source_html = 'x'.repeat(5 * 1024 * 1024);
  c.state.post.translations = {en: {content_html: 'y'.repeat(5 * 1024 * 1024)}};
  const data = 'data:image/png;base64,' + Buffer.alloc(3200000, 1).toString('base64');
  setDraft(c, '<p>Before</p><img src="' + data + '"><p>After</p>');
  await c.run('saveEditor()');
  assert.deepEqual(c.calls.map(call => call.endpoint), ['upload', 'post']);
  const payload = c.calls[1].data;
  assert.equal(payload.content_html, '<p>Before</p><img src="' + uploaded + '"><p>After</p>');
  assert.equal(payload.source_html, undefined);
  assert.equal(payload.translations, undefined);
  assert.equal(payload.version, 7);
  assert.ok(JSON.stringify(payload).length < 1000);
  assert.equal(c.calls[0].data.get('file').size, 3200000);
});

test('body, excerpt and responsive srcset references share one upload while text and candidate descriptors stay in place', async () => {
  const c = client();
  setDraft(c, '<p>A</p><picture><source srcset="' + pixel + ' 2x"><img src="' + pixel + '" srcset="' + pixel + ' 1x, /other.png 2x" alt="B"></picture><p>C</p>', '<p>Summary<img src="' + pixel + '"></p>');
  await c.run('normalizeInlineImages(inlineMediaTarget())');
  assert.equal(c.calls.length, 1);
  assert.equal(c.get('sourceEditor').value, '<p>A</p><picture><source srcset="' + uploaded + ' 2x"><img src="' + uploaded + '" srcset="' + uploaded + ' 1x, /other.png 2x" alt="B"></picture><p>C</p>');
  assert.equal(c.get('excerpt').value, '<p>Summary<img src="' + uploaded + '"></p>');
});

test('an upload failure blocks saving, preserves all draft content and succeeds on retry without a duplicate document write', async () => {
  let attempts = 0;
  const c = client(async (endpoint, data) => {
    if (endpoint === 'upload') {if (++attempts === 1) throw Error('网络暂时不可用'); return {url: uploaded, type: 'image/png'};}
    return {post: {...data, version: 8}, source_hash: 'new-hash'};
  });
  const original = '<p>Text<img src="' + pixel + '"></p>';
  setDraft(c, original);
  await c.run('saveEditor()');
  assert.equal(c.get('sourceEditor').value, original);
  assert.equal(c.state.dirty, true);
  assert.equal(c.state.saving, false);
  assert.equal(c.calls.filter(call => call.endpoint === 'post').length, 0);
  await c.run('saveEditor()');
  assert.equal(attempts, 2);
  assert.equal(c.calls.filter(call => call.endpoint === 'post').length, 1);
});

test('a rejected article save reuses its successfully uploaded image on the next save', async () => {
  let saves = 0;
  const c = client(async (endpoint, data) => {
    if (endpoint === 'upload') return {url: uploaded, type: 'image/png'};
    if (++saves === 1) throw Error('保存暂时失败');
    return {post: {...data, version: 8}, source_hash: 'hash'};
  });
  setDraft(c, '<img src="' + pixel + '">');
  await c.run('saveEditor()');
  assert.match(c.get('sourceEditor').value, /wp-content\/uploads/);
  await c.run('saveEditor()');
  assert.equal(c.calls.filter(call => call.endpoint === 'upload').length, 1);
  assert.equal(saves, 2);
});

test('an automatic upload and repeated save clicks share the pending upload and emit one article save', async () => {
  let finish;
  const c = client(async (endpoint, data) => endpoint === 'upload' ? new Promise(resolve => {finish = () => resolve({url: uploaded, type: 'image/png'});}) : {post: {...data, version: 8}, source_hash: 'hash'});
  setDraft(c, '<img src="' + pixel + '">');
  const automatic = c.run('normalizeInlineImages(inlineMediaTarget(),true)');
  for (let i = 0; !finish && i < 5; i++) await new Promise(resolve => setImmediate(resolve));
  const saved = c.run('saveEditor()');
  await c.run('saveEditor()');
  assert.equal(c.calls.length, 1);
  finish();
  await Promise.all([automatic, saved]);
  assert.deepEqual(c.calls.map(call => call.endpoint), ['upload', 'post']);
});

test('switching language during an upload cannot mutate or save the newly selected draft', async () => {
  let finish;
  const c = client(async () => new Promise(resolve => {finish = () => resolve({url: uploaded, type: 'image/png'}); }));
  setDraft(c, '<img src="' + pixel + '">');
  const pending = c.run('saveEditor()');
  for (let i = 0; !finish && i < 5; i++) await new Promise(resolve => setImmediate(resolve));
  c.state.lang = 'nl';
  c.state.editorGeneration++;
  setDraft(c, '<p>New Dutch content</p>');
  finish();
  await pending;
  assert.equal(c.get('sourceEditor').value, '<p>New Dutch content</p>');
  assert.equal(c.calls.length, 1);
});

test('switching editor mode during upload replaces only matching image attributes and retains edits made meanwhile', async () => {
  let finish;
  const c = client(async () => new Promise(resolve => {finish = () => resolve({url: uploaded, type: 'image/png'}); }));
  c.state.mode = 'visual'; c.state.editorReady = true;
  setDraft(c, '<p>Old<img src="' + pixel + '"></p>');
  const pending = c.run('normalizeInlineImages(inlineMediaTarget())');
  for (let i = 0; !finish && i < 5; i++) await new Promise(resolve => setImmediate(resolve));
  c.state.mode = 'source';
  c.get('sourceEditor').value = '<p>New text<img src="' + pixel + '">Still here</p>';
  finish();
  await pending;
  assert.equal(c.get('sourceEditor').value, '<p>New text<img src="' + uploaded + '">Still here</p>');
});

test('file-only paste inserts one local image immediately and preserves the original bytes for a failed-upload retry', async () => {
  let fail = true;
  const c = client(async () => {if (fail) throw Error('上传失败'); return {url: uploaded, type: 'image/png'};});
  c.state.mode = 'visual'; c.state.editorReady = true;
  const file = new Blob([new Uint8Array([137, 80, 78, 71])], {type: 'image/png'});
  let prevented = false;
  c.context.event = {clipboardData: {files: [file], getData: () => ''}, preventDefault() {prevented = true;}};
  c.run('handleEditorPaste(event)');
  assert.equal(prevented, true);
  assert.match(c.body.innerHTML, /blob:https:\/\/ganwen.test\/1/);
  await drain(c);
  assert.equal(c.calls.length, 1);
  assert.match(c.body.innerHTML, /blob:/);
  fail = false;
  await c.run('normalizeInlineImages(inlineMediaTarget())');
  assert.match(c.body.innerHTML, /wp-content\/uploads/);
  assert.equal(c.calls[1].data.get('file').size, 4);
});

test('HTML plus image clipboard data uses native insertion once and preserves text surrounding its image', async () => {
  const c = client();
  c.state.mode = 'visual'; c.state.editorReady = true;
  const markup = '<p>Before<img src="' + pixel + '">After</p>';
  const file = new Blob(['png'], {type: 'image/png'});
  let prevented = false;
  c.context.event = {clipboardData: {files: [file], getData: () => markup}, preventDefault() {prevented = true;}};
  c.run('handleEditorPaste(event)');
  assert.equal(prevented, false);
  assert.equal(c.body.innerHTML, '');
  c.body.innerHTML = markup;
  await drain(c);
  assert.equal(c.body.innerHTML, '<p>Before<img src="' + uploaded + '">After</p>');
  assert.equal(c.calls.length, 1);
});

test('foreign blob references fail locally without dropping the image or attempting a network read', async () => {
  const c = client();
  let fetches = 0;
  c.context.fetch = async () => {fetches++; throw Error('Must not fetch');};
  const original = '<img src="blob:https://other.test/old">';
  setDraft(c, original);
  await assert.rejects(c.run('normalizeInlineImages(inlineMediaTarget())'), /不属于当前网站/);
  assert.equal(c.get('sourceEditor').value, original);
  assert.equal(fetches, 0);
  assert.equal(c.calls.length, 0);
});

test('unsupported and oversized images retain their draft without uploading or weakening file limits', async () => {
  const c = client();
  setDraft(c, '<img src="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=">');
  await assert.rejects(c.run('normalizeInlineImages(inlineMediaTarget())'), /格式不支持/);
  const original = '<img src="blob:https://ganwen.test/large">';
  setDraft(c, original);
  c.run("inlineMediaState().files.set('blob:https://ganwen.test/large',{size:32*1024*1024+1,type:'image/png'})");
  await assert.rejects(c.run('normalizeInlineImages(inlineMediaTarget())'), /32 MiB/);
  assert.equal(c.get('sourceEditor').value, original);
  assert.equal(c.calls.length, 0);
});

test('object URLs live through retries and are revoked when that editor lifecycle is discarded', async () => {
  const c = client(async () => {throw Error('Keep local draft');});
  c.state.mode = 'visual'; c.state.editorReady = true;
  c.context.event = {clipboardData: {files: [new Blob(['png'], {type: 'image/png'})], getData: () => ''}, preventDefault() {}};
  c.run('handleEditorPaste(event)');
  await drain(c);
  assert.equal(c.revoked.length, 0);
  c.state.authenticated = false;
  c.run('inlineMediaState()');
  assert.equal(c.revoked.length, 0);
  c.state.editorGeneration++;
  c.run('inlineMediaState()');
  assert.deepEqual(c.revoked, ['blob:https://ganwen.test/1']);
});

test('translation payload keeps machine provenance while dropping read-only timestamps', () => {
  const c = client();
  const payload = c.run("editableDocumentPayload({title:'Translated',content_html:'<p>Text</p>',source_hash:'source',machine_hash:'machine',updated_at:'old-time'},'en')");
  assert.equal(payload.source_hash, 'source');
  assert.equal(payload.machine_hash, 'machine');
  assert.equal(payload.updated_at, undefined);
});

test('clipboard HTML with text only still inserts its image file without losing the caption', async () => {
  const c = client();
  c.state.mode = 'visual'; c.state.editorReady = true;
  let prevented = false;
  c.context.event = {clipboardData: {files: [new Blob(['png'], {type: 'image/png'})], getData: () => '<p>Caption</p>'}, preventDefault() {prevented = true;}};
  c.run('handleEditorPaste(event)');
  assert.equal(prevented, true);
  assert.match(c.body.innerHTML, /^<p>Caption<\/p><img src="blob:/);
  await drain(c);
  assert.match(c.body.innerHTML, /^<p>Caption<\/p><img src="\/wp-content\/uploads\//);
  assert.equal(c.calls.length, 1);
});

test('clipboard local-file markup is replaced from clipboard bytes without accessing the original path', async () => {
  const c = client();
  c.state.mode = 'visual'; c.state.editorReady = true;
  let fetches = 0;
  c.context.fetch = async () => {fetches++; throw Error('Must not read a local path');};
  c.context.event = {clipboardData: {files: [new Blob(['png'], {type: 'image/png'})], getData: () => '<p>Before<img src="file:///private/picture.png" srcset="file:///private/picture.png 2x">After</p>'}, preventDefault() {}};
  c.run('handleEditorPaste(event)');
  assert.match(c.body.innerHTML, /^<p>Before<img src="blob:/);
  assert.doesNotMatch(c.body.innerHTML, /file:|srcset/);
  await drain(c);
  assert.equal(c.body.innerHTML, '<p>Before<img src="' + uploaded + '">After</p>');
  assert.equal(fetches, 0);
  assert.equal(c.calls.length, 1);
});

test('an upload completing after navigation cannot revive an abandoned article draft', async () => {
  let finish;
  const c = client(async () => new Promise(resolve => {finish = () => resolve({url: uploaded, type: 'image/png'});}));
  const original = '<p>Old<img src="' + pixel + '"></p>';
  setDraft(c, original);
  const pending = c.run('normalizeInlineImages(inlineMediaTarget())');
  for (let i = 0; !finish && i < 5; i++) await new Promise(resolve => setImmediate(resolve));
  c.state.loadPostGeneration++;
  c.state.view = 'settings';
  c.state.dirty = false;
  finish();
  await assert.rejects(pending, /未插入其他文章/);
  assert.equal(c.state.dirty, false);
  assert.equal(c.get('sourceEditor').value, original);
});
