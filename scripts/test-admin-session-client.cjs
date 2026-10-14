'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {webcrypto, createHash} = require('node:crypto');

const html = fs.readFileSync(path.join(__dirname, '../internal/blog/admin_ui.html'), 'utf8');
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)];
const source = scripts[1][1].trim().replace(/start\(\);$/, '');

function client(respond, query = '?embed=1&edit=2&editor_session=fixture-instance') {
  const attributes = new Map([...html.matchAll(/<[^>]*\bid="([^"]+)"[^>]*>/g)].map(match => [match[1], match[0]]));
  class Node {
    constructor(markup = '') {
      Object.assign(this, {value: '', textContent: '', checked: false, disabled: false, open: false, dataset: {}, style: {}, children: [], visibilityChanges: [], contentDocument: {documentElement: {dataset: {}}, body: {innerHTML: '<p>unsaved visual draft</p>'}}});
      const names = new Set((markup.match(/\bclass="([^"]*)"/)?.[1] || '').split(/\s+/).filter(Boolean));
      const change = (item, on) => {const wasHidden = names.has('hidden'); if (on) names.add(item); else names.delete(item); if (wasHidden !== names.has('hidden')) this.visibilityChanges.push(!names.has('hidden')); return on;};
      this.classList = {add: (...items) => items.forEach(item => change(item, true)), remove: (...items) => items.forEach(item => change(item, false)), contains: item => names.has(item), toggle: (item, on) => change(item, on ?? !names.has(item))};
    }
    addEventListener(name, handler) {this['on' + name] = handler;}
    setAttribute(key, value) {this[key] = value;}
    removeAttribute(key) {delete this[key];}
    append(...nodes) {this.children.push(...nodes);}
    replaceChildren(...nodes) {this.children = nodes;}
    querySelector() {return null;}
    close() {this.open = false;}
    focus() {}
  }
  const nodes = new Map(), get = id => {if (!nodes.has(id)) nodes.set(id, new Node(attributes.get(id))); return nodes.get(id);};
  const events = {}, calls = [], messages = [];
  const context = {
    document: {documentElement: {dataset: {theme: 'light', themeMode: 'light'}}, getElementById: get, createElement: () => new Node(), createElementNS: () => new Node(), querySelector: () => new Node(), querySelectorAll: () => [], addEventListener: (name, handler) => events['document:' + name] = handler},
    window: {parent: {postMessage: (message, origin) => messages.push({message, origin})}, confirm: () => true, addEventListener: (name, handler) => events[name] = handler},
    location: {origin: 'https://ganwen.test', href: 'https://ganwen.test/admin/' + query, search: query},
    history: {replaceState() {}}, localStorage: {setItem() {}}, matchMedia: () => ({matches: false, addEventListener() {}}),
    ResizeObserver: class {observe() {}}, getComputedStyle: () => ({paddingLeft: '0px', paddingRight: '0px'}),
    setTimeout: () => 1, clearTimeout() {}, FormData: class {}, URL, URLSearchParams, TextEncoder, crypto: webcrypto, console,
    fetch: async (url, options) => {
      calls.push({path: url.replace('/admin/api/', ''), options});
      const result = await respond(calls.at(-1), calls);
      return {ok: !result.status || result.status < 400, status: result.status || 200, json: async () => result.data};
    }
  };
  vm.createContext(context);
  require("./admin-i18n-test-harness.cjs")(context);
  vm.runInContext(source, context);
  const run = js => vm.runInContext(js, context);
  const state = run('state');
  return {state, run, nodes, get, calls, context, events, messages};
}

test('embedded editor announces its matching instance once before session startup', () => {
  const c = client(() => {throw Error('ready must not require authentication');});
  assert.equal(c.calls.length, 0);
  assert.equal(c.messages.length, 1);
  assert.equal(c.messages[0].message.type, 'ganwen-editor-ready');
  assert.equal(c.messages[0].message.id, 2);
  assert.equal(c.messages[0].message.session, 'fixture-instance');
  assert.equal(c.messages[0].origin, c.context.location.origin);
  assert.ok(/<html\b[^>]*data-editor-protocol="2"/.test(html), 'parent needs an explicit protocol marker for deployment version skew');
});

test('embedded editor rejects prior-instance close requests and closes its unloaded startup without prompting', () => {
  const c = client(() => ({data: {}}));
  let confirmations = 0;
  c.context.window.confirm = () => {confirmations++; return false;};
  const event = {origin: c.context.location.origin, source: c.context.window.parent, data: {type: 'ganwen-editor-close-request', id: 2, session: 'old-instance'}};
  c.events.message(event);
  assert.equal(c.messages.length, 1);
  c.events.message({...event, data: {...event.data, session: 'fixture-instance'}});
  assert.equal(confirmations, 0);
  assert.equal(c.messages.length, 2);
  assert.equal(c.messages[1].message.type, 'ganwen-editor-close');
  assert.equal(c.messages[1].message.session, 'fixture-instance');
});

test('embedded editor saved notification carries the authoritative language list and its instance', () => {
  const c = client(() => ({data: {}}));
  c.run("state.authenticated=true;state.post={id:2,status:'publish'};notifyEditorParent('ganwen-editor-saved',{public_languages:['zh','en']});");
  assert.equal(c.messages.length, 2);
  const saved = c.messages[1].message;
  assert.equal(saved.type, 'ganwen-editor-saved');
  assert.equal(saved.id, 2);
  assert.equal(saved.session, 'fixture-instance');
  assert.equal(saved.status, 'publish');
  assert.deepEqual(Array.from(saved.publicLanguages), ['zh', 'en']);
  c.run("notifyEditorParent('ganwen-editor-saved',{});");
  assert.equal(c.messages[2].message.publicLanguages, undefined, 'missing backend visibility must remain unknown');
});

test('legacy embedding without a session token keeps its old close and save protocol', () => {
  const c = client(() => ({data: {}}), '?embed=1&edit=2');
  assert.equal(c.messages.length, 0, 'an old parent does not use ready messages');
  const event = {origin: c.context.location.origin, source: c.context.window.parent, data: {type: 'ganwen-editor-close-request', id: 2}};
  c.events.message({...event, data: {...event.data, session: 'unexpected-instance'}});
  assert.equal(c.messages.length, 0);
  c.events.message(event);
  assert.equal(c.messages.length, 1);
  assert.equal(c.messages[0].message.type, 'ganwen-editor-close');
  assert.equal(c.messages[0].message.session, undefined);
  c.run("state.authenticated=true;state.post={id:2,status:'publish'};notifyEditorParent('ganwen-editor-saved',{public_languages:['zh']});");
  assert.equal(c.messages[1].message.type, 'ganwen-editor-saved');
  assert.equal(c.messages[1].message.id, 2);
  assert.equal(c.messages[1].message.session, undefined);
});

function authenticatedFixture({path}) {
  if (path === 'session') return {data: {authenticated: true, configured: true, user: 'editor', csrf: 'fixture-session'}};
  if (path === 'settings') return {data: {categories: [], tags: []}};
  assert.equal(path, 'post?id=2');
  return {data: {post: {id: 2, type: 'page', title: 'Requested page', content_html: '<p>Page</p>', status: 'publish', translations: {}}, source_hash: 'fixture-hash'}};
}

test('foreign-language embedded editor opens and saves the requested translation without writing the source', async () => {
  const post = {...authenticatedFixture({path: 'post?id=2'}).data.post, version: 3, translations: {en: {title: 'English title', content_html: '<p>English body</p>', source_hash: 'fixture-hash', status: 'publish', seo: {}}}};
  const c = client(call => {
    if (call.path === 'post?id=2') return {data: {post, source_hash: 'fixture-hash', public_languages: ['zh', 'en']}};
    if (call.path === 'translation') {
      const payload = JSON.parse(call.options.body);
      assert.equal(payload.language, 'en');
      assert.equal(payload.translation.title, 'Changed English title');
      assert.equal(payload.translation.content_html, '<p>Changed English body</p>');
      return {data: {post: {...post, translations: {en: payload.translation}}, source_hash: 'fixture-hash', public_languages: ['zh', 'en']}};
    }
    return authenticatedFixture(call);
  }, '?embed=1&edit=2&editor_session=fixture-instance&editor_language=en');
  c.state.mode = 'source';
  await c.run('start()');
  assert.equal(c.state.lang, 'en');
  assert.equal(c.get('postTitle').value, 'English title');
  assert.equal(c.get('sourceEditor').value, '<p>English body</p>');
  c.get('postTitle').value = 'Changed English title';
  c.get('sourceEditor').value = '<p>Changed English body</p>';
  // Inline upload behavior is covered by the real media-function harness.
  c.run('normalizeInlineImages=async()=>{}');
  await c.run('saveEditor()');
  assert.deepEqual(c.calls.filter(call => call.options.method === 'POST').map(call => call.path), ['translation']);
  assert.equal(c.state.post.title, 'Requested page');
  assert.equal(c.state.post.content_html, '<p>Page</p>');
  assert.equal(c.state.lang, 'en');
});

test('embedded language selection accepts every supported code while absent, invalid and nonembedded requests use Chinese', async () => {
  const c = client(authenticatedFixture);
  const codes = Array.from(c.run('editorLanguages.map(([code])=>code)'));
  for (const code of codes) {
    c.context.location.search = '?embed=1&edit=2&editor_language=' + code;
    c.state.authenticated = true;
    await c.run('openPost(2)');
    assert.equal(c.state.lang, code);
  }
  for (const query of ['?embed=1&edit=2', '?embed=1&edit=2&editor_language=unsupported', '?edit=2&editor_language=en']) {
    const fallback = client(authenticatedFixture, query);
    fallback.state.authenticated = true;
    await fallback.run('openPost(2)');
    assert.equal(fallback.state.lang, 'zh');
  }
});

test('same-account login restores the chosen draft language instead of reapplying the front-page language', async () => {
  const c = client(() => ({data: {authenticated: false, configured: true, csrf: 'fresh-login'}}), '?embed=1&edit=2&editor_language=en');
  draft(c);
  await assert.rejects(c.run("api('translation',{id:2})"), /登录已过期/);
  await c.run("showApp('editor')");
  assertDraft(c);
  assert.equal(c.calls.length, 1);
});

test('startup hides login before a delayed session and opens the requested editor without showing it', async () => {
  let release;
  const c = client(call => call.path === 'session' ? new Promise(resolve => release = resolve) : authenticatedFixture(call));
  assert.ok(/<section\b[^>]*id="startup"/.test(html), 'initial loading section is missing');
  assert.equal(c.get('login').classList.contains('hidden'), true);
  assert.equal(c.get('shell').classList.contains('hidden'), true);
  assert.equal(c.get('startup').classList.contains('hidden'), false);
  const pending = c.run('start()');
  assert.equal(c.get('login').classList.contains('hidden'), true);
  assert.equal(c.get('startup').classList.contains('hidden'), false);
  assert.equal(c.run('start()'), pending);
  assert.deepEqual(c.calls.map(call => call.path), ['session']);
  release(authenticatedFixture({path: 'session'}));
  await pending;
  assert.equal(c.state.authenticated, true);
  assert.equal(c.state.post.id, 2);
  assert.equal(c.get('postTitle').value, 'Requested page');
  assert.equal(c.get('shell').classList.contains('hidden'), false);
  assert.equal(c.get('startup').classList.contains('hidden'), true);
  assert.deepEqual(c.get('login').visibilityChanges, []);
  assert.deepEqual(c.calls.map(call => call.path), ['session', 'settings', 'post?id=2']);
});

test('startup shows login only after an anonymous session and reuses its challenge', async () => {
  let release;
  const c = client(() => new Promise(resolve => release = resolve));
  const pending = c.run('start()');
  assert.equal(c.get('login').classList.contains('hidden'), true);
  release({data: {authenticated: false, configured: true, csrf: 'existing-challenge'}});
  await pending;
  assert.equal(c.state.authenticated, false);
  assert.equal(c.state.csrf, 'existing-challenge');
  assert.equal(c.get('login').classList.contains('hidden'), false);
  assert.equal(c.get('shell').classList.contains('hidden'), true);
  assert.equal(c.get('startup').classList.contains('hidden'), true);
  assert.deepEqual(c.get('login').visibilityChanges, [true]);
  assert.deepEqual(c.calls.map(call => call.path), ['session']);
});

for (const failure of ['503', 'network']) {
  test('startup ' + failure + ' failure stays out of login and retries without duplicate requests', async () => {
    let attempts = 0, release;
    const c = client(call => {
      if (call.path !== 'session') return authenticatedFixture(call);
      if (++attempts > 1) return new Promise(resolve => release = resolve);
      if (failure === 'network') throw Error('Failed to fetch');
      return {status: 503, data: {error: '暂时无法读取登录状态，请稍后重试'}};
    });
    await c.run('start()');
    assert.equal(c.state.authenticated, false);
    assert.equal(c.get('login').classList.contains('hidden'), true);
    assert.equal(c.get('startup').classList.contains('hidden'), false);
    assert.equal(c.get('startupError').classList.contains('hidden'), false);
    assert.match(c.get('startupError').textContent, failure === 'network' ? /请求失败，请稍后重试/ : /暂时无法读取登录状态/);
    assert.equal(c.get('startupRetry').classList.contains('hidden'), false);
    assert.equal(c.get('startupRetry').disabled, false);
    const pending = c.get('startupRetry').onclick();
    assert.equal(c.get('startupRetry').disabled, true);
    assert.equal(c.get('startupRetry').onclick(), pending);
    assert.equal(attempts, 2);
    assert.equal(c.get('login').classList.contains('hidden'), true);
    release(authenticatedFixture({path: 'session'}));
    await pending;
    assert.equal(c.state.authenticated, true);
    assert.equal(c.state.post.id, 2);
    assert.equal(c.get('startup').classList.contains('hidden'), true);
    assert.deepEqual(c.get('login').visibilityChanges, []);
  });
}

test('startup category failure retains authenticated editor shell and provides a working retry', async () => {
  let catalogCalls = 0;
  const c = client(call => call.path === 'settings' && ++catalogCalls === 1
    ? {status: 503, data: {error: '分类暂时不可用'}} : authenticatedFixture(call));
  await c.run('start()');
  assert.equal(c.state.authenticated, true);
  assert.equal(c.get('shell').classList.contains('hidden'), false);
  assert.equal(c.get('startup').classList.contains('hidden'), true);
  assert.equal(c.get('login').classList.contains('hidden'), true);
  assert.equal(c.get('startupAppError').classList.contains('hidden'), false);
  assert.match(c.get('startupAppErrorMessage').textContent, /请求失败，请稍后重试/);
  await c.get('startupAppRetry').onclick();
  assert.equal(c.state.post.id, 2);
  assert.equal(c.get('startupAppError').classList.contains('hidden'), true);
  assert.deepEqual(c.get('login').visibilityChanges, []);
});

test('startup article failure retries its requested article inside the authenticated editor', async () => {
  let articleCalls = 0;
  const c = client(call => call.path === 'post?id=2' && ++articleCalls === 1
    ? {status: 503, data: {error: '文章暂时不可用'}} : authenticatedFixture(call));
  await c.run('start()');
  assert.equal(c.state.authenticated, true);
  assert.equal(c.get('shell').classList.contains('hidden'), false);
  assert.equal(c.get('login').classList.contains('hidden'), true);
  assert.equal(c.get('startupAppError').classList.contains('hidden'), false);
  assert.match(c.get('startupAppErrorMessage').textContent, /请求失败，请稍后重试/);
  await c.get('startupAppRetry').onclick();
  assert.equal(c.state.post.id, 2);
  assert.equal(articleCalls, 2);
  assert.equal(c.get('startupAppError').classList.contains('hidden'), true);
  assert.deepEqual(c.get('login').visibilityChanges, []);
});

test('startup remains coalesced while the authenticated category request is still pending', async () => {
  let release;
  const c = client(call => call.path === 'settings' ? new Promise(resolve => release = resolve) : authenticatedFixture(call));
  const pending = c.run('start()');
  await new Promise(setImmediate);
  assert.equal(c.state.authenticated, true);
  assert.equal(c.get('startup').classList.contains('hidden'), true);
  assert.equal(c.run('start()'), pending);
  assert.deepEqual(c.calls.map(call => call.path), ['session', 'settings']);
  release({data: {categories: [], tags: []}});
  await pending;
  assert.equal(c.state.post.id, 2);
  assert.deepEqual(c.get('login').visibilityChanges, []);
});

test('startup retry session failure retains previously verified identity until a successful retry', async () => {
  let sessionCalls = 0, catalogCalls = 0;
  const c = client(call => {
    if (call.path === 'session' && ++sessionCalls === 2) return {status: 503, data: {error: '会话读取暂时失败'}};
    if (call.path === 'settings' && ++catalogCalls === 1) return {status: 503, data: {error: '分类暂时失败'}};
    return authenticatedFixture(call);
  });
  await c.run('start()');
  await c.get('startupAppRetry').onclick();
  assert.equal(c.state.authenticated, true);
  assert.equal(c.state.user, 'editor');
  assert.equal(c.get('startup').classList.contains('hidden'), true);
  assert.equal(c.get('shell').classList.contains('hidden'), false);
  assert.equal(c.get('startupAppError').classList.contains('hidden'), false);
  assert.match(c.get('startupAppErrorMessage').textContent, /请求失败，请稍后重试/);
  await c.get('startupAppRetry').onclick();
  assert.equal(c.state.post.id, 2);
  assert.deepEqual(c.get('login').visibilityChanges, []);
});

for (const endpoint of ['settings', 'post?id=2']) {
  test('startup genuine expiry at ' + endpoint + ' retains its requested editor through login', async () => {
    let sessionCalls = 0;
    const c = client(call => {
      if (call.path === 'session' && ++sessionCalls > 1) return {data: {authenticated: false, configured: true, csrf: 'expired-session-challenge'}};
      if (call.path === endpoint) return {status: 401, data: {error: '请先登录'}};
      return authenticatedFixture(call);
    });
    await c.run('start()');
    assert.equal(c.state.authenticated, false);
    assert.equal(c.state.resumeSession.user, 'editor');
    assert.equal(c.state.resumeSession.view, 'editor');
    assert.equal(c.state.pendingEditID, 2);
    assert.equal(c.get('login').classList.contains('hidden'), false);
    assert.equal(c.get('shell').classList.contains('hidden'), true);
    assert.equal(c.get('startup').classList.contains('hidden'), true);
    assert.equal(c.get('startupAppError').classList.contains('hidden'), true);
    assert.deepEqual(c.get('login').visibilityChanges, [true]);
  });
}

function nonEmbeddedStartupFailure(sessionResponse = authenticatedFixture({path: 'session'})) {
  let catalogCalls = 0, sessionCalls = 0;
  const c = client(call => {
    if (call.path === 'posts') return {data: []};
    if (call.path === 'session' && ++sessionCalls > 1) return typeof sessionResponse === 'function' ? sessionResponse() : sessionResponse;
    if (call.path === 'settings' && ++catalogCalls === 1) return {status: 503, data: {error: '分类暂时失败'}};
    return authenticatedFixture(call);
  });
  c.run("state.embed=false;location.search='';location.href='https://ganwen.test/admin/';");
  return c;
}

function newStartupDraft(c) {
  c.run("newDocument('post');state.mode='source';$('postTitle').value='New unsaved article';$('sourceEditor').value='<p>New unsaved body</p>';refreshEditorDirty();");
  return c.state.post;
}

function assertStartupDraft(c, post) {
  assert.equal(c.state.post, post);
  assert.equal(c.state.dirty, true);
  assert.equal(c.get('postTitle').value, 'New unsaved article');
  assert.equal(c.get('sourceEditor').value, '<p>New unsaved body</p>');
}

test('startup error becomes obsolete when the administrator creates or navigates to other content', async () => {
  const c = nonEmbeddedStartupFailure();
  await c.run('start()');
  assert.equal(c.get('startupAppError').classList.contains('hidden'), false);
  newStartupDraft(c);
  assert.equal(c.get('startupAppError').classList.contains('hidden'), true);
});

test('startup stale retry preserves a newly created draft instead of running first-load navigation', async () => {
  const c = nonEmbeddedStartupFailure();
  await c.run('start()');
  const retry = c.get('startupAppRetry').onclick;
  const post = newStartupDraft(c);
  c.context.window.confirm = () => {throw Error('retry must remain in the current editor');};
  await retry();
  assert.equal(c.state.view, 'editor');
  assert.equal(c.state.authenticated, true);
  assertStartupDraft(c, post);
});

test('startup retry uses the current view when a new draft is created during its session read', async () => {
  let release;
  const c = nonEmbeddedStartupFailure(() => new Promise(resolve => release = resolve));
  await c.run('start()');
  const pending = c.get('startupAppRetry').onclick();
  const post = newStartupDraft(c);
  release(authenticatedFixture({path: 'session'}));
  await pending;
  assert.equal(c.state.view, 'editor');
  assertStartupDraft(c, post);
});

test('startup retry expiry preserves a new draft and its editor destination through login', async () => {
  const c = nonEmbeddedStartupFailure({data: {authenticated: false, configured: true, csrf: 'expired-challenge'}});
  await c.run('start()');
  const retry = c.get('startupAppRetry').onclick;
  const post = newStartupDraft(c);
  await retry();
  assert.equal(c.state.authenticated, false);
  assert.equal(c.state.resumeSession.user, 'editor');
  assert.equal(c.state.resumeSession.view, 'editor');
  assert.equal(c.get('login').classList.contains('hidden'), false);
  assertStartupDraft(c, post);
});

test('startup retry account change blocks entry without discarding the previous account draft', async () => {
  const c = nonEmbeddedStartupFailure({data: {authenticated: true, configured: true, user: 'other-editor', csrf: 'other-account-session'}});
  await c.run('start()');
  const retry = c.get('startupAppRetry').onclick;
  const post = newStartupDraft(c);
  await retry();
  assert.equal(c.state.authenticated, false);
  assert.equal(c.state.user, 'editor');
  assert.equal(c.state.resumeSession.user, 'editor');
  assert.equal(c.get('login').classList.contains('hidden'), false);
  assert.match(c.get('loginError').textContent, /账号已变化/);
  assertStartupDraft(c, post);
});

function draft(c) {
  c.run("state.authenticated=true;state.user='editor';state.view='editor';state.returnView='pages';state.lang='nl';state.mode='source';state.post={id:2,type:'page',version:3,title:'saved title'};state.raw='<p>draft raw</p>';state.dirty=true;state.settingsDirty=true;state.editorReady=true;state.hash='source-hash';$('postTitle').value='unsaved title';$('sourceEditor').value='<p>unsaved source</p>';$('excerpt').value='unsaved excerpt';$('categories').value='draft category';$('translationReviewed').checked=true;$('visualEditor').srcdoc='editor document';$('siteTitle').value='unsaved site title';");
}

function assertDraft(c) {
  assert.equal(c.state.post.id, 2);
  assert.equal(c.state.post.version, 3);
  assert.equal(c.state.lang, 'nl');
  assert.equal(c.state.mode, 'source');
  assert.equal(c.state.dirty, true);
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.state.raw, '<p>draft raw</p>');
  assert.equal(c.state.hash, 'source-hash');
  assert.equal(c.get('postTitle').value, 'unsaved title');
  assert.equal(c.get('sourceEditor').value, '<p>unsaved source</p>');
  assert.equal(c.get('excerpt').value, 'unsaved excerpt');
  assert.equal(c.get('categories').value, 'draft category');
  assert.equal(c.get('translationReviewed').checked, true);
  assert.equal(c.get('visualEditor').srcdoc, 'editor document');
  assert.equal(c.get('visualEditor').contentDocument.body.innerHTML, '<p>unsaved visual draft</p>');
  assert.equal(c.get('siteTitle').value, 'unsaved site title');
}

test('expiry before saving retains all draft state and same-account login restores without reloading the article', async () => {
  const c = client(() => ({data: {authenticated: false, configured: true, csrf: 'fresh-login'}}));
  draft(c);
  await assert.rejects(c.run("api('translation',{id:2})"), /登录已过期/);
  assert.equal(c.calls.length, 1);
  assert.equal(c.calls[0].path, 'session');
  assert.equal(c.state.authenticated, false);
  assert.equal(c.get('shell').classList.contains('hidden'), true);
  assertDraft(c);
  await c.run("showApp('editor')");
  assert.equal(c.state.authenticated, true);
  assert.equal(c.state.resumeSession, null);
  assert.equal(c.calls.length, 1);
  assertDraft(c);
});

test('a rejected save never replays and a 401 preserves the draft through the login screen', async () => {
  let sessionCalls = 0;
  const c = client(({path}) => path === 'session'
    ? {data: ++sessionCalls === 1 ? {authenticated: true, user: 'editor', csrf: 'active'} : {authenticated: false, configured: true, csrf: 'login'}}
    : {status: 401, data: {error: '请先登录'}});
  draft(c);
  await assert.rejects(c.run("api('post',{id:2})"), /请先登录/);
  assert.equal(c.calls.filter(call => call.options.method === 'POST').length, 1);
  assert.equal(c.state.authenticated, false);
  assertDraft(c);
});

test('a real save 401 releases the visual lock and resumes the same retained draft after login', async () => {
  let sessions = 0;
  const c = client(({path}) => path === 'session'
    ? {data: ++sessions === 1 ? {authenticated: true, user: 'editor', csrf: 'active'} : {authenticated: false, configured: true, csrf: 'login'}}
    : {status: 401, data: {error: '请先登录'}});
  draft(c);
  c.run('normalizeInlineImages=async()=>{}');
  const body = c.get('visualEditor').contentDocument.body;
  const saving = c.run('saveEditor()');
  assert.equal(body.contentEditable, 'false');
  await saving;
  assert.equal(c.state.authenticated, false);
  assert.equal(c.state.saving, false);
  assert.equal(body.contentEditable, 'true');
  assert.equal(c.get('editorGrid').inert, false);
  assert.equal(c.get('savePostButton').disabled, false);
  assertDraft(c);
  await c.run("showApp('editor')");
  assert.equal(c.state.authenticated, true);
  assert.equal(body.contentEditable, 'true');
  assertDraft(c);
  assert.deepEqual(c.calls.map(call => call.path), ['session', 'translation', 'session']);
});

test('write refreshes another-tab CSRF first, while a 403 is shown once without retrying or clearing content', async () => {
  const c = client(({path, options}) => {
    if (path === 'session') return {data: {authenticated: true, user: 'editor', csrf: 'other-tab-token'}};
    assert.equal(options.headers['X-CSRF-Token'], 'other-tab-token');
    return {status: 403, data: {error: '请求来源或 CSRF 验证失败'}};
  });
  draft(c);
  c.state.csrf = 'stale-token';
  await assert.rejects(c.run("api('post',{id:2})"), /CSRF 验证失败/);
  assert.deepEqual(c.calls.map(call => call.path), ['session', 'post']);
  assert.equal(c.state.authenticated, true);
  assertDraft(c);
});

test('an identity change blocks the pending write and cannot restore a different account draft without confirmation', async () => {
  const c = client(() => ({data: {authenticated: true, configured: true, user: 'other-editor', csrf: 'different-user'}}));
  draft(c);
  await assert.rejects(c.run("api('post',{id:2})"), /账号已变化/);
  assert.equal(c.calls.length, 1);
  let confirmations = 0;
  c.context.window.confirm = () => {confirmations++; return false;};
  await c.run("showApp('other-editor')");
  assert.equal(confirmations, 1);
  assert.equal(c.state.authenticated, false);
  assert.equal(c.state.user, 'editor');
  assertDraft(c);
  c.context.window.confirm = () => true;
  c.run("state.embed=false;location.search='';loadPosts=async()=>{};loadTermCatalog=async()=>{};navigateAdmin=async()=>{};");
  await c.run("showApp('other-editor')");
  assert.equal(c.state.authenticated, true);
  assert.equal(c.state.user, 'other-editor');
  assert.equal(c.state.post, null);
  assert.equal(c.state.dirty, false);
  assert.equal(c.get('postTitle').value, '');
});

for (const remember of [false, true]) {
  test('stale login form refreshes its challenge and sends explicit remember=' + remember, async () => {
    const c = client(({path, options}) => {
      if (path === 'session') return {data: {authenticated: false, configured: true, csrf: 'new-login-challenge'}};
      assert.equal(path, 'login');
      assert.equal(options.headers['X-CSRF-Token'], 'new-login-challenge');
      assert.equal(JSON.parse(options.body).remember, remember);
      return {data: {authenticated: true, user: 'editor', csrf: 'new-session'}};
    });
    draft(c);
    c.state.authenticated = false;
    c.state.resumeSession = {user: 'editor', view: 'editor'};
    c.state.csrf = 'expired-login-challenge';
    c.get('username').value = 'editor';
    c.get('password').value = 'test-only-password';
    c.get('rememberLogin').checked = remember;
    await c.run('submitLogin({preventDefault(){}})');
    assert.deepEqual(c.calls.map(call => call.path), ['session', 'login']);
    assert.equal(c.state.authenticated, true);
    assert.equal(c.state.csrf, 'new-session');
    assert.equal(c.get('password').value, '');
    assertDraft(c);
  });
}

test('explicit logout discards the draft only after canLeave accepts it', async () => {
  const c = client(({path}) => ({data: path === 'session' ? {authenticated: true, configured: true, user: 'editor', csrf: 'session'} : {ok: true}}));
  draft(c);
  c.context.window.confirm = () => false;
  await c.run("$('logoutButton').onclick()");
  assert.equal(c.calls.length, 0);
  assertDraft(c);
  c.context.window.confirm = () => true;
  await c.run("$('logoutButton').onclick()");
  assert.equal(c.state.post, null);
  assert.equal(c.state.resumeSession, null);
  assert.equal(c.state.dirty, false);
  assert.equal(c.state.authenticated, false);
});

test('remember-login starts unchecked and retained drafts still trigger the leave-page warning', () => {
  assert.match(html, /<input id="rememberLogin" type="checkbox">/);
  const c = client(() => ({data: {}}));
  draft(c);
  c.state.authenticated = false;
  let prevented = false;
  const event = {preventDefault() {prevented = true;}};
  c.events.beforeunload(event);
  assert.equal(prevented, true);
  assert.equal(event.returnValue, '');
});

test('a temporary session-store failure leaves the editor authenticated and its draft intact', async () => {
  const c = client(() => ({status: 503, data: {error: '暂时无法读取登录状态，请稍后重试'}}));
  draft(c);
  await assert.rejects(c.run("api('post',{id:2})"), /暂时无法/);
  assert.equal(c.calls.length, 1);
  assert.equal(c.state.authenticated, true);
  assert.equal(c.state.resumeSession, null);
  assertDraft(c);
});

test('another-tab login reconnects the same account without asking for a password or reloading its draft', async () => {
  const c = client(() => ({data: {authenticated: true, user: 'editor', csrf: 'other-tab'}}));
  draft(c);
  c.state.authenticated = false;
  c.state.resumeSession = {user: 'editor', view: 'editor'};
  await c.run('reconnectAdminSession()');
  assert.equal(c.state.authenticated, true);
  assert.equal(c.state.csrf, 'other-tab');
  assert.deepEqual(c.calls.map(call => call.path), ['session']);
  assertDraft(c);
});

test('footer settings distinguish default, custom and hidden without calling a translation service', async () => {
  const c = client(() => {throw Error('Automatic translation must not run');});
  c.run("applyFooterSettings({title:'甘文',footer_text:null});");
  assert.equal(c.get('footerDefault').checked, true);
  assert.equal(c.get('footerText').value, '© 甘文');
  assert.equal(c.run('footerSetting()'), null);
  assert.equal(c.get('footerLanguage').children.length, 10);
  c.get('footerText').value = '自定义页脚';
  c.get('footerText').oninput();
  assert.equal(c.run('footerSetting()'), '自定义页脚');
  c.get('footerText').value = '';
  c.get('footerText').oninput();
  assert.equal(c.run('footerSetting()'), '');
  assert.equal(c.get('footerTranslateButton').disabled, true);
  assert.equal(c.calls.length, 0);
});

test('footer hashes preserve raw whitespace and old translations stay stale until explicitly confirmed', async () => {
  const c = client(() => {throw Error('No translation requested');});
  const raw = ' 页脚\n文字 ', expected = createHash('sha256').update(raw, 'utf8').digest('hex');
  c.context.testFooter = raw;
  c.run("applyFooterSettings({title:'甘文',footer_text:testFooter});");
  await c.run('refreshFooterSource()');
  assert.equal(c.state.footerCurrentHash, expected);
  assert.equal(c.run('footerSetting()'), raw);
  c.get('footerTranslation').value = 'Original footer translation';
  c.get('footerTranslation').oninput();
  assert.equal(c.state.footerDrafts.en.source_hash, expected);
  c.get('footerText').value = '已经修改的页脚';
  await c.run('refreshFooterSource()');
  assert.equal(c.run('footerDraftIsCurrent(state.footerDrafts.en)'), false);
  const before = await c.run('collectFooterTranslations()');
  assert.equal(before.en.source_hash, expected);
  assert.match(c.get('footerTranslationStatus').textContent, /原文已变化/);
  c.get('footerConfirmButton').onclick();
  assert.equal(c.run('footerDraftIsCurrent(state.footerDrafts.en)'), true);
  const after = await c.run('collectFooterTranslations()');
  assert.notEqual(after.en.source_hash, expected);
  assert.equal(c.calls.length, 0);
});

test('AI footer translation runs only on its button and remains an unsaved draft', async () => {
  const c = client(({path, options}) => {
    if (path === 'session') return {data: {authenticated: true, user: 'editor', csrf: 'csrf'}};
    assert.equal(path, 'footer/translate');
    assert.deepEqual(JSON.parse(options.body), {footer_text: '页脚', language: 'en'});
    return {data: {text: 'Footer', source_hash: createHash('sha256').update('页脚').digest('hex')}};
  });
  c.run("state.authenticated=true;state.user='editor';applyFooterSettings({title:'甘文',footer_text:'页脚'});");
  assert.equal(c.calls.length, 0);
  await c.get('footerTranslateButton').onclick();
  assert.deepEqual(c.calls.map(call => call.path), ['session', 'footer/translate']);
  assert.equal(c.get('footerTranslation').value, 'Footer');
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.state.footerTranslating, false);
});

