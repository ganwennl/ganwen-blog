'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {performance} = require('node:perf_hooks');

const inputPath = process.env.ADMIN_PERFORMANCE_HTML || path.join(__dirname, '../internal/blog/admin_ui.html');
const html = fs.readFileSync(inputPath, 'utf8');
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)];
const source = scripts[1][1].trim().replace(/start\(\);$/, '');
const fixture = Array.from({length: 2400}, (_, i) => '<p data-row="' + i + '">' + '中文 paragraph &amp; persistent draft. '.repeat(16) + '</p>' + (i % 8 ? '' : '<img src="/wp-content/uploads/fixture-' + i + '.png" alt="saved image">')).join('\n');
const metricNames = ['htmlReads', 'htmlWrites', 'htmlParses', 'bodyScans', 'clones', 'stringify', 'toolbarScans', 'commandQueries'];

// A DOM contract double measures whole-document operations and event scheduling;
// it does not claim to reproduce browser layout, image decoding or native undo.
function client() {
  const metrics = Object.fromEntries(metricNames.map(name => [name, 0]));
  const timers = new Map(), frames = new Map(), events = {}, nodes = new Map(), calls = [], notices = [], attachments = [];
  let sequence = 0;
  class Node {
    constructor(tag = 'div') {
      Object.assign(this, {tagName: tag.toUpperCase(), value: '', checked: false, disabled: false, open: false, dataset: {}, style: {}, children: [], attributes: new Map(), handlers: {}, _html: '', _images: null});
      const classes = new Set();
      this.classList = {add: (...values) => values.forEach(value => classes.add(value)), remove: (...values) => values.forEach(value => classes.delete(value)), contains: value => classes.has(value), toggle: (value, force) => {const active = force ?? !classes.has(value); if (active) classes.add(value); else classes.delete(value); return active;}};
    }
    addEventListener(name, handler) {(this.handlers[name] ||= []).push(handler);}
    emit(name, event = {}) {for (const handler of this.handlers[name] || []) handler(event);}
    append(...nodes) {this.children.push(...nodes);}
    replaceChildren(...nodes) {this.children = nodes;}
    setAttribute(name, value) {this.attributes.set(name, String(value)); this[name] = String(value);}
    getAttribute(name) {return this.attributes.get(name) ?? null;}
    hasAttribute(name) {return this.attributes.has(name);}
    removeAttribute(name) {this.attributes.delete(name); delete this[name];}
    querySelector() {return null;}
    querySelectorAll() {return [];}
    focus() {}
    click() {return this.onclick?.();}
    close() {this.open = false;}
    showModal() {this.open = true;}
    contains() {return true;}
    setSelectionRange() {}
    get innerHTML() {if (this._html.length > 4096) metrics.htmlReads++; return this._html;}
    set innerHTML(value) {if (value.length > 4096) {metrics.htmlWrites++; metrics.htmlParses++;} this._html = value;}
  }
  class Body extends Node {
    constructor(markup = '') {super('body'); this._html = markup;}
    peek() {
      let result = this._html;
      for (const image of [...(this._images || [])].reverse()) result = result.slice(0, image.offset) + image.markup + result.slice(image.offset + image.originalLength);
      return result;
    }
    mutate(markup) {this._html = markup; this._images = null;}
    get innerHTML() {metrics.htmlReads++; return this.peek();}
    set innerHTML(value) {metrics.htmlWrites++; metrics.htmlParses++; this.mutate(value);}
    cloneNode() {metrics.clones++; return new Body(this.peek());}
    replaceChildren(...children) {
      if (children.length === 1 && children[0] instanceof Body) {
        const fragment = children[0];
        if (this.isConnected) attachments.push((fragment._images || []).filter(image => image.tagName === 'IMG').map(image => ({src: image.getAttribute('src'), loading: image.getAttribute('loading'), decoding: image.getAttribute('decoding')})));
        // Move the fragment's nodes rather than reparsing, preserving the node
        // identities used by runtime-only image-hint tracking in the real DOM.
        this._html = fragment._html;
        this._images = fragment._images;
        fragment._html = '';
        fragment._images = null;
        return;
      }
      super.replaceChildren(...children);
      if (!children.length) this.mutate('');
    }
    querySelectorAll(selector) {
      if (selector.startsWith('.ries-')) return [];
      metrics.bodyScans++;
      if (!this._images) this._images = [...this._html.matchAll(/<(?:img|source|video)\b[^>]*>/gi)].map(match => {
        const item = new Node(match[0].match(/^<(\w+)/)[1]);
        Object.assign(item, {offset: match.index, originalLength: match[0].length, markup: match[0]});
        const attribute = name => new RegExp('\\s' + name + '(?:\\s*=\\s*(?:"([^"]*)"|\'([^\']*)\'|([^\\s>]+)))?', 'i');
        item.hasAttribute = name => attribute(name).test(item.markup);
        item.getAttribute = name => {const found = item.markup.match(attribute(name)); return found ? found[1] ?? found[2] ?? found[3] ?? '' : null;};
        item.setAttribute = (name, value) => {const text = ' ' + name + '="' + String(value).replace(/"/g, '&quot;') + '"'; item.markup = item.hasAttribute(name) ? item.markup.replace(attribute(name), text) : item.markup.replace(/>$/, text + '>');};
        item.removeAttribute = name => {item.markup = item.markup.replace(attribute(name), '');};
        return item;
      });
      return this._images.filter(item => selector.split(',').some(part => {
        const tag = part.trim().match(/^([a-z]+)/i)?.[1];
        if (tag && item.tagName !== tag.toUpperCase()) return false;
        return [...part.matchAll(/\[([^\]=]+)(?:=[^\]]+)?\]/g)].every(match => item.hasAttribute(match[1]));
      }));
    }
  }
  const get = id => {if (!nodes.has(id)) nodes.set(id, new Node()); return nodes.get(id);};
  const body = new Body(), paragraph = new Node('p');
  body.isConnected = true;
  paragraph.parentElement = body;
  const range = {commonAncestorContainer: paragraph, startContainer: paragraph, collapsed: true, cloneRange() {return {...this};}, selectNodeContents() {}, collapse() {}};
  const selection = {rangeCount: 1, anchorNode: paragraph, getRangeAt: () => range, removeAllRanges() {}, addRange() {}};
  const buttons = ['undo', 'redo', 'bold', 'italic', 'justifyLeft', 'justifyCenter', 'justifyRight'].map(command => {const node = new Node('button'); node.dataset.command = command; return node;});
  const makeElement = tag => {if (tag === 'template') {const content = new Body(); return {content, get innerHTML() {return content.innerHTML;}, set innerHTML(value) {content.innerHTML = value;}};} return new Node(tag);};
  const frameDocument = {body, documentElement: {dataset: {}}, head: new Node('head'), createElement: makeElement, getElementById: () => null, addEventListener: (name, handler) => events['frame:' + name] = handler, getSelection: () => selection, createRange: () => range,
    queryCommandEnabled: () => {metrics.commandQueries++; return true;}, queryCommandState: () => {metrics.commandQueries++; return false;}, execCommand: command => {if (frameDocument.commandHTML !== undefined) body.mutate(frameDocument.commandHTML); body.emit('input', {inputType: command === 'redo' ? 'historyRedo' : 'historyUndo'}); return true;}};
  paragraph.ownerDocument = frameDocument;
  get('visualEditor').contentDocument = frameDocument;
  get('previewFrame').contentDocument = {documentElement: {dataset: {}}};
  const editorInputs = ['postTitle', 'sourceEditor', 'excerpt', 'seoTitle', 'seoDescription', 'postStatus', 'categories', 'tags', 'featuredImage', 'sticky', 'translationReviewed'].map(get);
  const document = {documentElement: {dataset: {theme: 'light'}}, getElementById: get, createElement: makeElement, createElementNS: (_, tag) => makeElement(tag), querySelector: () => new Node(), querySelectorAll: selector => {
    if (selector === '.toolbar button') {metrics.toolbarScans++; return buttons;}
    if (selector === '[data-command]') return buttons;
    if (selector.includes('#view-editor')) return editorInputs;
    return [];
  }, addEventListener: (name, handler) => events[name] = handler};
  const window = {parent: {postMessage() {}}, confirm: () => true, prompt: () => null, addEventListener: (name, handler) => events[name] = handler};
  const setTimer = (callback, delay = 0) => {const id = ++sequence; timers.set(id, {callback, delay}); return id;};
  const context = {document, window, location: {origin: 'https://ganwen.test', href: 'https://ganwen.test/admin/?embed=1&edit=4&editor_session=performance-instance', search: '?embed=1&edit=4&editor_session=performance-instance'}, history: {replaceState() {}}, localStorage: {setItem() {}}, matchMedia: () => ({matches: false, addEventListener() {}}),
    setTimeout: setTimer, clearTimeout: id => timers.delete(id), requestAnimationFrame: callback => {const id = ++sequence; frames.set(id, callback); return id;}, cancelAnimationFrame: id => frames.delete(id),
    ResizeObserver: class {observe() {}}, getComputedStyle: () => ({paddingLeft: '0px', paddingRight: '0px'}), FormData, Blob, URL, URLSearchParams, TextEncoder, atob, console,
    JSON: {parse: JSON.parse, stringify: (...args) => {metrics.stringify++; return JSON.stringify(...args);}}, fetch: async () => {throw Error('unexpected network request');}};
  vm.createContext(context);
  const run = text => vm.runInContext(text, context);
  run(source);
  const state = run('state');
  Object.assign(state, {authenticated: true, user: 'editor', view: 'editor', lang: 'zh', mode: 'visual', post: {id: 4, type: 'post', version: 3, title: 'Long article', content_html: fixture, translations: {}, categories: [], tags: [], seo: {}}, settingsDirty: false});
  get('postTitle').value = 'Long article';
  get('postStatus').value = 'draft';
  context.fixture = fixture;
  run('setContent(fixture)');
  get('visualEditor').onload();
  const reset = () => {for (const key of metricNames) metrics[key] = 0;};
  async function flush() {
    for (let cycle = 0; cycle < 10; cycle++) {
      const pendingTimers = [...timers.entries()].filter(([, item]) => item.delay < 1000);
      const pendingFrames = [...frames.entries()];
      if (!pendingTimers.length && !pendingFrames.length) break;
      for (const [id, item] of pendingTimers) {timers.delete(id); await item.callback();}
      for (const [id, callback] of pendingFrames) {frames.delete(id); await callback();}
      await Promise.resolve();
    }
  }
  async function paint() {
    const pendingFrames = [...frames.entries()];
    for (const [id, callback] of pendingFrames) {frames.delete(id); await callback();}
    await Promise.resolve();
  }
  return {metrics, reset, run, state, context, get, body, frameDocument, events, calls, notices, attachments, flush, paint};
}

