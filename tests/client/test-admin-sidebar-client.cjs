'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const harness = fs.readFileSync(path.join(__dirname, 'test-admin-session-client.cjs'), 'utf8');
const harnessRequire = id => id === 'node:test' ? {test() {}} : require(id);
const {client} = new Function('require', '__dirname', harness + '\nreturn {client};')(harnessRequire, __dirname);
const html = fs.readFileSync(path.join(__dirname, '../../internal/blog/web/admin/admin_ui.html'), 'utf8');
const modules = ['search', 'popular', 'stats', 'archives', 'categories', 'tags'];
const sidebar = {modules, article_sidebar_enabled: false, sidebar_ads: {enabled: false, items: []}};
const session = {authenticated: true, configured: true, user: 'editor', csrf: 'sidebar-fixture'};

function setup(respond, saved = sidebar) {
  const c = client(respond, '');
  c.context.fixtureSidebar = saved;
  c.run("state.authenticated=true;state.user='editor';state.view='sidebar';applySidebarSettings(fixtureSidebar)");
  return c;
}

function deferredReads() {
  const pending = [];
  return {pending, respond: () => new Promise(resolve => pending.push(resolve))};
}

async function waitFor(predicate) {
  for (let i = 0; !predicate() && i < 40; i++) await new Promise(resolve => setImmediate(resolve));
  assert.ok(predicate(), 'the expected request did not start');
}

test('the sidebar page owns module choices, article visibility and advertisements', () => {
  const section = html.match(/<section id="view-sidebar"[\s\S]*?<\/section>/)[0];
  const settingsSection = html.match(/<section id="view-settings"[\s\S]*?<\/section>/)[0];
  assert.match(html, /data-view="sidebar"/);
  for (const id of ['articleSidebarEnabled', 'adEnabled', 'adFields', 'sidebarSaveButton', ...modules.map(key => 'sidebarModule-' + key)]) {
    assert.ok(section.includes('id="' + id + '"'), id + ' must belong to the sidebar page');
    assert.ok(!settingsSection.includes('id="' + id + '"'), id + ' must not remain in site settings');
  }
  assert.match(section, /id="sidebarPreviewLink" href="\/" target="_blank" rel="noopener"/);
  assert.doesNotMatch(settingsSection, /id="publicStatsEnabled"/);
});

test('missing module settings enable every module while an empty list disables all', () => {
  const c = setup(() => {throw Error('editing must not write');}, {sidebar_ads: {items: []}});
  assert.deepEqual(Array.from(c.run('collectSidebarModules()')), modules);
  assert.equal(c.get('articleSidebarEnabled').checked, false);
  c.context.fixtureSidebar = {...sidebar, modules: []};
  c.run('applySidebarSettings(fixtureSidebar)');
  assert.deepEqual(Array.from(c.run('collectSidebarModules()')), []);
  c.get('sidebarModule-archives').checked = true;
  c.get('sidebarForm').onchange();
  assert.equal(c.state.sidebarDirty, true);
  assert.equal(c.state.settingsDirty, false);
  c.get('sidebarModule-archives').checked = false;
  c.get('sidebarForm').oninput();
  assert.equal(c.state.sidebarDirty, false);
  c.get('articleSidebarEnabled').checked = true;
  c.get('sidebarForm').oninput();
  assert.equal(c.state.sidebarDirty, true);
});

test('sidebar saving uses PUT and only sends its three configuration fields', async () => {
  const submitted = [];
  const c = setup(call => {
    if (call.path === 'session') return {data: session};
    assert.equal(call.path, 'sidebar');
    assert.equal(call.options.method, 'PUT');
    assert.equal(call.options.headers['X-CSRF-Token'], session.csrf);
    assert.equal(call.options.credentials, 'same-origin');
    submitted.push(JSON.parse(call.options.body));
    return {data: submitted.at(-1)};
  });
  for (const enabled of [true, false]) {
    c.get('articleSidebarEnabled').checked = enabled;
    for (const key of modules) c.get('sidebarModule-' + key).checked = false;
    c.get('sidebarForm').oninput();
    await c.get('sidebarForm').onsubmit({preventDefault() {}});
    assert.deepEqual(Object.keys(submitted.at(-1)).sort(), ['article_sidebar_enabled', 'modules', 'sidebar_ads']);
    assert.deepEqual(submitted.at(-1).modules, []);
    assert.equal(c.state.sidebar.article_sidebar_enabled, enabled);
    assert.equal(c.state.sidebarDirty, false);
  }
});