test('an in-flight AI translation cannot overwrite manual changes', async () => {
  const c = client(() => {throw Error('Use a controlled request');});
  c.run("state.authenticated=true;state.user='editor';applyFooterSettings({title:'甘文',footer_text:'页脚'});let finishFooter;api=async()=>new Promise(resolve=>finishFooter=resolve);");
  const pending = c.run('translateFooter()');
  c.get('footerTranslation').value = 'My manual footer';
  c.get('footerTranslation').oninput();
  c.run("finishFooter({text:'Automatic footer',source_hash:'hash'})");
  await pending;
  assert.equal(c.get('footerTranslation').value, 'My manual footer');
  assert.match(c.get('toast').textContent, /未覆盖/);
});

test('a delayed settings save locks inputs, prevents duplicate saves and releases the form after its response', async () => {
  let finish, posts = 0;
  const c = client(({path, options}) => {
    if (path === 'session') return {data: {authenticated: true, user: 'editor', csrf: 'csrf'}};
    assert.equal(path, 'settings');
    posts++;
    const submitted = JSON.parse(options.body);
    return new Promise(resolve => {finish = () => resolve({data: submitted});});
  });
  c.run("state.authenticated=true;state.user='editor';applySiteSettings({title:'原站名',base_url:'https://ganwen.test',posts_per_page:10,footer_text:'页脚'});$('siteTitle').value='新站名';state.settingsDirty=true;");
  const pending = c.run("$('settingsForm').onsubmit({preventDefault(){}})");
  assert.equal(c.state.settingsSaving, true);
  assert.equal(c.get('settingsForm').inert, true);
  assert.equal(c.get('settingsSaveButton').disabled, true);
  assert.equal(c.run('canLeave()'), false);
  await c.run("$('settingsForm').onsubmit({preventDefault(){}})");
  for (let i = 0; !finish && i < 20; i++) await new Promise(resolve => setImmediate(resolve));
  assert.equal(posts, 1);
  assert.equal(typeof finish, 'function');
  finish();
  await pending;
  assert.equal(c.get('siteTitle').value, '新站名');
  assert.equal(c.state.settingsDirty, false);
  assert.equal(c.state.settingsSaving, false);
  assert.equal(c.get('settingsForm').inert, false);
  assert.equal(c.get('settingsSaveButton').disabled, false);
});

