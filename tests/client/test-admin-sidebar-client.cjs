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
const defaultOrder = ['search', 'popular', 'stats', 'archives', 'ads', 'categories', 'tags'];
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

test('sidebar saving uses PUT and only sends its five configuration fields', async () => {
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
    assert.deepEqual(Object.keys(submitted.at(-1)).sort(), ['article_sidebar_enabled', 'custom_order_enabled', 'modules', 'order', 'sidebar_ads']);
    assert.deepEqual(submitted.at(-1).modules, []);
    assert.equal(submitted.at(-1).custom_order_enabled, false);
    assert.deepEqual(submitted.at(-1).order, defaultOrder);
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
  c.context.fixtureSettings = {title: 'Site', posts_per_page: 10, article_sidebar_enabled: false, sidebar_modules: ['search'], sidebar_ads: {enabled: false, items: []}, public_stats_enabled: true, sidebar_custom_order_enabled: false, sidebar_order: defaultOrder, custom_order_enabled: false, order: defaultOrder};
  c.get('sidebarModule-archives').checked = false;
  c.get('articleSidebarEnabled').checked = true;
  c.get('sidebarForm').oninput();
  c.run('applySiteSettings(fixtureSettings)');
  assert.equal(c.get('sidebarModule-archives').checked, false);
  assert.equal(c.get('articleSidebarEnabled').checked, true);
  assert.equal(c.state.sidebarDirty, true);
  c.get('siteTitle').value = 'Updated site';
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  for (const key of ['sidebar_modules', 'modules', 'sidebar_ads', 'article_sidebar_enabled', 'public_stats_enabled', 'sidebar_custom_order_enabled', 'sidebar_order', 'custom_order_enabled', 'order']) assert.equal(Object.hasOwn(submitted, key), false, 'site saving must omit ' + key);
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

function enableCustomOrder(c, enabled = true) {
  c.get('sidebarCustomOrderEnabled').checked = enabled;
  c.get('sidebarCustomOrderEnabled').onchange();
}

function shownOrder(c) {
  return c.get('sidebarOrderList').children.map(row => row.dataset.module);
}

test('older sidebar responses default to system ordering with all seven sortable components', () => {
  const c = setup(() => {throw Error('reading ordering must not write');});
  assert.equal(c.get('sidebarCustomOrderEnabled').checked, false);
  assert.equal(c.get('sidebarDefaultOrderNotice').classList.contains('hidden'), false);
  assert.equal(c.get('sidebarOrderEditor').classList.contains('hidden'), true);
  assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder);
  assert.deepEqual(shownOrder(c), defaultOrder);
  assert.equal(c.state.sidebarDirty, false);
  assert.equal(c.run("moveSidebarModule('ads',0)"), false, 'default mode cannot be reordered');
  assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder);
});

test('turning custom order off shows the system order and preserves the saved custom sequence', () => {
  const savedOrder = [...defaultOrder].reverse();
  const c = setup(() => {throw Error('changing ordering must not write');}, {...sidebar, order: savedOrder, custom_order_enabled: false});
  const defaultText = c.get('sidebarDefaultOrderText').textContent;
  enableCustomOrder(c);
  assert.deepEqual(shownOrder(c), savedOrder);
  assert.equal(c.get('sidebarOrderEditor').classList.contains('hidden'), false);
  assert.equal(c.get('sidebarDefaultOrderNotice').classList.contains('hidden'), true);
  assert.equal(c.state.sidebarDirty, true);
  enableCustomOrder(c, false);
  assert.equal(c.get('sidebarDefaultOrderText').textContent, defaultText);
  assert.deepEqual(Array.from(c.state.sidebarOrder), savedOrder);
  assert.equal(c.state.sidebarDirty, false, 'returning to the saved mode restores the baseline');
  enableCustomOrder(c);
  assert.deepEqual(shownOrder(c), savedOrder);
});