test('sidebar saves lock navigation and duplicate submission until the result is applied', async () => {
  let release;
  const c = setup(call => call.path === 'session' ? {data: session} : new Promise(resolve => {release = () => resolve({data: JSON.parse(call.options.body)});}));
  c.get('sidebarModule-search').checked = false;
  c.get('sidebarForm').oninput();
  const save = c.get('sidebarForm').onsubmit({preventDefault() {}});
  assert.equal(c.get('sidebarForm').inert, true);
  assert.equal(c.get('sidebarSaveButton').disabled, true);
  assert.equal(c.run('canLeave()'), false);
  await c.get('sidebarForm').onsubmit({preventDefault() {}});
  await waitFor(() => release);
  assert.equal(c.calls.filter(call => call.options.method === 'PUT').length, 1);
  release();
  await save;
  assert.equal(c.state.sidebarDirty, false);
  assert.equal(c.get('sidebarForm').inert, false);
});

test('a rejected sidebar save preserves the module and article drafts', async () => {
  const c = setup(call => call.path === 'session' ? {data: session} : {status: 503, data: {error: '保存失败'}});
  c.get('sidebarModule-archives').checked = false;
  c.get('articleSidebarEnabled').checked = true;
  c.get('sidebarForm').oninput();
  await c.get('sidebarForm').onsubmit({preventDefault() {}});
  assert.equal(c.get('sidebarModule-archives').checked, false);
  assert.equal(c.get('articleSidebarEnabled').checked, true);
  assert.equal(c.state.sidebar.article_sidebar_enabled, false);
  assert.equal(c.state.sidebarDirty, true);
  assert.equal(c.get('sidebarForm').inert, false);
  assert.equal(c.get('sidebarSaveButton').disabled, false);
});

test('sidebar drafts warn on navigation and unload, and accepted navigation discards them', async () => {
  const c = setup(() => {throw Error('navigation must not write');});
  c.get('sidebarModule-archives').checked = false;
  c.get('sidebarForm').oninput();
  let confirmations = 0;
  c.context.window.confirm = () => {confirmations++; return false;};
  await c.run("navigateAdmin('posts')");
  assert.equal(c.state.view, 'sidebar');
  assert.equal(confirmations, 1);
  const unload = {preventDefault() {this.prevented = true;}};
  c.events.beforeunload(unload);
  assert.equal(unload.prevented, true);
  c.context.window.confirm = () => true;
  await c.run("navigateAdmin('posts')");
  assert.equal(c.state.view, 'posts');
  assert.equal(c.state.sidebarDirty, false);
  assert.equal(c.get('sidebarModule-archives').checked, true);
});

test('sidebar reads cannot overwrite edits made while pending', async () => {
  const {pending, respond} = deferredReads(), c = setup(respond);
  const read = c.run('loadSidebar()');
  c.get('sidebarModule-tags').checked = false;
  c.get('sidebarForm').oninput();
  pending[0]({data: sidebar});
  await read;
  assert.equal(c.get('sidebarModule-tags').checked, false);
  assert.equal(c.state.sidebarDirty, true);
  assert.equal(c.get('sidebarForm').inert, false);
});

test('only the latest sidebar read may update its page and unlock its form', async () => {
  const {pending, respond} = deferredReads(), c = setup(respond);
  const old = c.run('loadSidebar()');
  c.run("showView('posts');showView('sidebar')");
  const latest = c.run('loadSidebar()');
  await c.get('sidebarForm').onsubmit({preventDefault() {}});
  assert.equal(c.calls.length, 2, 'saving must stay disabled during a read');
  pending[0]({data: {...sidebar, modules: []}});
  await old;
  assert.equal(c.get('sidebarForm').inert, true);
  pending[1]({data: {...sidebar, modules: ['archives']}});
  await latest;
  assert.deepEqual(Array.from(c.run('collectSidebarModules()')), ['archives']);
  assert.equal(c.get('sidebarForm').inert, false);
});