test('failed settings save unlocks the form and keeps pending input', async () => {
  const c = client(({path}) => path === 'session' ? {data: {authenticated: true, user: 'editor', csrf: 'csrf'}} : {status: 503, data: {error: '暂时无法保存'}});
  c.run("state.authenticated=true;state.user='editor';applySiteSettings({title:'原站名',posts_per_page:10,footer_text:''});$('siteTitle').value='未保存站名';state.settingsDirty=true;");
  await c.run("$('settingsForm').onsubmit({preventDefault(){}})");
  assert.equal(c.get('siteTitle').value, '未保存站名');
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.state.settingsSaving, false);
  assert.equal(c.get('settingsForm').inert, false);
  assert.equal(c.get('settingsSaveButton').disabled, false);
});

test('login expiry while loading a different article resumes the requested ID rather than leaving a blank editor', async () => {
  let articleCalls = 0;
  const c = client(({path}) => {
    if (path === 'session') return {data: {authenticated: false, configured: true, csrf: 'login'}};
    assert.equal(path, 'post?id=17');
    if (++articleCalls === 1) return {status: 401, data: {error: '请先登录'}};
    return {data: {post: {id: 17, type: 'post', title: 'Requested article', content_html: '<p>Content</p>', status: 'publish', translations: {}}, source_hash: 'post-hash'}};
  });
  c.run("state.authenticated=true;state.user='editor';state.view='posts';state.termCatalogReady=true;");
  await c.run('openPost(17)');
  assert.equal(c.state.post, null);
  assert.equal(c.state.pendingEditID, 17);
  assert.equal(c.state.authenticated, false);
  await c.run("showApp('editor')");
  assert.equal(c.state.authenticated, true);
  assert.equal(c.state.post.id, 17);
  assert.equal(c.state.pendingEditID, 0);
  assert.equal(c.get('postTitle').value, 'Requested article');
  assert.equal(c.get('editorLoadError').classList.contains('hidden'), true);
  assert.equal(articleCalls, 2);
});

