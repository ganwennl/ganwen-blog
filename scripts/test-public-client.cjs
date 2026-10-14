'use strict';
// Isolated browser contract tests: no HTTP requests, production events or cookies.
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../internal/blog/public.js'), 'utf8');
const themeSource = fs.readFileSync(path.join(__dirname, '../internal/blog/public-theme.js'), 'utf8');
const languages = ['zh', 'nl', 'en', 'hi', 'es', 'ar', 'fr', 'bn', 'pt', 'id', 'ur'];
let sequence = 0;
class Element {
  constructor(dataset = {}) { this.dataset = dataset; this.tagName = 'DIV'; this.id = ''; this.textContent = ''; this.children = []; this.hidden = true; this.listeners = {}; this.attributes = {}; this.open = false; this.clicks = 0; this.classes = new Set(); this.classList = {add: name => this.classes.add(name), remove: name => this.classes.delete(name), toggle: () => {}}; }
  addEventListener(type, callback) { (this.listeners[type] ??= []).push(callback); }
  emit(type, event = {}) { event.target ??= this; for (const callback of this.listeners[type] || []) callback(event); }
  setAttribute(name, value) { this.attributes[name] = value; }
  getAttribute(name) { return this.attributes[name] ?? null; }
  removeAttribute(name) { delete this.attributes[name]; if (name === 'src') delete this.src; }
  click() { this.clicks++; this.emit('click', {button: 0}); }
  focus() { this.focused = true; if (this.ownerDocument) this.ownerDocument.activeElement = this; }
  showModal() { this.open = true; }
  close() { this.open = false; this.emit('close'); }
  matches(selector) { return selector.split(',').some(value => {
    value = value.trim();
    if (value === '[data-language-picker]') return Boolean(this.isLanguagePicker);
    if (value === '.intro-contacts') return this.classes.has('intro-contacts');
    if (value === '[contenteditable]:not([contenteditable="false"])') return this.attributes.contenteditable !== undefined && this.attributes.contenteditable !== 'false';
    const role = /^\[role="([^"]+)"\]$/.exec(value);
    if (role) return this.attributes.role === role[1];
    if (value === 'a[href]') return this.tagName === 'A' && Boolean(this.href);
    return this.tagName.toLowerCase() === value;
  }); }
  contains(target) { for (let current = target; current; current = current.parentElement) if (current === this) return true; return false; }
  closest(selector) { for (let current = this; current; current = current.parentElement) if (current.matches(selector)) return current; return null; }
  querySelector(selector) { return this.children.find(child => child.matches(selector)) || null; }
  append(...children) { for (const child of children) { child.parentElement = this; this.children.push(child); } }
}
function storage() { const values = new Map(); return {getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key), values}; }
async function browser(options = {}) {
  const listeners = {};
  const calls = [];
  const beacons = [];
  const messages = [];
  const scrolls = [];
  const timeouts = [];
  const location = new URL(options.url || 'https://www.ganwen.nl/?p=1447');
  location.reload = () => { location.reloads = (location.reloads || 0) + 1; };
  location.assign = value => { location.assigned = value; };
  const main = new Element({publicLanguage: options.language || 'zh', postId: String(options.id ?? 1447)});
  if (options.independent) main.dataset.independentPage = 'true';
  const edit = new Element(); const dialog = new Element(); const frame = new Element(); const close = new Element(); const admin = new Element(); const back = new Element();
  const edits = options.editIDs ? options.editIDs.map(id => new Element({editPostId: String(id)})) : [edit];
  back.setAttribute('href', (main.dataset.publicLanguage === 'zh' ? '/' : '/' + main.dataset.publicLanguage + '/') + '?paged=1');
  if (options.independent) back.setAttribute('href', (main.dataset.publicLanguage === 'zh' ? '/' : '/' + main.dataset.publicLanguage + '/'));
  const search = new Element({searchOpen: String(Boolean(options.searchOpen))}); search.hidden = false;
  const searchToggle = new Element(); searchToggle.tagName = 'BUTTON';
  const input = new Element(); input.tagName = 'INPUT'; input.parentElement = search;
  const help = new Element(); const helpSummary = new Element(); helpSummary.tagName = 'SUMMARY'; help.append(helpSummary);
  const layout = new Element(); layout.tagName = 'SELECT'; layout.value = 'journal';
  const toc = new Element(); const tocLinks = new Element(); const next = new Element(); const previous = new Element();
  const introContacts = options.contacts === false ? null : new Element();
  if (introContacts) { introContacts.tagName = 'NAV'; introContacts.classes.add('intro-contacts'); introContacts.hidden = false; }
  const contactLinks = introContacts ? ['https://example.com/', 'mailto:editor@example.com'].map(href => {
    const link = new Element(); link.tagName = 'A'; link.href = href; link.hidden = false; return link;
  }) : [];
  const contactLinksGroup = new Element(); contactLinksGroup.classes.add('contact-links'); contactLinksGroup.hidden = false;
  contactLinksGroup.append(...contactLinks);
  introContacts?.append(contactLinksGroup);
  const picker = new Element(); picker.isLanguagePicker = true;
  const summary = new Element(); summary.parentElement = picker;
  const languageLinks = (options.languageLinks || languages).map(language => {
    const link = new Element(); link.parentElement = picker;
    link.setAttribute('lang', language); link.href = location.origin + (language === 'zh' ? '/' : '/' + language + '/') + '?cat=13&tag=notes&paged=2';
    if (language === main.dataset.publicLanguage) link.setAttribute('aria-current', 'true');
    return link;
  });
  picker.querySelector = selector => selector === 'summary' ? summary : null;
  picker.querySelectorAll = selector => selector === '.language-links a[lang]' ? languageLinks : [];
  frame.contentWindow = {postMessage: (message, origin) => messages.push({message, origin})};
  const elements = {'[data-public-language]': main, '[data-language-picker]': picker, '.intro-contacts': introContacts, '[data-article-edit]': edit, '[data-public-editor]': dialog, '[data-public-editor-frame]': frame, '[data-article-editor-close]': close, '[data-back-link]': back, '.article-card': options.article === false ? null : new Element(), '.search-toggle': searchToggle, '[data-shortcuts]': help, '[data-layout-picker]': layout, '[data-next]': next, '[data-previous]': previous, '[data-article-toc]': options.headings ? toc : null, '[data-toc-links]': options.headings ? tocLinks : null};
  elements['[data-independent-page]'] = options.independent ? main : null;
  const document = {readyState: 'complete', referrer: options.referrer || '', documentElement: new Element({supportedLanguages: languages.join(' ')}), body: new Element(), activeElement: null,
    querySelector: selector => elements[selector] || null,
    querySelectorAll: selector => selector === '[data-article-edit]' ? edits : selector === '[data-admin-link]' ? [admin] : selector === '[data-back-link]' ? [back] : selector === '.language-links a[lang]' ? languageLinks : selector === '.article-body h2, .article-body h3' ? options.headings || [] : selector === '[id]' ? [...options.headings || [], ...options.idElements || []].filter(element => element.id) : [],
    getElementById: id => id === 'site-search' ? search : id === 'search-text' ? input : null,
    createElement: tagName => { const element = new Element(); element.tagName = tagName.toUpperCase(); return element; },
    addEventListener: (type, callback) => { (listeners[type] ??= []).push(callback); }, cookie: ''};
  for (const element of [summary, ...languageLinks, ...contactLinks, input, searchToggle, helpSummary, layout, ...edits]) element.ownerDocument = document;
  const sessionStorage = options.sessionStorage || storage();
  const navigator = {doNotTrack: options.dnt, globalPrivacyControl: options.gpc,
    sendBeacon: (url, body) => { beacons.push({url, body}); return options.beacon !== false; }};
  const context = {document, location, navigator, URL, URLSearchParams, Blob, Uint8Array, AbortController, Date,
    crypto: options.noCrypto ? undefined : {randomUUID: () => '00000000-0000-4000-8000-' + String(++sequence).padStart(12, '0')},
    sessionStorage, localStorage: storage(), matchMedia: () => ({matches: false, addEventListener() {}}),
    addEventListener: (type, callback) => { (listeners[type] ??= []).push(callback); },
    getSelection: () => ({toString: () => ''}), scrollTo: position => scrolls.push(position), scrollY: options.scrollY || 0, history: {length: 1}, requestAnimationFrame: callback => callback(),
    setTimeout: (callback, milliseconds) => {timeouts.push({callback, milliseconds, active: true}); return timeouts.length;},
    clearTimeout: id => {if (timeouts[id - 1]) timeouts[id - 1].active = false;},
    fetch: async (url, settings) => {
      calls.push({url, settings});
      if (url === '/admin/api/editor-session') {
        if (options.probeError) throw Error('isolated failure');
        if (options.probeResponse) return options.probeResponse();
        return {ok: options.probeOK !== false, json: async () => options.probeBody || {authenticated: Boolean(options.auth)}};
      }
      if (settings?.method === 'HEAD') {
        if (options.refreshResponse) return options.refreshResponse(url, settings);
        return {ok: options.refreshStatus !== 404, status: options.refreshStatus || 200};
      }
      return {ok: true};
    }};
  context.window = context;
  vm.runInNewContext(themeSource, context, {filename: 'public-theme.js'});
  vm.runInNewContext(source, context, {filename: 'public.js'});
  await new Promise(resolve => setImmediate(resolve));
  return {location, document, main, picker, summary, languageLinks, introContacts, contactLinks, edit, edits, dialog, frame, close, admin, back, calls, beacons, messages, sessionStorage, search, searchToggle, input, help, helpSummary, layout, toc, tocLinks, next, previous, scrolls, timeouts,
    emit: (type, event = {}) => { for (const callback of listeners[type] || []) callback(event); },
    payload: async () => JSON.parse(await beacons[0].body.text())};
}