test('sidebar responses from a logged-out account cannot restore discarded configuration', async () => {
  const {pending, respond} = deferredReads(), c = setup(respond);
  const read = c.run('loadSidebar()');
  c.context.anonymous = {authenticated: false, configured: true, csrf: 'anonymous-fixture'};
  await c.run('showLogin(false, anonymous)');
  pending[0]({data: sidebar});
  await read;
  assert.equal(c.state.sidebar, null);
  assert.equal(c.state.sidebarDirty, false);
  assert.equal(c.get('sidebarForm').inert, true);
});

test('a failed initial sidebar read cannot enable default-value saves', async () => {
  const c = client(() => ({status: 503, data: {error: 'temporarily unavailable'}}), '');
  c.state.authenticated = true;
  c.state.user = 'editor';
  await assert.rejects(c.run('loadSidebar()'), /请求失败，请稍后重试/);
  assert.equal(c.get('sidebarForm').inert, true);
  assert.equal(c.get('sidebarSaveButton').disabled, true);
  await c.get('sidebarForm').onsubmit({preventDefault() {}});
  assert.equal(c.calls.length, 1);
});

test('expired sidebar saving retains its draft through same-account login without replaying it', async () => {
  const c = setup(() => ({data: {authenticated: false, configured: true, csrf: 'expired-session'}}));
  c.get('sidebarModule-stats').checked = false;
  c.get('articleSidebarEnabled').checked = true;
  c.get('sidebarForm').oninput();
  await c.get('sidebarForm').onsubmit({preventDefault() {}});
  assert.equal(c.state.authenticated, false);
  assert.equal(c.state.resumeSession.view, 'sidebar');
  assert.equal(c.get('sidebarForm').inert, true);
  await c.run("showApp('editor')");
  assert.equal(c.state.view, 'sidebar');
  assert.equal(c.get('sidebarModule-stats').checked, false);
  assert.equal(c.get('articleSidebarEnabled').checked, true);
  assert.equal(c.state.sidebarDirty, true);
  assert.equal(c.get('sidebarForm').inert, false);
  assert.equal(c.calls.length, 1);
});

test('site settings reads and saves leave independent sidebar drafts and visibility untouched', async () => {
  let submitted;
  const c = setup(call => call.path === 'session' ? {data: session} : {data: submitted = JSON.parse(call.options.body)});
  c.context.fixtureSettings = {title: 'Site', posts_per_page: 10, article_sidebar_enabled: false, sidebar_modules: ['search'], sidebar_ads: {enabled: false, items: []}, public_stats_enabled: true};
  c.get('sidebarModule-archives').checked = false;
  c.get('articleSidebarEnabled').checked = true;
  c.get('sidebarForm').oninput();
  c.run('applySiteSettings(fixtureSettings)');
  assert.equal(c.get('sidebarModule-archives').checked, false);
  assert.equal(c.get('articleSidebarEnabled').checked, true);
  assert.equal(c.state.sidebarDirty, true);
  c.get('siteTitle').value = 'Updated site';
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  for (const key of ['sidebar_modules', 'modules', 'sidebar_ads', 'article_sidebar_enabled', 'public_stats_enabled']) assert.equal(Object.hasOwn(submitted, key), false, 'site saving must omit ' + key);
  assert.equal(c.state.sidebarDirty, true);
});

test('the sidebar deep link loads its own configuration after login', async () => {
  const c = client(call => {
    assert.equal(call.path, 'sidebar');
    return {data: {...sidebar, modules: ['archives']}};
  }, '?view=sidebar');
  c.run('loadPosts=async()=>{};loadTermCatalog=async()=>{}');
  await c.run("showApp('editor')");
  assert.equal(c.state.view, 'sidebar');
  assert.deepEqual(Array.from(c.run('collectSidebarModules()')), ['archives']);
});