test('login expiry before settings have loaded retries the settings read after login', async () => {
  let settingsCalls = 0;
  const c = client(({path}) => {
    if (path === 'session') return {data: {authenticated: false, configured: true, csrf: 'login'}};
    assert.equal(path, 'settings');
    if (++settingsCalls === 1) return {status: 401, data: {error: '请先登录'}};
    return {data: {title: 'Loaded settings', posts_per_page: 10, footer_text: 'Footer'}};
  });
  c.run("state.authenticated=true;state.user='editor';state.view='settings';");
  await assert.rejects(c.run('loadSettings()'), /请先登录/);
  assert.equal(c.state.settings, null);
  await c.run("showApp('editor')");
  assert.equal(c.state.authenticated, true);
  assert.equal(c.get('siteTitle').value, 'Loaded settings');
  assert.equal(settingsCalls, 2);
});

test('an embedded editor recovers even when its first category-catalog request expires', async () => {
  let catalogCalls = 0;
  const c = client(({path}) => {
    if (path === 'session') return {data: {authenticated: false, configured: true, csrf: 'login'}};
    if (path === 'settings') return ++catalogCalls === 1 ? {status: 401, data: {error: '请先登录'}} : {data: {categories: [], tags: []}};
    assert.equal(path, 'post?id=2');
    return {data: {post: {id: 2, type: 'page', title: 'Embedded page', status: 'publish', content_html: '', translations: {}}, source_hash: 'hash'}};
  });
  await assert.rejects(c.run("showApp('editor')"), /请先登录/);
  assert.equal(c.state.resumeSession.view, 'editor');
  assert.equal(c.state.pendingEditID, 2);
  await c.run("showApp('editor')");
  assert.equal(c.state.post.id, 2);
  assert.equal(c.state.termCatalogReady, true);
  assert.equal(catalogCalls, 2);
  assert.equal(c.get('editorLoadError').classList.contains('hidden'), true);
});