function editorMessage(page, type, session = new URL(page.frame.src, page.location.origin).searchParams.get('editor_session'), id = Number(new URL(page.frame.src, page.location.origin).searchParams.get('edit'))) {
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow,
    data: {type, id, session, ...(type === 'ganwen-editor-saved' ? {status: 'publish', publicLanguages: languages} : {})}});
}

test('editor can close immediately while its iframe is still loading', async () => {
  const page = await browser({auth: true});
  page.edit.click();
  page.close.click();
  assert.equal(page.dialog.open, false);
  assert.equal(page.frame.src, undefined);
  assert.equal(page.edit.focused, true);
  assert.equal(page.messages.length, 0);
});

test('frontend editing opens the language currently being read and rejects an unsupported language hint', async () => {
  for (const language of [...languages, 'zz']) {
    const page = await browser({auth: true, language});
    page.edit.click();
    const url = new URL(page.frame.src, page.location.origin);
    assert.equal(url.searchParams.get('editor_language'), languages.includes(language) ? language : 'zh');
    assert.equal(url.searchParams.get('edit'), '1447');
    assert.equal(url.origin, page.location.origin);
  }
});

test('saving keeps the editor open and refreshes the original position only after confirmed close', async () => {
  const sessionStorage = storage();
  const url = 'https://www.ganwen.nl/en/?p=1447#notes';
  const page = await browser({auth: true, language: 'en', url, scrollY: 987, sessionStorage});
  page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
  editorMessage(page, 'ganwen-editor-saved');
  editorMessage(page, 'ganwen-editor-saved');
  assert.equal(page.location.reloads, undefined);
  assert.equal(page.dialog.open, true);
  page.close.click();
  assert.equal(page.dialog.open, true, 'child owns saving and unsaved-draft confirmation');
  assert.equal(page.messages[0].message.session, new URL(page.frame.src, page.location.origin).searchParams.get('editor_session'));
  editorMessage(page, 'ganwen-editor-close');
  assert.equal(page.dialog.open, false);
  assert.equal(page.location.reloads, 1);
  const reloaded = await browser({auth: true, language: 'en', url, sessionStorage});
  reloaded.emit('pageshow', {persisted: false});
  assert.equal(reloaded.scrolls.at(-1)?.top, 987);
  assert.equal(reloaded.edit.focused, true);
  reloaded.emit('pageshow', {persisted: false});
  assert.equal(reloaded.scrolls.length, 1);
});

test('closing and reopening the same article rejects the previous iframe instance messages', async () => {
  const page = await browser({auth: true});
  page.edit.click();
  const first = new URL(page.frame.src, page.location.origin).searchParams.get('editor_session');
  editorMessage(page, 'ganwen-editor-ready');
  editorMessage(page, 'ganwen-editor-close');
  page.edit.click();
  const second = new URL(page.frame.src, page.location.origin).searchParams.get('editor_session');
  assert.ok(first && second);
  assert.notEqual(first, second);
  for (const type of ['ganwen-editor-ready', 'ganwen-editor-saved', 'ganwen-editor-close']) editorMessage(page, type, first, 1447);
  assert.equal(page.dialog.open, true);
  assert.equal(page.location.reloads, undefined);
  editorMessage(page, 'ganwen-editor-ready', second);
  page.close.click();
  assert.equal(page.messages.at(-1).message.session, second);
  assert.equal(page.dialog.open, true);
  editorMessage(page, 'ganwen-editor-close', second);
  assert.equal(page.dialog.open, false);
  assert.equal(page.location.reloads, undefined);
});

test('independent pages return to the blog and follow an address changed in the editor', async () => {
  const sessionStorage = storage();
  sessionStorage.setItem('ganwen.list-position.v1', JSON.stringify({url: '/nl/?cat=13&paged=2', scroll: 300, savedAt: Date.now()}));
  const page = await browser({auth: true, independent: true, language: 'nl', url: 'https://www.ganwen.nl/nl/bewijs.html#a1', sessionStorage});
  assert.equal(page.back.getAttribute('href'), '/nl/');
  page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {
    type: 'ganwen-editor-saved', id: 1447, session: new URL(page.frame.src, page.location.origin).searchParams.get('editor_session'),
    status: 'publish', publicLanguages: ['zh', 'nl'], publicPath: '/evidence.html'}});
  editorMessage(page, 'ganwen-editor-close');
  assert.equal(page.location.assigned, '/nl/evidence.html#a1');
  assert.equal(page.location.reloads, undefined);
});

test('independent editor address cannot redirect to another origin', async () => {
  for (const publicPath of ['//evil.example/', 'https://evil.example/', '/proof.html?redirect=evil', '/proof%2ehtml']) {
    const page = await browser({auth: true, independent: true, url: 'https://www.ganwen.nl/bewijs.html'});
    page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
    page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {
      type: 'ganwen-editor-saved', id: 1447, session: new URL(page.frame.src, page.location.origin).searchParams.get('editor_session'),
      status: 'publish', publicLanguages: ['zh'], publicPath}});
    editorMessage(page, 'ganwen-editor-close');
    assert.equal(page.location.assigned, undefined);
    assert.equal(page.location.reloads, 1);
  }
});

