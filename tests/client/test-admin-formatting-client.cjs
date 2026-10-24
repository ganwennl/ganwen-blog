'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '../../internal/blog/web/admin/admin_ui.html'), 'utf8');
const plainSource = html.slice(html.indexOf('const editorMediaVisualProperties='), html.indexOf('function clearEditorFormattingHTML('));
const harness = fs.readFileSync(path.join(__dirname, 'test-admin-performance-client.cjs'), 'utf8');
const harnessRequire = id => id === 'node:test' ? {test() {}} : require(id);
const {client} = new Function('require', '__dirname', harness + '\nreturn {client};')(harnessRequire, __dirname);
const escapeText = value => String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const voidTags = new Set(['img', 'source', 'track', 'br', 'hr', 'input', 'meta', 'link', 'base']);

// A small DOM tree double exercises the actual recursive cleanup with media,
// nested paragraphs and text nodes. Native HTML parsing, layout and undo are
// deliberately left to the separate browser checks.
function tree() {
  class Style {
    constructor() {this.values = new Map();}
    set cssText(raw) {this.values.clear(); for (const declaration of raw.split(';')) {const split = declaration.indexOf(':'); if (split < 1) continue; const property = declaration.slice(0, split).trim().toLowerCase(), value = declaration.slice(split + 1).trim(), important = /\s*!important$/i.test(value); this.setProperty(property, value.replace(/\s*!important$/i, ''), important ? 'important' : '');}}
    get cssText() {return [...this.values].map(([property, {value, priority}]) => property + ': ' + value + (priority ? ' !important' : '') + ';').join(' ');}
    setProperty(property, value, priority = '') {this.values.set(property, {value, priority}); [...this.values.keys()].forEach((name, index) => this[index] = name);}
    get length() {return this.values.size;}
    getPropertyValue(property) {return this.values.get(property)?.value || '';}
    getPropertyPriority(property) {return this.values.get(property)?.priority || '';}
  }
  class Sheet {
    replaceSync(raw) {this.cssRules = [...raw.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(match => {const style = new Style(); style.cssText = match[2]; return {type: 1, selectorText: match[1].trim(), style};});}
  }
  class Node {
    constructor(type, tag = '', text = '') {Object.assign(this, {nodeType: type, tagName: tag.toUpperCase(), nodeValue: text, childNodes: [], attrs: new Map(), ownerDocument: document, parentNode: null, style: new Style()});}
    append(...nodes) {for (const node of nodes) {if (node.nodeType === 11) {this.append(...[...node.childNodes]); continue;} node.remove(); node.parentNode = this; this.childNodes.push(node);}}
    remove() {if (this.parentNode) {const children = this.parentNode.childNodes; children.splice(children.indexOf(this), 1); this.parentNode = null;}}
    setAttribute(name, value) {this.attrs.set(name, String(value));}
    getAttribute(name) {return this.attrs.get(name) ?? null;}
    removeAttribute(name) {this.attrs.delete(name);}
    get attributes() {return [...this.attrs].map(([name, value]) => ({name, value}));}
    matches(selector) {return selector.split(',').some(part => part.startsWith('.') && (this.getAttribute('class') || '').split(/\s+/).includes(part.slice(1)));}
    querySelectorAll(selector) {const result = []; for (const child of this.childNodes) if (child.nodeType === 1) {if (selector === '*' || child.tagName.toLowerCase() === selector) result.push(child); result.push(...child.querySelectorAll(selector));} return result;}
    cloneNode(deep) {const copy = new Node(this.nodeType, this.tagName, this.nodeValue); for (const [name, value] of this.attrs) copy.setAttribute(name, value); if (deep) for (const child of this.childNodes) copy.append(child.cloneNode(true)); return copy;}
    get textContent() {return this.nodeType === 3 ? this.nodeValue : this.childNodes.map(node => node.textContent).join('');}
    set textContent(value) {for (const child of [...this.childNodes]) child.remove(); if (value) this.append(document.createTextNode(value));}
    get innerHTML() {return this.childNodes.map(node => node.outerHTML).join('');}
    get outerHTML() {if (this.nodeType === 3) return escapeText(this.nodeValue); if (this.nodeType === 11) return this.innerHTML; const tag = this.tagName.toLowerCase(), attrs = [...this.attrs].map(([name, value]) => ' ' + name + '="' + escapeText(value).replace(/"/g, '&quot;') + '"').join(''); return '<' + tag + attrs + '>' + (voidTags.has(tag) ? '' : this.innerHTML + '</' + tag + '>');}
  }
  const document = {createElement: tag => new Node(1, tag), createTextNode: text => new Node(3, '', text), createDocumentFragment: () => new Node(11)};
  const element = (tag, attrs = {}, ...children) => {const node = document.createElement(tag); for (const [name, value] of Object.entries(attrs)) node.setAttribute(name, value); node.append(...children.map(child => typeof child === 'string' ? document.createTextNode(child) : child)); return node;};
  const root = (...children) => {const fragment = document.createDocumentFragment(); fragment.append(...children); return fragment;};
  const context = {document, CSSStyleSheet: Sheet, editorHelperSelector: '.ries-writing-assistant-badge-host,.ries-translation-extension-container'};
  vm.createContext(context); vm.runInContext(plainSource, context);
  const cleanTree = input => {context.input = input; return vm.runInContext('plainEditorContent(input)', context);};
  return {element, root, cleanTree, clean: input => cleanTree(input).innerHTML};
}

test('whole-document cleanup removes nested X text styles and links while keeping every image and paragraph', () => {
  const {element: e, root, clean} = tree();
  const picture = e('img', {src: '/wp-content/uploads/photo.jpg', srcset: '/photo-300.jpg 300w, /photo-900.jpg 900w', width: 900, height: 600, alt: '原图', loading: 'lazy'});
  const emoji = e('img', {src: 'https://abs.twimg.com/emoji/v2/svg/1f923.svg', alt: '🤣', class: 'r-foreign emoji'});
  const input = root(e('div', {style: 'text-align:center', class: 'external-layout'}, e('div', {}, e('font', {color: 'red', size: 7}, e('b', {}, '第一段')), e('a', {href: 'https://external.test'}, e('span', {style: 'font-size:48px'}, '链接文字'), emoji)), e('p', {align: 'right'}, e('a', {href: '/photo'}, picture)), e('div', {}, '末段')));
  const result = clean(input);
  assert.equal(result, '<p>第一段链接文字<img src="https://abs.twimg.com/emoji/v2/svg/1f923.svg" alt="🤣" class="r-foreign emoji"></p><p>' + picture.outerHTML + '</p><p>末段</p>');
  assert.match(input.innerHTML, /external-layout/, 'the source tree must remain unchanged for undo');
  assert.equal((result.match(/<img /g) || []).length, 2);
});

test('cleanup preserves explicit paragraph and line boundaries without adding wrapper paragraphs', () => {
  const {element: e, root, clean} = tree();
  assert.equal(clean(root(e('section', {}, e('div', {}, e('p', {}, 'A', e('br'), 'B'), e('p'), e('h2', {}, 'Heading'), e('div', {}, 'Tail'))))), '<p>A<br>B</p><p></p><p>Heading</p><p>Tail</p>');
  assert.equal(clean(root(e('pre', {}, 'line 1\nline 2'))), '<p>line 1<br>line 2</p>');
});

test('hidden executable markup never becomes visible text and media execution attributes are removed', () => {
  const {element: e, root, clean} = tree();
  const result = clean(root(e('p', {}, 'Visible', e('script', {}, 'secretScript()'), e('style', {}, '.hidden{display:none}'), e('noscript', {}, 'hidden fallback')), e('video', {src: '/movie.mp4', controls: '', poster: '/poster.jpg', onplay: 'bad()', style: 'color:red'}, e('source', {src: '/movie.webm', type: 'video/webm', onerror: 'bad()'}), e('script', {}, 'videoScript()')), e('iframe', {src: 'https://www.youtube.com/embed/abc', srcdoc: '<script>bad()</script>', onload: 'bad()'}), e('iframe', {src: 'data:text/html,<script>bad()</script>'}), e('img', {src: 'java\nscript:bad()', alt: 'fallback'})));
  assert.match(result, /<video src="\/movie.mp4" controls="" poster="\/poster.jpg" style="color: red;"><source src="\/movie.webm" type="video\/webm"><\/video>/);
  assert.match(result, /<iframe src="https:\/\/www.youtube.com\/embed\/abc"><\/iframe>/);
  for (const forbidden of ['secretScript', 'hidden fallback', 'videoScript', 'srcdoc', 'onplay', 'onerror', 'onload', '<script', '<style', 'javascript', 'data:text/html']) assert.equal(result.includes(forbidden), false, forbidden);
});

test('cleanup retains draft image bytes and responsive picture media for pending upload replacement', () => {
  const {element: e, root, clean} = tree();
  const input = root(e('picture', {}, e('source', {srcset: '/wide.webp 1200w', media: '(min-width:800px)'}), e('img', {src: 'blob:https://ganwen.test/pending', srcset: 'data:image/png;base64,aGVsbG8= 1x', class: 'wp-smiley', alt: '待上传'})));
  assert.equal(clean(input), '<p>' + input.innerHTML + '</p>');
});

test('cleanup preserves image visual dimensions while rejecting active and external CSS values', () => {
  const {element: e, root, clean} = tree();
  const result = clean(root(e('img', {src: '/photo.jpg', class: 'aligncenter custom-photo', style: 'width:160px;height:80px;object-fit:contain;object-position:center;behavior:url(bad);filter:url(https://bad.test/filter);transform:expression(bad())', alt: '图像'})));
  assert.equal(result, '<p><img src="/photo.jpg" class="aligncenter custom-photo" style="width: 160px; height: 80px; object-fit: contain; object-position: center;" alt="图像"></p>');
});

test('cleanup preserves class-driven SVG fills, strokes, gradients and dimensions inside its own scope', () => {
  const {element: e, root, cleanTree} = tree();
  const original = root(e('svg', {viewBox: '0 0 100 50', class: 'artwork', style: 'width:200px;height:100px'}, e('style', {}, '.st0{fill:url(#paint);stroke:#123;stroke-width:2}.artwork{opacity:0.8}'), e('defs', {}, e('linearGradient', {id: 'paint'}, e('stop', {offset: 0, style: 'stop-color:red;stop-opacity:0.5'}))), e('path', {class: 'st0', d: 'M0 0L100 50', fill: '#456', stroke: '#789'}), e('script', {}, 'bad()')));
  const cleaned = cleanTree(original), output = cleaned.innerHTML;
  assert.match(output, /class="artwork" style="width: 200px; height: 100px;"/);
  assert.match(output, /svg\[data-editor-svg-scope="1"\]:is\(\.st0\),svg\[data-editor-svg-scope="1"\] \.st0\{fill: url\(#paint\); stroke: #123; stroke-width: 2;\}/);
  assert.match(output, /class="st0" d="M0 0L100 50" fill="#456" stroke="#789"/);
  assert.match(output, /style="stop-color: red; stop-opacity: 0.5;"/);
  assert.equal(output.includes('<script'), false);
  assert.equal(cleanTree(cleaned).innerHTML, output, 'repeated cleanup must keep the SVG appearance and scope stable');
});

test('the source-mode cleanup uses native replacement and snapshot recovery without replacing draft media state', async () => {
  const c = client(); await c.flush();
  const original = '<div style="text-align:center"><b>Draft</b><img src="blob:https://ganwen.test/kept"></div>', cleaned = '<p>Draft<img src="blob:https://ganwen.test/kept"></p>';
  c.context.original = original; c.context.cleaned = cleaned;
  c.run("setMode('source');$('sourceEditor').value=original;clearEditorFormattingHTML=()=>cleaned;inlineMediaState().objectURLs.add('blob:https://ganwen.test/kept')");
  const input = c.get('sourceEditor'), media = c.state.inlineMedia, calls = [];
  input.setSelectionRange = (start, end, direction) => Object.assign(input, {selectionStart: start, selectionEnd: end, selectionDirection: direction});
  c.context.document.execCommand = (name, _, value) => {calls.push({name, start: input.selectionStart, end: input.selectionEnd}); if (name === 'insertText') input.value = value; else if (name === 'undo') input.value = original; return true;};
  c.context.document.queryCommandEnabled = () => true;
  c.run('clearAllEditorFormatting()');
  assert.equal(input.value, cleaned); assert.equal(c.state.dirty, true); assert.equal(c.state.inlineMedia, media);
  assert.deepEqual(calls[0], {name: 'insertText', start: 0, end: original.length});
  c.run("command('undo')"); assert.equal(input.value, original); assert.equal(c.state.inlineMedia, media);
});

test('visual cleanup isolates document history and restores its snapshot only at the unchanged cleanup boundary', async () => {
  const c = client(); await c.flush();
  const original = c.run('getContent()'), initial = c.state.initialContent, generation = c.state.editorGeneration;
  const cleaned = '<p>Cleaned<img src="/saved.jpg" alt="kept"></p>';
  c.context.cleaned = cleaned;
  c.run("clearEditorFormattingHTML=()=>cleaned;inlineMediaState().objectURLs.add('blob:https://ganwen.test/retained')");
  const media = c.state.inlineMedia;
  c.run('clearAllEditorFormatting()');
  assert.equal(c.state.editorReady, false, 'the visual document must reload across cleanup history');
  assert.equal(c.state.editorGeneration, generation, 'pending media targets must remain valid');
  assert.equal(c.state.initialContent, initial, 'cleanup must not accept a draft as saved');
  assert.equal(c.state.originalRaw, original);
  c.get('visualEditor').onload();
  assert.equal(c.run('getContent()'), cleaned);
  assert.equal(c.run('canUndoEditorCleanup()'), true);
  c.body.mutate(cleaned + '<p>Later typing</p>'); c.body.emit('input', {inputType: 'insertText'});
  assert.equal(c.run('canUndoEditorCleanup()'), false);
  c.run('undoEditorCleanup()');
  assert.equal(c.body.peek(), cleaned + '<p>Later typing</p>', 'recovery cannot overwrite later edits');
  c.frameDocument.commandHTML = cleaned;
  c.run("command('undo')");
  assert.equal(c.run('canUndoEditorCleanup()'), true, 'native undo may return to the cleanup boundary');
  c.run("command('undo')");
  assert.equal(c.state.editorReady, false);
  c.get('visualEditor').onload();
  assert.equal(c.run('getContent()'), original);
  assert.equal(c.state.editorCleanupSnapshot, null);
  assert.equal(c.state.inlineMedia, media);
  assert.equal(c.state.initialContent, initial);
});

test('an upload finishing after cleanup updates both recovery snapshots without resurrecting a temporary source', async () => {
  const c = client(); await c.flush();
  const temporary = 'blob:https://ganwen.test/pending';
  c.context.original = '<div><b>Photo</b><img src="' + temporary + '"></div>';
  c.context.cleaned = '<p>Photo<img src="' + temporary + '"></p>';
  c.run("setMode('source');$('sourceEditor').value=original;clearEditorFormattingHTML=()=>cleaned");
  const input = c.get('sourceEditor');
  input.setSelectionRange = () => {};
  c.context.document.execCommand = (_, __, value) => {input.value = value; return true;};
  c.context.document.queryCommandEnabled = () => true;
  c.run('clearAllEditorFormatting()');
  c.context.temporary = temporary;
  c.run("applyInlineImageURL(inlineMediaTarget(),temporary,'/wp-content/uploads/complete.png')");
  assert.equal(c.run('canUndoEditorCleanup()'), true);
  c.run('undoEditorCleanup()');
  assert.equal(input.value, '<div><b>Photo</b><img src="/wp-content/uploads/complete.png"></div>');
});

test('cleanup and source undo respect the saving guard', async () => {
  const c = client(); await c.flush();
  c.run("setMode('source');state.saving=true"); let commands = 0;
  c.context.document.execCommand = () => {commands++; return true;};
  c.run("clearAllEditorFormatting();command('undo')"); assert.equal(commands, 0);
});

test('cleanup waits for manual media insertion instead of reloading its active selection document', async () => {
  const c = client(); await c.flush();
  c.run("state.manualMediaUploads=new Set([inlineMediaTarget()]);clearEditorFormattingHTML=()=>{throw Error('must not clean during upload')}");
  const before = c.run('getContent()');
  c.run('clearAllEditorFormatting()');
  assert.equal(c.state.editorReady, true);
  assert.equal(c.run('getContent()'), before);
  assert.equal(c.notices.length, 0);
});

test('saving legacy null taxonomy fields produces usable empty lists and accepts new terms', async () => {
  const c = client(); await c.flush();
  c.run('state.post.categories=null;state.post.tags=null');
  assert.deepEqual(JSON.parse(c.run('JSON.stringify(capture().categories)')), []);
  assert.deepEqual(JSON.parse(c.run('JSON.stringify(capture().tags)')), []);
  c.get('categories').value = '新分类'; c.get('tags').value = '新标签';
  assert.equal(c.run('capture().categories[0].name'), '新分类');
  assert.equal(c.run('capture().tags[0].name'), '新标签');
});