test('site default layout supports both choices, tracks changes and retains the saved selection', async () => {
  let saved;
  const c = client(({path, options}) => {
    if (path === 'session') return {data: {authenticated: true, user: 'editor', csrf: 'csrf'}};
    assert.equal(path, 'settings');
    saved = JSON.parse(options.body);
    return {data: saved};
  });
  assert.match(html, /<select id="defaultLayout"><option value="journal"[^>]*>日志阅读<\/option><option value="classic"[^>]*>经典卡片<\/option><\/select>/);
  c.run("state.authenticated=true;state.user='editor';applySiteSettings({title:'甘文',posts_per_page:10,default_layout:''});");
  assert.equal(c.get('defaultLayout').value, 'journal');
  assert.equal(c.state.settingsDirty, false);
  c.get('defaultLayout').value = 'classic';
  c.get('settingsForm').oninput();
  assert.equal(c.state.settingsDirty, true);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(saved.default_layout, 'classic');
  assert.equal(c.get('defaultLayout').value, 'classic');
  assert.equal(c.state.settingsDirty, false);
  c.get('defaultLayout').value = 'journal';
  c.get('settingsForm').oninput();
  assert.equal(c.state.settingsDirty, true);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(saved.default_layout, 'journal');
  assert.equal(c.get('defaultLayout').value, 'journal');
  assert.equal(c.state.settingsDirty, false);
});