test('a queued native close event cannot tear down an article reopened before that event arrives', async () => {
  const page = await browser({auth: true, article: false, editIDs: [1447, 10000]});
  page.dialog.close = () => { page.dialog.open = false; };
  page.edits[0].click(); editorMessage(page, 'ganwen-editor-ready');
  editorMessage(page, 'ganwen-editor-close');
  page.edits[1].click();
  const reopenedURL = page.frame.src;
  page.dialog.emit('close');
  assert.equal(page.dialog.open, true);
  assert.equal(page.frame.src, reopenedURL);
  assert.equal(page.document.body.classes.has('has-article-editor'), true);
  editorMessage(page, 'ganwen-editor-ready');
  page.close.click();
  assert.equal(page.messages.at(-1).message.id, 10000);
});

function loadedEditor(page, {protocol, loading = false, valid = true, scriptReady = true, target} = {}) {
  const root = new Element();
  if (protocol) root.setAttribute('data-editor-protocol', protocol);
  page.frame.contentWindow.location = new URL(target || page.frame.src, page.location.origin);
  const button = new Element(); if (scriptReady) button.onclick = () => {};
  page.frame.contentDocument = {documentElement: root, readyState: loading ? 'loading' : 'complete',
    getElementById: id => valid ? id === 'savePostButton' ? button : new Element() : null};
}

test('new frontend preserves draft protection with an old iframe after a deployment rollback', async () => {
  const page = await browser({auth: true});
  page.edit.click();
  loadedEditor(page);
  page.close.click();
  assert.equal(page.dialog.open, true);
  assert.equal(page.messages[0].message.type, 'ganwen-editor-close-request');
  assert.equal(page.messages[0].message.id, 1447);
  assert.equal(page.messages[0].message.session, undefined);
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {type: 'ganwen-editor-saved', id: 1447}});
  assert.equal(page.location.reloads, undefined);
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {type: 'ganwen-editor-close', id: 1447}});
  assert.equal(page.dialog.open, false);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.location.reloads, 1);
});

test('old iframe saves without visibility fields check the article and removed remembered pages before removing a stale filter', async () => {
  const targets = [];
  const page = await browser({auth: true, language: 'en', url: 'https://www.ganwen.nl/en/?p=1447',
    refreshResponse: async url => {targets.push(url); return {status: 404};}});
  page.back.setAttribute('href', '/en/?cat=13&paged=4');
  page.edit.click(); loadedEditor(page); page.frame.emit('load');
  for (const type of ['ganwen-editor-saved', 'ganwen-editor-close']) page.emit('message', {origin: page.location.origin,
    source: page.frame.contentWindow, data: {type, id: 1447}});
  assert.equal(page.dialog.open, false);
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(targets, ['/en/?p=1447', '/en/?cat=13&paged=4', '/en/?cat=13&paged=1']);
  assert.equal(page.location.assigned, '/en/?paged=1');
  assert.equal(page.location.reloads, undefined);
});

test('a translated article with missing API language metadata keeps its uncertain visibility check across a rapid reopen', async () => {
  let release;
  const page = await browser({auth: true, language: 'en', url: 'https://www.ganwen.nl/en/?p=1447',
    refreshResponse: () => new Promise(resolve => {release = resolve;})});
  page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
  const session = new URL(page.frame.src, page.location.origin).searchParams.get('editor_session');
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow,
    data: {type: 'ganwen-editor-saved', id: 1447, session, status: 'publish'}});
  editorMessage(page, 'ganwen-editor-close');
  assert.equal(page.calls.filter(call => call.settings?.method === 'HEAD').length, 1);
  page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
  release({status: 404});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.dialog.open, true);
  assert.equal(page.location.assigned, undefined);
  editorMessage(page, 'ganwen-editor-close');
  release({status: 404});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.calls.filter(call => call.settings?.method === 'HEAD').length, 2);
  assert.equal(page.location.assigned, '/en/?paged=1');
});

test('iframe load only enables legacy messages for a complete old admin document at the exact target', async () => {
  for (const options of [{protocol: '2'}, {protocol: '3'}, {loading: true}, {valid: false}, {scriptReady: false}, {target: 'https://evil.example/admin/?embed=1&edit=1447'},
    {target: 'https://www.ganwen.nl/admin/?embed=1&edit=1447&editor_session=wrong'}, {target: 'https://www.ganwen.nl/admin/?embed=1&edit=999'}]) {
    const page = await browser({auth: true});
    page.edit.click();
    loadedEditor(page, options); page.frame.emit('load');
    page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {type: 'ganwen-editor-saved', id: 1447}});
    page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {type: 'ganwen-editor-close', id: 1447}});
    assert.equal(page.dialog.open, true);
    assert.equal(page.location.reloads, undefined);
  }
  const page = await browser({auth: true});
  page.edit.click(); loadedEditor(page); page.frame.emit('load');
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {type: 'ganwen-editor-close', id: 1447}});
  assert.equal(page.dialog.open, false);
});

test('routine identity rechecks preserve a verified editor entry through a transient failure', async () => {
  const options = {auth: true};
  const page = await browser(options);
  options.probeError = true;
  page.emit('focus');
  assert.equal(page.edit.hidden, false);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.edit.hidden, false);
  assert.equal(page.admin.hidden, false);
  page.edit.click();
  assert.equal(page.dialog.open, true);
  delete options.probeError;
  options.auth = false;
  page.emit('storage', {key: 'ganwen.admin-session.changed'});
  assert.equal(page.edit.hidden, true);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.edit.hidden, true);
  assert.equal(page.dialog.open, true);
});

test('refreshing a verified identity never briefly hides the focused edit button', async () => {
  const page = await browser({auth: true});
  let hidden = page.edit.hidden;
  Object.defineProperty(page.edit, 'hidden', {get: () => hidden, set: value => {
    hidden = value;
    if (value && page.document.activeElement === page.edit) page.document.activeElement = page.document.body;
  }});
  page.edit.focus();
  page.emit('focus');
  assert.equal(page.document.activeElement, page.edit);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.document.activeElement, page.edit);
  assert.equal(page.edit.hidden, false);
});

test('unpublishing the current article returns to a safe list in the current language after close', async () => {
  for (const [status, publicLanguages, language] of [
    ['draft', [], 'zh'], ['private', [], 'en'], ['publish', ['zh'], 'en'], ['publish', ['zh', 'nl'], 'nl'],
  ]) {
    const url = 'https://www.ganwen.nl' + (language === 'zh' ? '/' : '/' + language + '/') + '?p=1447';
    const page = await browser({auth: true, url, language});
    page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
    const session = new URL(page.frame.src, page.location.origin).searchParams.get('editor_session');
    page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow,
      data: {type: 'ganwen-editor-saved', id: 1447, session, status, publicLanguages}});
    assert.equal(page.dialog.open, true);
    assert.equal(page.location.assigned, undefined);
    editorMessage(page, 'ganwen-editor-close');
    if (status === 'publish' && publicLanguages.includes(language)) {
      assert.equal(page.location.reloads, 1);
      assert.equal(page.location.assigned, undefined);
    } else {
      assert.equal(page.location.reloads, undefined);
      assert.equal(page.location.assigned, (language === 'zh' ? '/' : '/' + language + '/') + '?paged=1');
      assert.equal(page.sessionStorage.getItem('ganwen.editor-position.v1'), null);
    }
  }
});