function assertNoWholeDocumentWork(metrics) {
  for (const name of ['htmlReads', 'htmlWrites', 'htmlParses', 'bodyScans', 'clones', 'stringify']) assert.equal(metrics[name], 0, name + ' must not run per keystroke/caret movement');
}

async function measure() {
  const c = client();
  await c.flush(); c.reset();
  const start = performance.now();
  for (let i = 0; i < 100; i++) {c.body.mutate(fixture + '<p>typed ' + i + '</p>'); c.body.emit('input', {inputType: 'insertText'});}
  const synchronous = {...c.metrics};
  await c.flush();
  const afterIdle = {...c.metrics};
  const elapsedMs = performance.now() - start;
  c.reset();
  for (let i = 0; i < 100; i++) c.events['frame:selectionchange']();
  const selectionSynchronous = {...c.metrics};
  await c.flush();
  return {fixtureBytes: Buffer.byteLength(fixture), paragraphs: 2400, images: 300, inputEvents: 100, synchronous, afterIdle, selectionSynchronous, selectionAfterIdle: {...c.metrics}, elapsedMs};
}

if (process.argv.includes('--report')) {
  measure().then(report => console.log(JSON.stringify(report, null, 2))).catch(error => {console.error(error); process.exitCode = 1;});
} else {
  test('long article input bursts avoid whole-document work and coalesce toolbar updates', async () => {
    const report = await measure();
    assertNoWholeDocumentWork(report.synchronous);
    assertNoWholeDocumentWork(report.afterIdle);
    assertNoWholeDocumentWork(report.selectionSynchronous);
    assertNoWholeDocumentWork(report.selectionAfterIdle);
    assert.ok(report.selectionAfterIdle.commandQueries <= 7, 'caret burst should update toolbar state once');
  });

  test('continuous typing across paint frames defers native layout queries until a pause', async () => {
    const c = client();
    await c.flush(); c.reset();
    for (let index = 0; index < 50; index++) {
      c.body.emit('input', {inputType: 'insertText'});
      c.events['frame:selectionchange']();
      await c.paint();
      assert.equal(c.metrics.commandQueries, 0, 'typing must not force native command layout on a paint frame');
    }
    assertNoWholeDocumentWork(c.metrics);
    await c.flush();
    assert.equal(c.metrics.commandQueries, 7, 'the toolbar must still update after typing pauses');
    c.reset();
    c.body.emit('input', {inputType: 'insertText'});
    c.state.embed = false;
    await c.run("navigateAdmin('posts',false)");
    await c.flush();
    assert.equal(c.metrics.commandQueries, 0, 'a deferred toolbar must not query a discarded editor');
  });

  test('injected helper mutations leave the saved draft clean while genuine edits remain observable', async () => {
    const c = client();
    await c.flush(); c.reset();
    const helper = new Object(), inside = new Object(), paragraph = new Object();
    Object.assign(helper, {nodeType: 1, tagName: 'DIV', children: [], closest: () => helper});
    Object.assign(inside, {nodeType: 1, tagName: 'IMG', children: [], closest: () => helper});
    Object.assign(paragraph, {nodeType: 1, tagName: 'P', children: [], closest: () => null});
    Object.assign(c.context, {helper, inside, paragraph});
    const generation = c.state.editorGeneration, revision = c.state.visualRevision;
    c.run('handleEditorMutations([{type:"childList",target:document.getElementById("visualEditor").contentDocument.body,addedNodes:[helper],removedNodes:[]},{type:"attributes",target:inside}],state.editorGeneration)');
    assert.equal(c.state.visualRevision, revision);
    assert.equal(c.state.dirty, false);
    assert.equal(c.state.inlineMedia, null, 'helper icons must not be uploaded as article images');
    assertNoWholeDocumentWork(c.metrics);
    c.run('handleEditorMutations([{type:"childList",target:document.getElementById("visualEditor").contentDocument.body,addedNodes:[helper,paragraph],removedNodes:[]}],state.editorGeneration)');
    assert.equal(c.state.editorGeneration, generation);
    assert.equal(c.state.visualRevision, revision + 1);
    assert.equal(c.state.dirty, true, 'a helper in the same batch must not hide a real article edit');
  });

  test('saved images receive lazy decoding hints before entering the live editor DOM', async () => {
    const c = client();
    assert.equal(c.attachments.length, 1, 'initial content should be mounted from an offline fragment');
    assert.equal(c.attachments[0].length, 300);
    assert.ok(c.attachments[0].every(image => image.loading === 'lazy' && image.decoding === 'async'), 'all persisted images must be deferred before the connected DOM sees them');
    assert.equal(c.run('getContent()'), fixture, 'runtime image hints must not change stored HTML');
    c.run("setMode('source')");
    c.get('sourceEditor').value += '<img src="/wp-content/uploads/new-source-image.png">';
    c.get('sourceEditor').emit('input');
    const sourceEdit = c.get('sourceEditor').value;
    c.run("setMode('visual')");
    assert.equal(c.attachments.length, 2);
    assert.equal(c.attachments[1].length, 301);
    assert.ok(c.attachments[1].every(image => image.loading === 'lazy' && image.decoding === 'async'));
    assert.equal(c.run('capture().content_html'), sourceEdit);
  });

  test('switching modes and capturing immediately use the latest edit before idle callbacks', async () => {
    const c = client();
    await c.flush();
    const latest = fixture + '<p>latest typing before switch</p>';
    c.body.mutate(latest); c.body.emit('input', {inputType: 'insertText'});
    c.run("setMode('source')");
    assert.equal(c.get('sourceEditor').value, latest);
    c.get('sourceEditor').value += '<p>source-only final edit</p>';
    c.get('sourceEditor').emit('input');
    const sourceEdit = c.get('sourceEditor').value;
    c.run("setMode('visual')");
    assert.equal(c.run('capture().content_html'), sourceEdit);
    await c.flush();
    assert.equal(c.run('capture().content_html'), sourceEdit);
  });

  test('source edits before iframe load remain the visual draft when undo returns to that content', async () => {
    const c = client();
    c.state.mode = 'source';
    c.run("setContent('<p>Original</p>')");
    const beforeLoad = '<p>Original</p><p>source edit before iframe load</p>';
    c.get('sourceEditor').value = beforeLoad;
    c.get('sourceEditor').emit('input');
    c.run("setMode('visual')");
    c.get('visualEditor').onload();
    c.run("setMode('source')");
    c.get('sourceEditor').value = beforeLoad + '<p>later source edit</p>';
    c.get('sourceEditor').emit('input');
    c.run("setMode('visual')");
    c.body.mutate(beforeLoad);
    c.body.emit('input', {inputType: 'historyUndo'});
    assert.equal(c.run('capture().content_html'), beforeLoad);
    assert.equal(c.state.dirty, true);
  });

  test('pending manual media warns on unload and close and accepted discard ignores the late response', async () => {
    const c = client();
    await c.flush();
    const originalMounted = c.body.peek();
    let finish;
    c.context.pendingUpload = new Promise(resolve => finish = resolve);
    c.run('api=async()=>pendingUpload');
    c.run("chooseMedia('image')");
    c.get('mediaFile').files = [new Blob(['image bytes'], {type: 'image/png'})];
    const upload = c.get('mediaFile').onchange();
    assert.equal(c.state.dirty, false);
    let warned = false, confirmations = 0;
    c.events.beforeunload({preventDefault() {warned = true;}});
    assert.equal(warned, true);
    c.context.window.confirm = () => {confirmations++; return false;};
    assert.equal(c.run('closeEditor()'), false);
    assert.equal(confirmations, 1);
    c.context.window.confirm = () => {confirmations++; return true;};
    assert.equal(c.run('closeEditor()'), true);
    assert.equal(confirmations, 2);
    finish({url: '/wp-content/uploads/late-manual.png', type: 'image/png', name: 'late.png'});
    await upload;
    assert.equal(c.body.peek(), originalMounted);
    assert.equal(c.state.dirty, false);
    warned = false;
    c.events.beforeunload({preventDefault() {warned = true;}});
    assert.equal(warned, false);
  });

  test('title and metadata typing never serializes the unchanged article body', async () => {
    const c = client();
    await c.flush(); c.reset();
    for (let i = 0; i < 50; i++) {
      c.get('postTitle').value = 'Updated title ' + i;
      c.get('postTitle').emit('input');
      c.get('excerpt').value = 'Updated summary ' + i;
      c.get('excerpt').emit('input');
    }
    assertNoWholeDocumentWork(c.metrics);
    await c.flush();
    assertNoWholeDocumentWork(c.metrics);
    assert.equal(c.state.dirty, true);
    assert.equal(c.run('capture().title'), 'Updated title 49');
  });

  test('preview synchronizes a just-edited document without waiting for idle work', async () => {
    const c = client();
    await c.flush();
    const latest = fixture + '<p>preview before any scheduled callback</p>';
    c.body.mutate(latest); c.body.emit('input', {inputType: 'insertText'});
    c.get('previewButton').onclick();
    assert.ok(c.get('previewFrame').srcdoc.includes(latest));
    assert.equal(c.get('previewDialog').open, true);
    assert.equal(c.state.dirty, true);
  });

  test('a completed image upload invalidates a previously cached visual draft', async () => {
    const c = client();
    await c.flush();
    const temporary = 'blob:https://ganwen.test/pending-performance-image';
    const uploaded = '/wp-content/uploads/2026/10/performance-image.png';
    c.body.mutate(fixture + '<img src="' + temporary + '">');
    c.body.emit('input', {inputType: 'insertText'});
    assert.ok(c.run('getContent()').includes(temporary));
    c.context.temporaryImage = temporary;
    c.context.uploadedImage = uploaded;
    c.run('applyInlineImageURL(inlineMediaTarget(),temporaryImage,uploadedImage)');
    const updated = c.run('getContent()');
    assert.ok(updated.includes(uploaded));
    assert.equal(updated.includes(temporary), false);
    assert.equal(c.state.dirty, true);
  });

  test('saving immediately captures current content once and never drops a failed draft', async () => {
    const c = client();
    await c.flush();
    const latest = fixture + '<p>must reach the save API</p>';
    c.body.mutate(latest); c.body.emit('input', {inputType: 'insertText'});
    c.context.savedCalls = [];
    c.run("api=async(path,payload)=>{savedCalls.push({path,payload});throw Error('save intentionally rejected')}");
    await c.run('saveEditor()');
    assert.equal(c.context.savedCalls.length, 1);
    assert.equal(c.context.savedCalls[0].payload.content_html, latest);
    assert.equal(c.state.dirty, true);
    assert.equal(c.run('getContent()'), latest);
    assert.equal(c.get('editorGrid').inert, false);
    c.context.window.confirm = () => false;
    assert.equal(c.run('canLeave()'), false);
  });

  for (const outcome of ['success', 'failure']) {
    test('visual editing stays locked through a held save and delayed iframe load then recovers after ' + outcome, async () => {
      const c = client();
      await c.flush();
      const latest = fixture + '<p>held-save draft</p>';
      c.body.mutate(latest); c.body.emit('input', {inputType: 'insertText'});
      c.context.heldSave = new Promise((resolve, reject) => {c.context.completeSave = resolve; c.context.failSave = reject;});
      c.context.savedCalls = [];
      c.run('api=async(path,payload)=>{savedCalls.push({path,payload});return heldSave}');
      const save = c.run('saveEditor()');
      await Promise.resolve(); await Promise.resolve();
      assert.equal(c.state.saving, true);
      assert.equal(c.body.contentEditable, 'false', 'outer inert must be supplemented by a native iframe editing lock');
      assert.equal(c.get('savePostButton').textContent, '正在保存…');
      let prevented = false;
      c.body.emit('beforeinput', {inputType: 'insertText', preventDefault() {prevented = true;}});
      assert.equal(prevented, true);
      prevented = false;
      c.body.emit('paste', {preventDefault() {prevented = true;}});
      assert.equal(prevented, true);
      c.frameDocument.commandHTML = latest + '<p>must not enter while saving</p>';
      c.run("command('insertHTML','blocked')");
      assert.equal(c.run('getContent()'), latest);
      // Exercise an iframe that only becomes available after the save started.
      c.state.raw = latest;
      c.state.editorReady = false;
      c.get('visualEditor').onload();
      assert.equal(c.body.contentEditable, 'false');
      assert.equal(c.context.savedCalls.length, 1);
      const payload = c.context.savedCalls[0].payload;
      if (outcome === 'success') c.context.completeSave({post: {...payload, version: 4}, source_hash: 'saved-hash', public_languages: []});
      else c.context.failSave(Error('held save failed'));
      await save;
      assert.equal(c.state.saving, false);
      assert.match(c.get('visualEditor').srcdoc, /<body contenteditable="false"/, 'initial iframe markup waits for the controlled mount');
      assert.equal(c.body.contentEditable, 'true');
      assert.equal(c.get('editorGrid').inert, false);
      assert.equal(c.get('savePostButton').disabled, false);
      assert.equal(c.get('savePostButton').textContent, '保存文章');
      assert.equal(c.run('getContent()'), latest);
      assert.equal(c.state.dirty, outcome === 'failure');
    });
  }

  for (const mode of ['visual', 'source']) {
    test('successful ' + mode + ' saving preserves the current DOM, caret, scrolling and subsequent draft', async () => {
      const c = client();
      await c.flush();
      const latest = fixture + '<p>first edit saved in place</p>';
      let focused = 0;
      if (mode === 'source') {
        c.run("setMode('source')");
        Object.assign(c.get('sourceEditor'), {value: latest, selectionStart: 17, selectionEnd: 23, selectionDirection: 'backward', scrollTop: 140, scrollLeft: 20});
        c.get('sourceEditor').focus = () => {focused++; c.get('sourceEditor').scrollTop = 0;};
        c.get('sourceEditor').setSelectionRange = (start, end, direction) => Object.assign(c.get('sourceEditor'), {selectionStart: start, selectionEnd: end, selectionDirection: direction});
        c.get('sourceEditor').emit('input');
      } else {
        c.body.mutate(latest); c.body.emit('input', {inputType: 'insertText'});
        c.frameDocument.defaultView = {scrollX: 20, scrollY: 140, scrollTo(x, y) {Object.assign(this, {scrollX: x, scrollY: y});}};
        c.body.focus = () => {focused++; c.frameDocument.defaultView.scrollY = 0;};
      }
      const generation = c.state.editorGeneration, srcdoc = c.get('visualEditor').srcdoc, body = c.body;
      c.context.savedCalls = [];
      c.run("api=async(path,payload)=>{savedCalls.push({path,payload});return {post:{...payload,version:4},source_hash:'saved-hash',public_languages:[]}}");
      await c.run('saveEditor()');
      assert.equal(c.state.editorGeneration, generation, 'saving must not replace the existing editing document');
      assert.equal(c.get('visualEditor').srcdoc, srcdoc);
      assert.equal(c.get('visualEditor').contentDocument.body, body);
      assert.equal(c.run('getContent()'), latest);
      assert.equal(c.state.dirty, false);
      assert.equal(focused, 1);
      if (mode === 'source') {
        assert.equal(c.get('sourceEditor').selectionStart, 17);
        assert.equal(c.get('sourceEditor').selectionEnd, 23);
        assert.equal(c.get('sourceEditor').selectionDirection, 'backward');
        assert.equal(c.get('sourceEditor').scrollTop, 140);
        assert.equal(c.get('sourceEditor').scrollLeft, 20);
        c.get('sourceEditor').value += '<p>second edit after save</p>';
        c.get('sourceEditor').emit('input');
      } else {
        assert.equal(c.frameDocument.defaultView.scrollX, 20);
        assert.equal(c.frameDocument.defaultView.scrollY, 140);
        c.body.mutate(latest + '<p>second edit after save</p>'); c.body.emit('input', {inputType: 'insertText'});
      }
      assert.equal(c.run('capture().content_html'), latest + '<p>second edit after save</p>');
      assert.equal(c.state.dirty, true);
      assert.equal(c.context.savedCalls.length, 1);
    });
  }

  for (const missing of ['contentDocument', 'body']) {
    for (const outcome of ['success', 'failure']) {
      test('save cleanup handles missing iframe ' + missing + ' after ' + outcome, async () => {
        const c = client();
        c.state.editorReady = false;
        if (missing === 'contentDocument') c.get('visualEditor').contentDocument = undefined;
        else c.get('visualEditor').contentDocument.body = undefined;
        c.run(outcome === 'success'
          ? "api=async(path,payload)=>({post:{...payload,version:4},source_hash:'saved-hash',public_languages:[]})"
          : "api=async()=>{throw Error('intentional save failure')}");
        await assert.doesNotReject(c.run('saveEditor()'));
        assert.equal(c.state.saving, false);
        assert.equal(c.get('editorGrid').inert, false);
        assert.equal(c.get('savePostButton').disabled, false);
        assert.equal(c.run('getContent()'), fixture);
      });
    }
  }

  test('confirmed editor discard releases temporary image resources while a cancelled exit retains undo and retry bytes', async () => {
    const c = client();
    c.state.embed = false;
    const revoked = [];
    c.context.URL = class extends URL {static revokeObjectURL(source) {revoked.push(source);}};
    c.run("const media=inlineMediaState();media.objectURLs.add('blob:https://ganwen.test/undo-image');media.files.set('blob:https://ganwen.test/undo-image',new Blob(['original undo and retry bytes'],{type:'image/png'}));media.uploads.set('blob:https://ganwen.test/undo-image',Promise.resolve('/wp-content/uploads/persisted.png'));");
    c.get('postTitle').value = 'Unsaved title';
    c.get('postTitle').emit('input');
    c.context.window.confirm = () => false;
    await c.run("navigateAdmin('posts')");
    assert.equal(c.state.view, 'editor');
    assert.equal(c.state.inlineMedia.files.size, 1);
    assert.deepEqual(revoked, []);
    c.context.window.confirm = () => true;
    await c.run("navigateAdmin('posts')");
    assert.equal(c.state.view, 'posts');
    assert.equal(c.state.inlineMedia, null);
    assert.deepEqual(revoked, ['blob:https://ganwen.test/undo-image']);
  });

  test('undo to the saved content clears dirty state at the leave boundary without losing later edits', async () => {
    const c = client();
    await c.flush();
    c.body.mutate(fixture + '<p>unsaved addition</p>'); c.body.emit('input', {inputType: 'insertText'});
    let confirmations = 0;
    c.context.window.confirm = () => {confirmations++; return false;};
    assert.equal(c.run('canLeave()'), false);
    assert.equal(confirmations, 1);
    c.frameDocument.commandHTML = fixture;
    c.run("command('undo')");
    assert.equal(c.run('canLeave()'), true);
    assert.equal(confirmations, 1);
    c.frameDocument.commandHTML = fixture + '<p>unsaved addition</p>';
    c.run("command('redo')");
    await c.flush();
    assert.equal(c.state.dirty, true);
    assert.equal(c.run('canLeave()'), false);
    assert.equal(confirmations, 2);
    c.frameDocument.commandHTML = fixture;
    c.run("command('undo')");
    await c.flush();
    assert.equal(c.state.dirty, false);
    c.body.mutate(fixture + '<p>new edit after undo</p>'); c.body.emit('input', {inputType: 'insertText'});
    let prevented = false;
    c.events.beforeunload({preventDefault() {prevented = true;}});
    assert.equal(prevented, true);
    assert.equal(c.state.dirty, true);
  });

  test('confirmed navigation discards the old article without reviving its unload warning', async () => {
    const c = client();
    await c.flush();
    c.state.embed = false;
    c.get('postTitle').value = 'Discard this edited title';
    c.get('postTitle').emit('input');
    let confirmations = 0;
    c.context.window.confirm = () => {confirmations++; return true;};
    await c.run("navigateAdmin('posts')");
    assert.equal(confirmations, 1);
    assert.equal(c.state.view, 'posts');
    assert.equal(c.state.dirty, false);
    let warned = false;
    c.events.beforeunload({preventDefault() {warned = true;}});
    assert.equal(warned, false, 'confirmed discarded edits must not revive an unload warning');
    assert.equal(c.state.dirty, false);
    await c.flush();
    assert.equal(c.state.dirty, false);
    assert.equal(c.run('canLeave()'), true);
    assert.equal(confirmations, 1);
  });

  test('a queued undo reconciliation cannot mark a discarded article dirty from the posts page', async () => {
    const c = client();
    await c.flush();
    c.state.embed = false;
    c.body.mutate(fixture + '<p>pending old undo state</p>');
    c.body.emit('input', {inputType: 'historyUndo'});
    // This route is used after the caller has already approved leaving.
    await c.run("navigateAdmin('posts',false)");
    assert.equal(c.state.view, 'posts');
    assert.equal(c.state.dirty, false);
    await c.flush();
    assert.equal(c.state.dirty, false, 'old scheduled reconciliation must not revive discarded content');
    let warned = false;
    c.events.beforeunload({preventDefault() {warned = true;}});
    assert.equal(warned, false);
    assert.equal(c.state.dirty, false);
  });

  test('confirmed embedded-editor close permits the parent to unload the discarded iframe', async () => {
    const c = client();
    await c.flush();
    c.get('postTitle').value = 'Discard this embedded title';
    c.get('postTitle').emit('input');
    let confirmations = 0;
    c.context.window.confirm = () => {confirmations++; return true;};
    assert.equal(c.run('closeEditor()'), true);
    assert.equal(confirmations, 1);
    assert.equal(c.state.dirty, false);
    // The embedding page removes the iframe; its child view can still be editor.
    let warned = false;
    c.events.beforeunload({preventDefault() {warned = true;}});
    assert.equal(warned, false, 'accepted iframe close must not trigger a second discard prompt');
    assert.equal(c.state.dirty, false);
    await c.flush();
    c.events.beforeunload({preventDefault() {warned = true;}});
    assert.equal(warned, false);
    assert.equal(c.state.dirty, false);
    assert.equal(confirmations, 1);
  });
}