test('sidebar advertisements default off and preserve multi-item configuration across save, disable, remove and reread', async () => {
  let saved, submitted;
  const c = client(({path, options}) => {
    if (path === 'session') return {data: {authenticated: true, user: 'editor', csrf: 'csrf'}};
    assert.equal(path, 'settings');
    submitted = JSON.parse(options.body);
    saved = {...submitted, sidebar_ads: {...submitted.sidebar_ads, interval_seconds: 12}};
    return {data: saved};
  });
  c.run("state.authenticated=true;state.user='editor';applySiteSettings({title:'甘文',posts_per_page:10,sidebar_ads:{enabled:false,interval_seconds:12,items:[]}});");
  assert.equal(c.get('adEnabled').checked, false);
  assert.doesNotMatch(html, /\bid="adInterval"|轮播间隔/);
  assert.match(html, /按下方顺序每次刷新换下一条/);
  assert.equal(c.state.adItems.length, 0);
  assert.equal(c.state.settingsDirty, false);
  assert.equal(c.calls.length, 0, 'empty advertisement settings must not fetch images or APIs');
  for (const values of [
    {title: 'First <plain> advertisement', description: 'Plain description\nSecond line <text>', image_url: '/wp-content/uploads/ad-one.png', target_url: 'https://example.com/one', button_label: 'Learn more', sponsor: 'First sponsor'},
    {title: 'Second advertisement', description: '', image_url: 'https://images.example/ad-two.webp', target_url: 'https://example.com/two?campaign=blog', button_label: '', sponsor: ''}
  ]) {
    c.get('addAdButton').onclick();
    const row = c.get('adFields').children.at(-1), fields = row.children[0].children;
    for (const field of fields) {
      const input = field.children[1];
      const key = input.id.replace(/^ad-/, '').replace(/-\d+$/, '');
      input.value = values[key];
      input.oninput();
    }
    assert.equal(fields[1].children[1].rows, 3);
    assert.equal(fields[1].children[1].maxLength, 500);
    assert.equal(fields[4].children[1].maxLength, 40);
    assert.equal(fields[5].children[1].maxLength, 80);
  }
  c.get('adEnabled').checked = true;
  c.get('settingsForm').oninput();
  assert.equal(c.state.settingsDirty, true);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(saved.sidebar_ads.enabled, true);
  assert.equal(saved.sidebar_ads.interval_seconds, 12);
  assert.equal(Object.hasOwn(submitted.sidebar_ads, 'interval_seconds'), false, 'new UI must leave the legacy interval to the server');
  assert.equal(saved.sidebar_ads.items.length, 2);
  assert.equal(saved.sidebar_ads.items[0].title, 'First <plain> advertisement');
  assert.equal(saved.sidebar_ads.items[0].description, 'Plain description\nSecond line <text>');
  assert.equal(saved.sidebar_ads.items[0].button_label, 'Learn more');
  assert.equal(saved.sidebar_ads.items[0].sponsor, 'First sponsor');
  assert.equal(c.state.settingsDirty, false);
  assert.equal(c.state.adItems[1].image_url, 'https://images.example/ad-two.webp');
  assert.equal(c.get('adFields').children[0].titleInput.value, 'First <plain> advertisement');
  assert.equal(c.get('adFields').children[0].children[0].children[1].children[1].value, 'Plain description\nSecond line <text>');
  c.get('adEnabled').checked = false;
  c.get('settingsForm').oninput();
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(saved.sidebar_ads.enabled, false);
  assert.equal(saved.sidebar_ads.items.length, 2, 'disabling the slot must retain its items');
  c.get('adFields').children[0].children[1].children[0].onclick();
  assert.equal(c.state.settingsDirty, true);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(saved.sidebar_ads.items.length, 1);
  assert.equal(saved.sidebar_ads.items[0].title, 'Second advertisement');
  c.get('adFields').children[0].children[1].children[0].onclick();
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.deepEqual(saved.sidebar_ads.items, []);
  assert.equal(c.state.settingsDirty, false);
  assert.equal(c.get('adEnabled').checked, false);
  assert.equal(c.state.settings.sidebar_ads.interval_seconds, 12);
});