test('the most recent save controls public visibility and list edits never navigate to another list', async () => {
  for (const article of [true, false]) {
    const page = await browser({auth: true, article, language: 'en', url: 'https://www.ganwen.nl/en/?p=1447'});
    page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
    const session = new URL(page.frame.src, page.location.origin).searchParams.get('editor_session');
    for (const [status, publicLanguages] of [['private', []], ['publish', ['zh', 'en']]]) {
      page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow,
        data: {type: 'ganwen-editor-saved', id: 1447, session, status, publicLanguages}});
    }
    editorMessage(page, 'ganwen-editor-close');
    assert.equal(page.location.reloads, 1);
    assert.equal(page.location.assigned, undefined);
  }
  for (const unsafe of ['https://evil.example/en/', '/en/?p=42', '/nl/?paged=2', '/admin/', '/en/?attachment_id=2']) {
    const page = await browser({auth: true, language: 'en', url: 'https://www.ganwen.nl/en/?p=1447'});
    page.back.setAttribute('href', unsafe);
    page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
    const session = new URL(page.frame.src, page.location.origin).searchParams.get('editor_session');
    page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow,
      data: {type: 'ganwen-editor-saved', id: 1447, session, status: 'private', publicLanguages: []}});
    editorMessage(page, 'ganwen-editor-close');
    assert.equal(page.location.assigned, '/en/');
  }
});

test('saving on a removed final list page returns to its first filtered page only after a real 404', async () => {
  for (const result of [200, 404, 503, 'network']) {
    const options = {auth: true, article: false, language: 'en', url: 'https://www.ganwen.nl/en/?cat=13&tag=notes&paged=2',
      editIDs: [1447], refreshStatus: result};
    options.refreshResponse = async url => {
      if (result === 'network') throw Error('temporary network failure');
      return {status: url.endsWith('paged=1') ? 200 : result};
    };
    const page = await browser(options);
    page.edits[0].click(); editorMessage(page, 'ganwen-editor-ready'); editorMessage(page, 'ganwen-editor-saved');
    editorMessage(page, 'ganwen-editor-close');
    assert.equal(page.dialog.open, false);
    await new Promise(resolve => setImmediate(resolve));
    const checks = page.calls.filter(call => call.settings?.method === 'HEAD');
    assert.equal(checks.length, result === 404 ? 2 : 1);
    assert.equal(checks[0].url, '/en/?cat=13&tag=notes&paged=2');
    assert.equal(checks[0].settings.cache, 'no-store');
    if (result === 404) {
      assert.equal(page.location.assigned, '/en/?cat=13&tag=notes&paged=1');
      assert.equal(page.location.reloads, undefined);
      assert.equal(page.sessionStorage.getItem('ganwen.editor-position.v1'), null);
    } else {
      assert.equal(page.location.assigned, undefined);
      assert.equal(page.location.reloads, 1);
    }
  }
});

test('unpublishing an article checks a remembered final list page before returning there', async () => {
  const page = await browser({auth: true, refreshResponse: async url => ({status: url.endsWith('paged=1') ? 200 : 404})});
  page.back.setAttribute('href', '/?category_name=notes&paged=4');
  page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow,
    data: {type: 'ganwen-editor-saved', id: 1447, session: new URL(page.frame.src, page.location.origin).searchParams.get('editor_session'), status: 'private', publicLanguages: []}});
  editorMessage(page, 'ganwen-editor-close');
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.location.assigned, '/?category_name=notes&paged=1');
  assert.equal(page.calls.filter(call => call.settings?.method === 'HEAD').length, 2);
});

test('saving a last filtered article returns to the same-language unfiltered list only after a real 404', async () => {
  for (const language of ['zh', 'en']) for (const filter of ['cat=13', 'category_name=notes', 'tag=notes', 'author=3']) {
    const prefix = language === 'zh' ? '/' : '/' + language + '/';
    for (const status of [200, 404, 503, 'network']) {
      const page = await browser({auth: true, article: false, language, editIDs: [1447],
        url: 'https://www.ganwen.nl' + prefix + '?' + filter + '&paged=1', scrollY: 432,
        refreshResponse: async () => {if (status === 'network') throw Error('temporary network failure'); return {status};}});
      page.edits[0].click(); editorMessage(page, 'ganwen-editor-ready'); editorMessage(page, 'ganwen-editor-saved'); editorMessage(page, 'ganwen-editor-close');
      await new Promise(resolve => setImmediate(resolve));
      const checks = page.calls.filter(call => call.settings?.method === 'HEAD');
      assert.equal(checks.length, 1, 'saved filtered first pages require one availability check');
      assert.equal(checks[0].url, prefix + '?' + filter + '&paged=1');
      if (status === 404) {
        assert.equal(page.location.assigned, prefix + '?paged=1');
        assert.equal(page.location.reloads, undefined);
        assert.equal(page.sessionStorage.getItem('ganwen.editor-position.v1'), null);
      } else {
        assert.equal(page.location.assigned, undefined);
        assert.equal(page.location.reloads, 1);
        assert.equal(JSON.parse(page.sessionStorage.getItem('ganwen.editor-position.v1')).scroll, 432);
      }
    }
  }
});

test('filtered pagination retains its filter unless its first page also returns 404', async () => {
  for (const firstPageStatus of [200, 404, 503, 'network']) {
    const route = '/nl/?tag=notes&author=3&paged=7#list';
    const page = await browser({auth: true, article: false, language: 'nl', editIDs: [1447], url: 'https://www.ganwen.nl' + route,
      refreshResponse: async url => {
        if (url === route) return {status: 404};
        if (firstPageStatus === 'network') throw Error('temporary network failure');
        return {status: firstPageStatus};
      }});
    page.edits[0].click(); editorMessage(page, 'ganwen-editor-ready'); editorMessage(page, 'ganwen-editor-saved'); editorMessage(page, 'ganwen-editor-close');
    await new Promise(resolve => setImmediate(resolve));
    assert.deepEqual(page.calls.filter(call => call.settings?.method === 'HEAD').map(call => call.url),
      [route, '/nl/?tag=notes&author=3&paged=1#list']);
    assert.equal(page.location.assigned, firstPageStatus === 404 ? '/nl/?paged=1' : '/nl/?tag=notes&author=3&paged=1#list');
    assert.equal(page.location.reloads, undefined);
  }
});

test('an unknown article and unavailable filtered archive share one five-second deadline across three checks', async () => {
  const pending = [];
  const page = await browser({auth: true, language: 'en', url: 'https://www.ganwen.nl/en/?p=1447',
    refreshResponse: (url, settings) => new Promise((resolve, reject) => {
      settings.signal.addEventListener('abort', () => reject(Error('timeout'))); pending.push({url, resolve, settings});
    })});
  page.back.setAttribute('href', '/en/?tag=notes&paged=5');
  page.edit.click(); editorMessage(page, 'ganwen-editor-ready');
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow,
    data: {type: 'ganwen-editor-saved', id: 1447, session: new URL(page.frame.src, page.location.origin).searchParams.get('editor_session'), status: 'publish'}});
  editorMessage(page, 'ganwen-editor-close');
  pending[0].resolve({status: 404}); await new Promise(resolve => setImmediate(resolve));
  pending[1].resolve({status: 404}); await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(pending.map(check => check.url), ['/en/?p=1447', '/en/?tag=notes&paged=5', '/en/?tag=notes&paged=1']);
  assert.equal(new Set(pending.map(check => check.settings.signal)).size, 1);
  const timeouts = page.timeouts.filter(item => item.active);
  assert.equal(timeouts.length, 1); assert.equal(timeouts[0].milliseconds, 5000);
  timeouts[0].callback(); await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.dialog.open, false);
  assert.equal(page.location.assigned, '/en/?tag=notes&paged=1', 'timeout must not remove a filter without its own 404');
});