test('keyboard and small-screen move buttons reorder disabled components including advertisements', () => {
  const c = setup(() => {throw Error('reordering must not write');}, {...sidebar, modules: []});
  enableCustomOrder(c);
  assert.equal(c.get('sidebarOrderList').children[0].children[2].children[0].disabled, true);
  assert.equal(c.get('sidebarOrderList').children.at(-1).children[2].children[1].disabled, true);
  const ads = c.get('sidebarOrderList').children[4];
  assert.equal(ads.draggable, true);
  ads.children[2].children[0].onclick();
  assert.deepEqual(shownOrder(c), ['search', 'popular', 'stats', 'ads', 'archives', 'categories', 'tags']);
  assert.deepEqual(Array.from(c.run('collectSidebarModules()')), []);
  assert.equal(c.get('adEnabled').checked, false);
  assert.equal(c.state.sidebarDirty, true);
  assert.match(c.get('sidebarOrderStatus').textContent, /4/);
  c.get('sidebarOrderList').children[3].children[2].children[1].onclick();
  assert.deepEqual(shownOrder(c), defaultOrder);
  enableCustomOrder(c, false);
  assert.equal(c.state.sidebarDirty, false);
});

test('native drag and drop moves the dragged component to its target and rejects unrelated drops', () => {
  const c = setup(() => {throw Error('dragging must not write');});
  enableCustomOrder(c);
  const rows = c.get('sidebarOrderList').children;
  const transferred = {}, dataTransfer = {setData(type, value) {transferred[type] = value;}};
  rows[0].ondragstart({dataTransfer, preventDefault() {throw Error('an enabled drag must start');}});
  assert.equal(transferred['text/plain'], 'search');
  assert.equal(dataTransfer.effectAllowed, 'move');
  const over = {dataTransfer, preventDefault() {this.prevented = true;}};
  rows[6].ondragover(over);
  assert.equal(over.prevented, true);
  assert.equal(dataTransfer.dropEffect, 'move');
  rows[6].ondrop({preventDefault() {}});
  assert.deepEqual(shownOrder(c), ['popular', 'stats', 'archives', 'ads', 'categories', 'tags', 'search']);
  assert.equal(c.state.sidebarDragKey, '');
  rows[0].ondragend();
  assert.equal(c.state.sidebarDirty, true);
  const previous = shownOrder(c);
  c.get('sidebarOrderList').children[0].ondrop({preventDefault() {throw Error('an unrelated drop must be ignored');}});
  assert.deepEqual(shownOrder(c), previous);
});

test('custom-order writes keep the full sequence when disabled and rejected writes retain the draft', async () => {
  let reject = false, submitted;
  const c = setup(call => call.path === 'session' ? {data: session} : reject ? {status: 503, data: {error: '保存失败'}} : {data: submitted = JSON.parse(call.options.body)});
  enableCustomOrder(c);
  c.run("moveSidebarModule('ads',0)");
  const custom = shownOrder(c);
  enableCustomOrder(c, false);
  await c.get('sidebarForm').onsubmit({preventDefault() {}});
  assert.equal(submitted.custom_order_enabled, false);
  assert.deepEqual(submitted.order, custom);
  assert.equal(c.state.sidebarDirty, false);
  enableCustomOrder(c);
  reject = true;
  c.run("moveSidebarModule('tags',1)");
  const rejected = shownOrder(c);
  await c.get('sidebarForm').onsubmit({preventDefault() {}});
  assert.equal(c.get('sidebarCustomOrderEnabled').checked, true);
  assert.deepEqual(shownOrder(c), rejected);
  assert.equal(c.state.sidebarDirty, true);
  assert.equal(c.get('sidebarForm').inert, false);
});

test('pending reads and saves prevent ordering interaction and cannot overwrite a newer ordering draft', async () => {
  const {pending, respond} = deferredReads(), c = setup(respond, {...sidebar, custom_order_enabled: true});
  const read = c.run('loadSidebar()');
  assert.equal(c.run("moveSidebarModule('ads',0)"), false);
  c.state.sidebarOrder = [...defaultOrder].reverse();
  c.get('sidebarForm').oninput();
  pending[0]({data: {...sidebar, custom_order_enabled: true}});
  await read;
  assert.deepEqual(Array.from(c.state.sidebarOrder), [...defaultOrder].reverse());
  assert.equal(c.state.sidebarDirty, true);
  c.state.sidebarSaving = true;
  assert.equal(c.run("moveSidebarModule('ads',0)"), false);
  c.state.sidebarSaving = false;
});

test('ordering changes warn on leave and restore the saved order when discarded', async () => {
  const c = setup(() => {throw Error('navigation must not write');}, {...sidebar, custom_order_enabled: true});
  c.run("moveSidebarModule('ads',0)");
  c.context.window.confirm = () => false;
  await c.run("navigateAdmin('posts')");
  assert.equal(c.state.view, 'sidebar');
  assert.equal(c.state.sidebarOrder[0], 'ads');
  const unload = {preventDefault() {this.prevented = true;}};
  c.events.beforeunload(unload);
  assert.equal(unload.prevented, true);
  c.context.window.confirm = () => true;
  await c.run("navigateAdmin('posts')");
  assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder);
  assert.equal(c.state.sidebarDirty, false);
});