test('sidebar advertisement failed save retains its draft and account cleanup clears all advertisement fields', async () => {
  const c = client(({path}) => path === 'session' ? {data: {authenticated: true, user: 'editor', csrf: 'csrf'}} : {status: 503, data: {error: '暂时无法保存'}});
  c.run("state.authenticated=true;state.user='editor';applySiteSettings({title:'甘文',posts_per_page:10,sidebar_ads:{enabled:false,interval_seconds:8,items:[{title:'Saved advertisement',description:'Saved description',image_url:'/wp-content/uploads/ad.png',target_url:'https://example.com/',button_label:'Read more',sponsor:'Saved sponsor'}]}});");
  const title = c.get('adFields').children[0].titleInput;
  title.value = 'Unsaved advertisement';
  title.oninput();
  const description = c.get('adFields').children[0].children[0].children[1].children[1];
  description.value = 'Unsaved description\nSecond line';
  description.oninput();
  c.get('adEnabled').checked = true;
  c.get('settingsForm').oninput();
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.state.adItems[0].title, 'Unsaved advertisement');
  assert.equal(c.state.adItems[0].description, 'Unsaved description\nSecond line');
  assert.equal(c.state.adItems[0].button_label, 'Read more');
  assert.equal(c.state.adItems[0].sponsor, 'Saved sponsor');
  assert.equal(c.get('adEnabled').checked, true);
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.state.settingsSaving, false);
  assert.equal(c.get('settingsForm').inert, false);
  c.run('clearSessionContent()');
  assert.equal(c.state.adItems.length, 0);
  assert.equal(c.get('adFields').children.length, 0);
  assert.equal(c.get('adEnabled').checked, false);
  assert.equal(c.state.settings, null);
  assert.equal(c.state.settingsDirty, false);
});