test('a delayed filtered-first-page response cannot navigate across a rapid reopen', async () => {
  const pending = [];
  const page = await browser({auth: true, article: false, language: 'en', editIDs: [1447, 10000],
    url: 'https://www.ganwen.nl/en/?tag=notes&paged=2',
    refreshResponse: (url, settings) => new Promise(resolve => pending.push({url, settings, resolve}))});
  page.edits[0].click(); editorMessage(page, 'ganwen-editor-ready'); editorMessage(page, 'ganwen-editor-saved'); editorMessage(page, 'ganwen-editor-close');
  pending[0].resolve({status: 404}); await new Promise(resolve => setImmediate(resolve));
  assert.equal(pending.length, 2);
  page.edits[1].click(); editorMessage(page, 'ganwen-editor-ready');
  assert.equal(pending[1].settings.signal.aborted, true);
  pending[1].resolve({status: 404}); await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.dialog.open, true); assert.equal(page.location.assigned, undefined); assert.equal(page.location.reloads, undefined);
  editorMessage(page, 'ganwen-editor-close');
  pending[2].resolve({status: 200}); await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.location.reloads, 1);
});

test('a delayed final-page check cannot navigate over a reopened editor and committed changes refresh on its next close', async () => {
  let release;
  const page = await browser({auth: true, article: false, editIDs: [1447, 10000], url: 'https://www.ganwen.nl/?paged=2',
    refreshResponse: () => new Promise(resolve => {release = resolve;})});
  page.edits[0].click(); editorMessage(page, 'ganwen-editor-ready'); editorMessage(page, 'ganwen-editor-saved'); editorMessage(page, 'ganwen-editor-close');
  assert.equal(page.dialog.open, false);
  const firstCheck = page.calls.find(call => call.settings?.method === 'HEAD');
  assert.ok(firstCheck);
  page.edits[1].click(); editorMessage(page, 'ganwen-editor-ready');
  assert.equal(firstCheck.settings.signal.aborted, true);
  release({status: 404});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.dialog.open, true);
  assert.equal(page.location.assigned, undefined);
  assert.equal(page.location.reloads, undefined);
  editorMessage(page, 'ganwen-editor-close');
  release({status: 200});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.calls.filter(call => call.settings?.method === 'HEAD').length, 2);
  assert.equal(page.location.reloads, 1);
});

test('final-page checks time out after five seconds without guessing another route or trapping close', async () => {
  const page = await browser({auth: true, article: false, editIDs: [1447], url: 'https://www.ganwen.nl/?paged=2',
    refreshResponse: (_, settings) => new Promise((resolve, reject) => {settings.signal.addEventListener('abort', () => reject(Error('timeout')));})});
  page.edits[0].click(); editorMessage(page, 'ganwen-editor-ready'); editorMessage(page, 'ganwen-editor-saved'); editorMessage(page, 'ganwen-editor-close');
  assert.equal(page.dialog.open, false);
  const timeout = page.timeouts.find(item => item.active);
  assert.equal(timeout?.milliseconds, 5000);
  timeout.callback();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.location.assigned, undefined);
  assert.equal(page.location.reloads, 1);
});

test('ordinary articles, first pages, invalid page numbers and unchanged closes never issue an extra HEAD', async () => {
  for (const [route, article, saved] of [
    ['/?p=1447&paged=2', true, true], ['/?paged=1', false, true], ['/?paged=0', false, true],
    ['/?paged=2', false, false], ['/?paged=invalid', false, true], ['/?paged=1.5', false, true],
    ['/?tag=notes&paged=2', false, false], ['/?cat=&category_name=&tag=&author=&paged=1', false, true],
    ['/?p=1447&cat=13&tag=notes&paged=2', true, true],
  ]) {
    const page = await browser({auth: true, article, editIDs: [1447], url: 'https://www.ganwen.nl' + route});
    page.edits[0].click(); editorMessage(page, 'ganwen-editor-ready');
    if (saved) editorMessage(page, 'ganwen-editor-saved');
    editorMessage(page, 'ganwen-editor-close');
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(page.calls.filter(call => call.settings?.method === 'HEAD').length, 0);
    assert.equal(page.location.reloads, saved ? 1 : undefined);
  }
});