test('expired ordering writes resume the same draft without replay and account cleanup resets ordering', async () => {
  const c = setup(() => ({data: {authenticated: false, configured: true, csrf: 'expired-session'}}));
  enableCustomOrder(c);
  c.run("moveSidebarModule('ads',0)");
  await c.get('sidebarForm').onsubmit({preventDefault() {}});
  assert.equal(c.state.authenticated, false);
  await c.run("showApp('editor')");
  assert.equal(c.get('sidebarCustomOrderEnabled').checked, true);
  assert.equal(c.state.sidebarOrder[0], 'ads');
  assert.equal(c.state.sidebarDirty, true);
  assert.equal(c.calls.length, 1);
  c.run('clearSessionContent()');
  assert.equal(c.get('sidebarCustomOrderEnabled').checked, false);
  assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder);
  assert.equal(c.state.sidebarDirty, false);
});

test('interface-language changes reload and freshly render ordering labels and controls in all eleven languages', () => {
  const c = setup(() => {throw Error('changing UI language must not write configuration');});
  let reloads = 0;
  c.context.location.reload = () => reloads++;
  c.get('adminUILanguage').value = 'en';
  c.get('adminUILanguage').onchange();
  assert.equal(reloads, 1, 'a new interface language reinitializes the application');
  const dictionaries = JSON.parse(fs.readFileSync(path.join(__dirname, '../../internal/blog/web/admin/admin_messages.json'), 'utf8'));
  const keys = {search: '搜索', popular: '热门文章', stats: '站点统计', archives: '年月归档', ads: '广告', categories: '分类', tags: '标签'};
  for (const [lang, dictionary] of Object.entries(dictionaries)) {
    const localHarness = harness.replace('localStorage: {setItem() {}}', 'localStorage: {getItem() {return ' + JSON.stringify(lang) + ';},setItem() {}}');
    const {client: inLanguage} = new Function('require', '__dirname', localHarness + '\nreturn {client};')(harnessRequire, __dirname);
    const active = inLanguage(() => {throw Error('rendering must not write');}, '');
    active.context.fixtureSidebar = sidebar;
    active.run("state.authenticated=true;state.user='editor';state.view='sidebar';applySidebarSettings(fixtureSidebar)");
    enableCustomOrder(active);
    for (const row of active.get('sidebarOrderList').children) {
      const label = dictionary[keys[row.dataset.module]];
      assert.equal(row.children[1].textContent, label, lang + ' component label');
      assert.equal(row.children[2].children[0].title, dictionary['上移{0}'].replace('{0}', label), lang + ' move up label');
      assert.equal(row.children[2].children[1].title, dictionary['下移{0}'].replace('{0}', label), lang + ' move down label');
    }
    assert.equal(active.get('sidebarDefaultOrderText').textContent, ' · ' + defaultOrder.map(key => dictionary[keys[key]]).join(' → '));
  }
});

function pointerFixture(c, key = 'archives') {
  const rows = c.get('sidebarOrderList').children;
  rows.forEach((row, index) => {row.getBoundingClientRect = () => ({top: index * 60, height: 50});});
  const row = rows.find(item => item.dataset.module === key), handle = row.children[0];
  const captures = [], releases = [];
  handle.setPointerCapture = id => captures.push(id);
  handle.releasePointerCapture = id => releases.push(id);
  return {rows, row, handle, captures, releases};
}

function pointerEvent(pointerId, clientY, extra = {}) {
  return {pointerId, clientY, button: 0, isPrimary: true, preventDefault() {this.prevented = true;}, ...extra};
}