test('sidebar advertisements read older minimal items and keep optional text in the dirty snapshot', () => {
  const c = client(() => {throw Error('reading advertisements must not fetch media or APIs');});
  c.run("state.authenticated=true;state.user='editor';applySiteSettings({title:'甘文',posts_per_page:10,sidebar_ads:{enabled:false,interval_seconds:120,items:[{title:'Old advertisement',image_url:'/wp-content/uploads/ad.png',target_url:'https://example.com/'}]}});");
  assert.equal(c.state.adItems[0].description, '');
  assert.equal(c.state.adItems[0].button_label, '');
  assert.equal(c.state.adItems[0].sponsor, '');
  assert.equal(c.state.settingsDirty, false);
  c.state.settings.sidebar_ads.interval_seconds = 3;
  c.run('refreshSettingsDirty()');
  assert.equal(c.state.settingsDirty, false, 'legacy interval must not enter the editable snapshot');
  for (const index of [1, 4, 5]) {
    const input = c.get('adFields').children[0].children[0].children[index].children[1];
    input.value = 'New plain text';
    input.oninput();
    assert.equal(c.state.settingsDirty, true);
    input.value = '';
    input.oninput();
    assert.equal(c.state.settingsDirty, false);
  }
  assert.equal(c.calls.length, 0);
});

test('sidebar advertisements validate optional text limits before sending a save', async () => {
  for (const [index, limit, label] of [[1, 500, '说明'], [4, 40, '按钮文字'], [5, 80, '推广来源']]) {
    const c = client(() => {throw Error('overlong advertisement text must not issue a save');});
    c.run("state.authenticated=true;state.user='editor';applySiteSettings({title:'甘文',posts_per_page:10,sidebar_ads:{items:[{title:'Ad',image_url:'/wp-content/uploads/ad.png',target_url:'https://example.com/'}]}});");
    const input = c.get('adFields').children[0].children[0].children[index].children[1];
    input.value = '文'.repeat(limit + 1);
    input.oninput();
    await c.get('settingsForm').onsubmit({preventDefault() {}});
    assert.equal(c.calls.length, 0);
    assert.equal(c.get('toast').textContent, '广告' + label + '最多 ' + limit + ' 字');
    assert.equal(c.state.settingsDirty, true);
  }
});

test('sidebar advertisements reject incomplete drafts before sending a save and limit editable rows', async () => {
  const c = client(() => {throw Error('incomplete advertisements must not issue a save');});
  c.run("state.authenticated=true;state.user='editor';applySiteSettings({title:'甘文',posts_per_page:10});");
  c.get('addAdButton').onclick();
  const title = c.get('adFields').children[0].titleInput;
  title.value = 'Incomplete advertisement';
  title.oninput();
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.calls.length, 0);
  assert.match(c.get('toast').textContent, /标题、图片地址和目标网址/);
  assert.equal(c.state.settingsDirty, true);
  for (let i = 0; i < 12; i++) c.get('addAdButton').onclick();
  assert.equal(c.state.adItems.length, 12);
  assert.equal(c.get('addAdButton').disabled, true);
  assert.match(c.get('toast').textContent, /最多 12 条/);
});