test('anonymous cached article stays anonymous and emits one sanitized page event', async () => {
  const page = await browser({url: 'https://www.ganwen.nl/en/?p=1447&token=SECRET#private', language: 'en', referrer: 'https://source.example/sensitive?password=SECRET'});
  assert.equal(page.edit.hidden, true); assert.equal(page.admin.hidden, true); assert.equal(page.frame.src, undefined);
  assert.equal(page.calls[0].url, '/admin/api/editor-session'); assert.equal(page.calls[0].settings.cache, 'no-store');
  const payload = await page.payload();
  assert.equal(payload.path, '/en/?p=1447'); assert.equal(payload.referrer, 'https://source.example'); assert.equal(payload.language, 'en');
  for (const key of ['event_id', 'visitor_id', 'session_id']) assert.match(payload[key], /^[a-f0-9-]{36}$/);
  assert.equal(JSON.stringify(payload).includes('SECRET'), false);
  page.emit('pageshow', {persisted: false}); page.emit('pageshow', {persisted: true}); page.emit('pageshow', {persisted: false});
  assert.equal(page.beacons.length, 1); assert.equal(page.document.cookie, '');
});
test('authenticated article edits in place and only the matching frame may save or close', async () => {
  const page = await browser({auth: true});
  assert.equal(page.edit.hidden, false); assert.equal(page.admin.hidden, false); assert.equal(page.beacons.length, 0);
  page.edit.click(); assert.equal(page.dialog.open, true);
  const editorURL = new URL(page.frame.src, page.location.origin);
  assert.equal(editorURL.pathname, '/admin/'); assert.equal(editorURL.searchParams.get('embed'), '1'); assert.equal(editorURL.searchParams.get('edit'), '1447');
  editorMessage(page, 'ganwen-editor-ready');
  assert.equal(page.document.body.classes.has('has-article-editor'), true);
  const message = {type: 'ganwen-editor-saved', id: 1447, session: editorURL.searchParams.get('editor_session'), status: 'publish', publicLanguages: languages};
  page.emit('message', {origin: 'https://evil.example', source: page.frame.contentWindow, data: message});
  page.emit('message', {origin: page.location.origin, source: {}, data: message});
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {...message, id: 2}});
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: {...message, session: 'previous-frame'}});
  assert.equal(page.location.reloads, undefined);
  page.close.click(); assert.equal(page.dialog.open, true);
  assert.equal(page.messages[0].message.type, 'ganwen-editor-close-request'); assert.equal(page.messages[0].origin, page.location.origin);
  let cancelled = false;
  page.dialog.emit('cancel', {preventDefault: () => { cancelled = true; }});
  assert.equal(cancelled, true); assert.equal(page.dialog.open, true);
  let prevented = false;
  page.emit('keydown', {key: 'Escape', preventDefault: () => { prevented = true; }});
  assert.equal(prevented, true); assert.equal(page.back.clicks, 0);
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: message});
  assert.equal(page.location.reloads, undefined);
  editorMessage(page, 'ganwen-editor-close');
  assert.equal(page.dialog.open, false); assert.equal(page.frame.src, undefined); assert.equal(page.edit.focused, true);
  assert.equal(page.document.body.classes.has('has-article-editor'), false);
  page.emit('message', {origin: page.location.origin, source: page.frame.contentWindow, data: message});
  assert.equal(page.location.reloads, 1);
});
test('home editing uses the selected card and restores its focus', async () => {
  const page = await browser({auth: true, article: false, editIDs: [1447, 10000, 0]});
  assert.deepEqual(page.edits.map(button => button.hidden), [false, false, true]);
  for (const [index, id] of [[0, 1447], [1, 10000]]) {
    page.edits[index].click();
    assert.equal(new URL(page.frame.src, page.location.origin).searchParams.get('edit'), String(id));
    editorMessage(page, 'ganwen-editor-ready');
    editorMessage(page, 'ganwen-editor-close', undefined, 99);
    assert.equal(page.dialog.open, true);
    editorMessage(page, 'ganwen-editor-close');
    assert.equal(page.dialog.open, false); assert.equal(page.edits[index].focused, true);
  }
});
test('focus, visibility and cross-tab login changes refresh identity without discarding the editor', async () => {
  const options = {auth: false};
  const page = await browser(options);
  options.auth = true; page.emit('focus');
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.edit.hidden, false);
  page.edit.click();
  const editorURL = page.frame.src;
  options.auth = false; page.document.visibilityState = 'visible'; page.emit('visibilitychange');
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.edit.hidden, true); assert.equal(page.dialog.open, true);
  assert.equal(page.frame.src, editorURL);
  options.auth = true; page.emit('storage', {key: 'ganwen.admin-session.changed'});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.edit.hidden, false); assert.equal(page.dialog.open, true);
  const count = page.calls.length; page.emit('storage', {key: 'unrelated'});
  assert.equal(page.calls.length, count);
});
test('a login notification during a stale session request schedules a fresh probe', async () => {
  let release;
  const options = {auth: false, probeResponse: () => new Promise(resolve => { release = resolve; })};
  const page = await browser(options);
  options.auth = true; delete options.probeResponse;
  page.emit('storage', {key: 'ganwen.admin-session.changed'});
  release({ok: true, json: async () => ({authenticated: false})});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.edit.hidden, false);
  assert.equal(page.calls.filter(call => call.url === '/admin/api/editor-session').length, 2);
  assert.equal(page.beacons.length, 0);
});
test('privacy preferences and uncertain authentication prevent collection', async () => {
  for (const options of [{dnt: '1'}, {dnt: 'yes'}, {gpc: true}, {probeError: true}, {probeOK: false}, {probeBody: {}}, {noCrypto: true}]) {
    const page = await browser(options);
    assert.equal(page.beacons.length, 0); assert.equal(page.edit.hidden, true);
    assert.equal(page.calls.length, 1);
  }
});
test('search words and sensitive routes never enter event payloads', async () => {
  const search = await browser({url: 'https://www.ganwen.nl/?s=private%40example.net&cat=13&nonce=SECRET'});
  const payload = await search.payload();
  assert.equal(payload.path, '/?cat=13&s='); assert.equal(JSON.stringify(payload).includes('private'), false);
  for (const url of ['https://www.ganwen.nl/admin/', 'https://www.ganwen.nl/bewijs.html', 'https://www.ganwen.nl/wp-content/uploads/private.jpg', 'https://www.ganwen.nl/?preview=1', 'https://www.ganwen.nl/?p=1447&p=2', 'https://www.ganwen.nl/?tag=private%40mail.example']) {
    assert.equal((await browser({url})).beacons.length, 0, url);
  }
});
test('anonymous identifiers are session scoped and event identifiers are unique', async () => {
  const sessionStorage = storage();
  const first = await browser({sessionStorage}); const second = await browser({sessionStorage, url: 'https://www.ganwen.nl/?p=1875'});
  const one = await first.payload(); const two = await second.payload();
  assert.equal(one.visitor_id, two.visitor_id); assert.equal(one.session_id, two.session_id); assert.notEqual(one.event_id, two.event_id);
  sessionStorage.setItem('ganwen.analytics.last-seen.v1', String(Date.now() - 1800001));
  const three = await (await browser({sessionStorage})).payload();
  assert.equal(three.visitor_id, one.visitor_id); assert.notEqual(three.session_id, one.session_id);
});
test('beacon fallback sends only one same-origin keepalive POST', async () => {
  const page = await browser({beacon: false, article: false});
  assert.equal(page.calls.length, 2); assert.equal(page.calls[1].url, '/analytics/event');
  assert.equal(page.calls[1].settings.method, 'POST'); assert.equal(page.calls[1].settings.keepalive, true);
  assert.equal(page.calls[1].settings.credentials, 'same-origin');
  page.emit('pageshow', {}); assert.equal(page.calls.length, 2);
});
test('back-forward cache rechecks authentication and hides controls after logout', async () => {
  const options = {auth: true};
  const page = await browser(options);
  assert.equal(page.edit.hidden, false);
  options.auth = false;
  page.emit('pageshow', {persisted: true});
  assert.equal(page.edit.hidden, true); assert.equal(page.admin.hidden, true);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.edit.hidden, true); assert.equal(page.beacons.length, 1);
  page.emit('pageshow', {persisted: true});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(page.beacons.length, 1);
});
test('event query contract matches publicPage and real WordPress archive filters', async () => {
  const allowed = new Set(['p', 'page_id', 'cat', 'category_name', 'tag', 'author', 'm', 'post_format', 'paged', 's']);
  const cases = [
    ['/?p=1447&lang=nl&year=2025&monthnum=1&day=3&tag_id=23&author_name=SECRET', '/?p=1447'],
    ['/?page_id=2&lang=en', '/?page_id=2'],
    ['/nl/index.php?cat=13&category_name=reflections&tag=%E6%A0%87%E7%AD%BE&author=1&m=202501&post_format=video&paged=2&s=PRIVATE&year=2025',
      '/nl/index.php?cat=13&paged=2&author=1&category_name=reflections&tag=%E6%A0%87%E7%AD%BE&post_format=video&m=202501&s='],
    ['/?year=2025&monthnum=1&day=3&tag_id=23&author_name=SECRET&lang=nl', '/'],
  ];
  for (const [route, expected] of cases) {
    const payload = await (await browser({url: 'https://www.ganwen.nl' + route})).payload();
    assert.equal(payload.path, expected);
    for (const key of new URL(payload.path, 'https://www.ganwen.nl').searchParams.keys()) assert.equal(allowed.has(key), true, key);
    assert.equal(JSON.stringify(payload).includes('PRIVATE'), false); assert.equal(JSON.stringify(payload).includes('SECRET'), false);
  }
  for (const route of ['/?attachment_id=200', '/en/?attachment_id=200&cat=13', '/?p=1447&page_id=2', '/?m=202513', '/?m=2025', '/?post_format=unsupported']) {
    assert.equal((await browser({url: 'https://www.ganwen.nl' + route})).beacons.length, 0, route);
  }
});
test('language home routes match publicLanguagePath with or without trailing slash', async () => {
  for (const language of languages.filter(language => language !== 'zh')) {
    for (const suffix of ['', '/', '/index.php']) {
      const route = '/' + language + suffix;
      const home = await (await browser({url: 'https://www.ganwen.nl' + route, language, article: false})).payload();
      assert.equal(home.path, route); assert.equal(home.language, language);
      const article = await (await browser({url: 'https://www.ganwen.nl' + route + '?p=1447&token=SECRET', language})).payload();
      assert.equal(article.path, route + '?p=1447'); assert.equal(article.language, language);
      assert.equal(JSON.stringify(article).includes('SECRET'), false);
    }
  }
  for (const route of ['/enindex.php', '/nlindex.php', '/esindex.php', '/en/admin/', '/nl/wp-admin/', '/es//', '/zh', '/zh/', '/de/', '/xx/index.php', '/ur/admin/', '/ar//']) {
    assert.equal((await browser({url: 'https://www.ganwen.nl' + route})).beacons.length, 0, route);
  }
});