test('dragging a handle with mouse or touch captures its pointer and commits one move on release', () => {
  for (const pointerType of ['mouse', 'touch']) {
    const c = setup(() => {throw Error('dragging must not write');}, {...sidebar, custom_order_enabled: true});
    const {rows, row, handle, captures, releases} = pointerFixture(c);
    const down = pointerEvent(19, 205, {pointerType});
    handle.onpointerdown(down);
    assert.equal(down.prevented, true, 'the handle prevents competing native drags');
    assert.deepEqual(captures, [19]);
    assert.equal(row.classList.contains('dragging'), true);
    let nativePrevented = false;
    row.ondragstart({preventDefault() {nativePrevented = true;}});
    assert.equal(nativePrevented, true);
    handle.onpointermove(pointerEvent(19, 12, {pointerType}));
    assert.equal(c.get('sidebarOrderList').children, rows, 'moving must retain the captured DOM handle');
    assert.equal(rows[0].classList.contains('drag-over'), true);
    assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder, 'movement only highlights its target');
    handle.onpointerup(pointerEvent(19, 12, {pointerType}));
    assert.deepEqual(shownOrder(c), ['archives', 'search', 'popular', 'stats', 'ads', 'categories', 'tags']);
    assert.equal(c.state.sidebarPointerDrag, null);
    assert.deepEqual(releases, [19]);
    assert.equal(c.state.sidebarDirty, true);
    handle.onlostpointercapture(pointerEvent(19, 12));
    assert.deepEqual(shownOrder(c), ['archives', 'search', 'popular', 'stats', 'ads', 'categories', 'tags']);
  }
});

test('pointer cancellation or lost capture removes its highlight without changing the order', () => {
  for (const finish of ['onpointercancel', 'onlostpointercapture']) {
    const c = setup(() => {throw Error('cancelling a drag must not write');}, {...sidebar, custom_order_enabled: true});
    const {rows, handle, releases} = pointerFixture(c);
    handle.onpointerdown(pointerEvent(7, 205));
    handle.onpointermove(pointerEvent(7, 12));
    handle[finish](pointerEvent(7, 12));
    assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder);
    assert.equal(c.state.sidebarDirty, false);
    assert.equal(c.state.sidebarPointerDrag, null);
    assert.deepEqual(releases, [7]);
    for (const row of rows) {
      assert.equal(row.classList.contains('dragging'), false);
      assert.equal(row.classList.contains('drag-over'), false);
    }
  }
});

test('another pointer cannot move or finish an active drag and secondary buttons cannot begin one', () => {
  const c = setup(() => {throw Error('pointer checks must not write');}, {...sidebar, custom_order_enabled: true});
  const {handle, captures} = pointerFixture(c);
  handle.onpointerdown(pointerEvent(8, 205, {button: 2}));
  handle.onpointerdown(pointerEvent(8, 205, {isPrimary: false}));
  assert.equal(c.state.sidebarPointerDrag, null);
  assert.deepEqual(captures, []);
  handle.onpointerdown(pointerEvent(9, 205));
  handle.onpointermove(pointerEvent(10, 12));
  handle.onpointerup(pointerEvent(10, 12));
  assert.equal(c.state.sidebarPointerDrag.pointerId, 9);
  assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder);
  handle.onpointercancel(pointerEvent(9, 205));
  assert.equal(c.state.sidebarPointerDrag, null);
});

test('default mode and configuration loading or saving block pointer reordering', () => {
  for (const mode of ['default', 'loading', 'saving']) {
    const c = setup(() => {throw Error('disabled dragging must not write');}, {...sidebar, custom_order_enabled: mode !== 'default'});
    if (mode === 'loading') c.state.sidebarLoading = true;
    if (mode === 'saving') c.state.sidebarSaving = true;
    const {handle, captures} = pointerFixture(c);
    handle.onpointerdown(pointerEvent(3, 205));
    handle.onpointermove(pointerEvent(3, 12));
    handle.onpointerup(pointerEvent(3, 12));
    assert.deepEqual(captures, []);
    assert.equal(c.state.sidebarPointerDrag, null);
    assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder);
    assert.equal(c.state.sidebarDirty, false);
  }
});

test('a save or disabling custom ordering safely cancels a pending pointer gesture', () => {
  for (const next of ['save', 'default']) {
    const c = setup(() => {throw Error('cancelling a gesture must not write');}, {...sidebar, custom_order_enabled: true});
    const {handle, releases} = pointerFixture(c);
    handle.onpointerdown(pointerEvent(4, 205));
    handle.onpointermove(pointerEvent(4, 12));
    if (next === 'save') {
      c.state.sidebarSaving = true;
      c.run('refreshSidebarInteraction()');
    } else enableCustomOrder(c, false);
    assert.equal(c.state.sidebarPointerDrag, null);
    assert.deepEqual(releases, [4]);
    handle.onpointerup(pointerEvent(4, 12));
    assert.deepEqual(Array.from(c.state.sidebarOrder), defaultOrder);
  }
});