test('language picker supports current-language focus, arrows, Home, End and Escape', async () => {
  const page = await browser({language: 'nl', auth: true});
  const press = key => {
    const event = {key, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }};
    page.picker.emit('keydown', event); page.emit('keydown', event); return event;
  };
  page.summary.focus();
  assert.equal(press('ArrowDown').defaultPrevented, true); assert.equal(page.picker.open, true);
  assert.equal(page.document.activeElement, page.languageLinks[1]);
  press('ArrowDown'); assert.equal(page.document.activeElement, page.languageLinks[2]);
  press('Home'); assert.equal(page.document.activeElement, page.languageLinks[0]);
  press('ArrowUp'); assert.equal(page.document.activeElement, page.languageLinks.at(-1));
  press('End'); assert.equal(page.document.activeElement, page.languageLinks.at(-1));
  assert.equal(press('Escape').defaultPrevented, true); assert.equal(page.picker.open, false);
  assert.equal(page.document.activeElement, page.summary); assert.equal(page.back.clicks, 0);
});
test('open picker keeps article shortcuts from navigating and closes on outside click or focus', async () => {
  const page = await browser({auth: true});
  page.picker.open = true; page.languageLinks[0].focus();
  let prevented = false;
  page.emit('keydown', {key: 'Escape', preventDefault: () => { prevented = true; }});
  assert.equal(page.back.clicks, 0); assert.equal(prevented, false);
  page.emit('click', {target: page.languageLinks[0]}); assert.equal(page.picker.open, true);
  page.emit('click', {target: new Element()}); assert.equal(page.picker.open, false);
  page.picker.open = true; page.emit('focusin', {target: page.summary}); assert.equal(page.picker.open, true);
  page.emit('focusin', {target: new Element()}); assert.equal(page.picker.open, false);
});
test('direct contact links keep article shortcuts local without changing focus in all languages', async () => {
  for (const language of languages) {
    const page = await browser({language});
    const timerCount = page.timeouts.length;
    assert.equal(page.introContacts.tagName, 'NAV');
    assert.equal(page.introContacts.hidden, false);
    for (const control of page.contactLinks) {
      const href = control.href;
      assert.equal(control.hidden, false);
      control.click();
      assert.equal(control.href, href);
      control.focus();
      for (const key of ['ArrowLeft', 'ArrowRight', 'Escape', '/', '?']) {
        page.emit('keydown', {key, target: control, preventDefault() { throw Error('Contact shortcut changed article navigation'); }});
      }
      assert.equal(page.document.activeElement, control);
      assert.equal(page.previous.clicks, 0);
      assert.equal(page.next.clicks, 0);
      assert.equal(page.back.clicks, 0);
      assert.equal(page.search.hidden, true);
      assert.equal(page.help.open, false);
    }
    assert.equal(page.timeouts.length, timerCount, 'contact interactions must not start timers');
  }
});

test('contact descendants and either active focus or event target protect native contact keys', async () => {
  const page = await browser();
  const label = new Element(); page.contactLinks[0].append(label);
  for (const [active, target] of [[page.contactLinks[0], page.document.body], [page.document.body, label]]) {
    page.document.activeElement = active;
    for (const key of ['ArrowLeft', 'ArrowRight', 'Escape', '/', '?']) {
      page.emit('keydown', {key, target, preventDefault() { throw Error('Native contact key was consumed'); }});
    }
    assert.equal(page.document.activeElement, active);
  }
  assert.equal(page.previous.clicks, 0); assert.equal(page.next.clicks, 0); assert.equal(page.back.clicks, 0);
  assert.equal(page.search.hidden, true); assert.equal(page.help.open, false);
});

test('article shortcuts work after leaving contacts and when contacts are absent', async () => {
  for (const contacts of [true, false]) {
    const page = await browser({contacts});
    const timerCount = page.timeouts.length;
    if (contacts) page.contactLinks[0].focus();
    else {
      assert.equal(page.document.querySelector('.intro-contacts'), null);
      assert.equal(page.contactLinks.length, 0);
    }
    const outside = new Element(); outside.ownerDocument = page.document; outside.focus();
    page.emit('click', {target: outside}); page.emit('focusin', {target: outside});
    assert.equal(page.document.activeElement, outside);
    for (const key of ['ArrowLeft', 'ArrowRight', 'Escape']) page.emit('keydown', {key, target: outside, preventDefault() {}});
    assert.equal(page.previous.clicks, 1); assert.equal(page.next.clicks, 1); assert.equal(page.back.clicks, 1);
    assert.equal(page.timeouts.length, timerCount);
  }
});

test('all eleven language links retain filter URLs and write the selected same-origin cookie', async () => {
  const page = await browser({article: false});
  assert.deepEqual(page.languageLinks.map(link => link.getAttribute('lang')), languages);
  for (const link of page.languageLinks) {
    const before = link.href;
    link.click();
    assert.match(page.document.cookie, new RegExp('^ganwen_language=' + link.getAttribute('lang') + ';'));
    assert.match(page.document.cookie, /Path=\/; Max-Age=31536000; SameSite=Lax; Secure$/);
    assert.equal(link.href, before); assert.equal(new URL(link.href).search, '?cat=13&tag=notes&paged=2');
  }
  const link = page.languageLinks[3]; const cookie = page.document.cookie;
  link.href = 'https://external.example/hi/'; link.click(); assert.equal(page.document.cookie, cookie);
  const http = await browser({url: 'http://localhost/fr/?p=1447', language: 'fr'});
  http.languageLinks[6].click(); assert.equal(http.document.cookie.includes('; Secure'), false);
});
test('new-language list position survives article navigation with its full filter context', async () => {
  for (const language of languages.filter(language => language !== 'zh')) {
    const sessionStorage = storage(); const list = '/' + language + '/?cat=13&tag=notes&paged=2';
    const page = await browser({url: 'https://www.ganwen.nl' + list, language, article: false, scrollY: 846, sessionStorage});
    page.emit('pagehide');
    const state = JSON.parse(sessionStorage.getItem('ganwen.list-position.v1'));
    assert.equal(state.url, list); assert.equal(state.scroll, 846);
    const article = await browser({url: 'https://www.ganwen.nl/' + language + '/?p=1447', language, sessionStorage, referrer: 'https://www.ganwen.nl' + list});
    assert.equal(article.back.getAttribute('href'), list);
  }
});

test('changing article language never replaces the current-language back link with an old list', async () => {
  const sessionStorage = storage();
  const list = await browser({url: 'https://www.ganwen.nl/en/?cat=13&tag=notes&paged=2', language: 'en', article: false, sessionStorage});
  list.emit('pagehide');
  const translated = await browser({url: 'https://www.ganwen.nl/nl/?p=1447', language: 'nl', sessionStorage});
  assert.equal(translated.back.getAttribute('href'), '/nl/?paged=1');
  const original = await browser({url: 'https://www.ganwen.nl/en/?p=1447', language: 'en', sessionStorage});
  assert.equal(original.back.getAttribute('href'), '/en/?cat=13&tag=notes&paged=2');
});

test('search enhancement follows server state and Escape closes search before article navigation', async () => {
  const results = await browser({searchOpen: true, url: 'https://www.ganwen.nl/?s=notes', article: false});
  assert.equal(results.search.hidden, false); assert.equal(results.searchToggle.getAttribute('aria-expanded'), 'true');
  results.searchToggle.click();
  assert.equal(results.search.hidden, true); assert.equal(results.searchToggle.getAttribute('aria-expanded'), 'false');
  const article = await browser();
  assert.equal(article.search.hidden, true); assert.equal(article.searchToggle.getAttribute('aria-expanded'), 'false');
  article.emit('keydown', {key: '/', preventDefault() {}});
  assert.equal(article.search.hidden, false); assert.equal(article.document.activeElement, article.input);
  article.emit('keydown', {key: 'Escape', preventDefault() {}});
  assert.equal(article.search.hidden, true); assert.equal(article.document.activeElement, article.searchToggle);
  assert.equal(article.back.clicks, 0);
  article.document.activeElement = article.document.body;
  article.emit('keydown', {key: 'Escape', preventDefault() {}});
  assert.equal(article.back.clicks, 1);
});

test('Escape closes shortcut help and restores its summary focus without leaving the article', async () => {
  const page = await browser();
  page.emit('keydown', {key: '?', preventDefault() {}});
  assert.equal(page.help.open, true);
  page.emit('keydown', {key: 'Escape', preventDefault() {}});
  assert.equal(page.help.open, false); assert.equal(page.document.activeElement, page.helpSummary);
  assert.equal(page.back.clicks, 0);
});

test('article shortcuts leave native media, editable descendants and interactive widgets alone', async () => {
  const page = await browser();
  const controls = ['INPUT', 'TEXTAREA', 'SELECT', 'BUTTON', 'SUMMARY', 'AUDIO', 'VIDEO'].map(tagName => {
    const control = new Element(); control.tagName = tagName; return control;
  });
  for (const role of ['slider', 'textbox', 'tab', 'listbox', 'tree', 'grid']) {
    const widget = new Element(); widget.setAttribute('role', role); controls.push(widget);
  }
  const editable = new Element(); editable.setAttribute('contenteditable', '');
  const editableChild = new Element(); editable.append(editableChild); controls.push(editableChild);
  const inheritedEditable = new Element(); inheritedEditable.isContentEditable = true; controls.push(inheritedEditable);
  for (const control of controls) {
    page.document.activeElement = control;
    for (const key of ['ArrowRight', '/', 'Escape']) page.emit('keydown', {key, target: control, preventDefault() { throw Error('Interactive control consumed by article shortcut'); }});
  }
  assert.equal(page.next.clicks, 0); assert.equal(page.back.clicks, 0); assert.equal(page.search.hidden, true);
  page.document.activeElement = page.document.body;
  page.emit('keydown', {key: 'ArrowRight', preventDefault() {}});
  assert.equal(page.next.clicks, 1);
});

test('layout selection persists the allowed cookie and restores article scroll once after reload', async () => {
  const sessionStorage = storage(); const url = 'https://www.ganwen.nl/en/?p=1447#notes';
  const page = await browser({url, language: 'en', scrollY: 987, sessionStorage});
  page.layout.value = 'classic'; page.layout.emit('change');
  assert.equal(page.document.cookie, 'ganwen.layout.v1=classic; Path=/; Max-Age=31536000; SameSite=Lax; Secure');
  assert.equal(page.location.reloads, 1); assert.equal(page.location.href, url);
  const reloaded = await browser({url, language: 'en', sessionStorage});
  reloaded.emit('pageshow', {persisted: false});
  assert.equal(reloaded.scrolls.length, 1); assert.equal(reloaded.scrolls[0].top, 987);
  assert.equal(sessionStorage.getItem('ganwen.layout-position.v1'), null);
  reloaded.emit('pageshow', {persisted: false}); assert.equal(reloaded.scrolls.length, 1);
  const http = await browser({url: 'http://localhost/?paged=2', article: false});
  http.layout.value = 'journal'; http.layout.emit('change');
  assert.equal(http.document.cookie.includes('Secure'), false);
  assert.equal(JSON.parse(http.sessionStorage.getItem('ganwen.list-position.v1')).url, '/?paged=2');
  const invalid = await browser(); invalid.layout.value = 'unexpected; bad=value'; invalid.layout.emit('change');
  assert.equal(invalid.document.cookie, ''); assert.equal(invalid.location.reloads, undefined);
});

test('article TOC preserves existing anchors and text while avoiding generated ID collisions', async () => {
  const heading = (tag, text, id = '') => { const element = new Element(); element.tagName = tag; element.textContent = text; element.id = id; element.innerHTML = '<em>original markup</em>'; return element; };
  const headings = [heading('H2', 'First <img src=x>'), heading('H3', ' Existing title ', 'existing section'), heading('H2', 'First <img src=x>')];
  const reserved = new Element(); reserved.id = 'article-section-1';
  const page = await browser({headings, idElements: [reserved]});
  assert.equal(page.toc.hidden, false);
  assert.deepEqual(headings.map(item => item.id), ['article-section-1-2', 'existing section', 'article-section-3']);
  assert.deepEqual(page.tocLinks.children.map(link => link.href), ['#article-section-1-2', '#existing%20section', '#article-section-3']);
  assert.deepEqual(page.tocLinks.children.map(link => link.textContent), ['First <img src=x>', 'Existing title', 'First <img src=x>']);
  assert.equal(page.tocLinks.children[1].className, 'toc-subheading');
  for (const item of headings) assert.equal(item.innerHTML, '<em>original markup</em>');
  const single = heading('H2', 'Only heading');
  const short = await browser({headings: [single, heading('H3', '  ')]});
  assert.equal(short.toc.hidden, true); assert.equal(short.tocLinks.children.length, 0); assert.equal(single.id, '');
});

test('early theme bootstrap applies saved and system themes before deferred controls exist', () => {
  for (const [saved, systemDark, expected, denied] of [
    ['light', true, 'light', false], ['dark', false, 'dark', false],
    ['system', true, 'dark', false], ['invalid', false, 'light', false],
    [null, true, 'dark', true],
  ]) {
    const root = {dataset: {}};
    const meta = {setAttribute: (key, value) => { meta[key] = value; }};
    vm.runInNewContext(themeSource, {
      document: {documentElement: root, querySelector: selector => {
        assert.equal(selector, 'meta[name="theme-color"]'); return meta;
      }},
      localStorage: {getItem: key => {
        assert.equal(key, 'ganwen.theme.v1');
        if (denied) throw Error('storage unavailable');
        return saved;
      }},
      matchMedia: () => ({matches: systemDark}),
    });
    assert.equal(root.dataset.theme, expected);
    assert.equal(meta.content, expected === 'dark' ? '#1e1e1e' : '#eff4f9');
  }
});
