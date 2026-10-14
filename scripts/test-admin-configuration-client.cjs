'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const harness = fs.readFileSync(path.join(__dirname, 'test-admin-session-client.cjs'), 'utf8');
const harnessRequire = id => id === 'node:test' ? {test() {}} : require(id);
const {client} = new Function('require', '__dirname', harness + '\nreturn {client};')(harnessRequire, __dirname);
const session = {authenticated: true, configured: true, user: 'editor', csrf: 'configuration-fixture'};
const settings = {title: 'Saved site', base_url: 'https://ganwen.test', posts_per_page: 10, footer_text: ''};
const config = {provider: 'compatible', endpoint: 'https://api.example.test/v1', model: 'saved-model', execution_mode: 'manual', auto_publish: false};

function setup(respond, view = 'settings') {
  const c = client(respond, '');
  c.context.fixtureSettings = settings;
  c.context.fixtureConfig = config;
  c.run("state.authenticated=true;state.user='editor';applySiteSettings(fixtureSettings);applyTranslationConfig(fixtureConfig)");
  c.state.view = view;
  return c;
}

function deferredReads() {
  const pending = [];
  return {pending, respond: () => new Promise(resolve => pending.push(resolve))};
}

test('article sidebar defaults off, restores saved state and tracks checkbox drafts', () => {
  const c = setup(() => {throw Error('editing the sidebar must not write');});
  assert.equal(c.get('articleSidebarEnabled').checked, false);
  assert.equal(c.state.settingsDirty, false);
  c.get('articleSidebarEnabled').checked = true;
  c.get('settingsForm').oninput();
  assert.equal(c.state.settingsDirty, true);
  c.get('articleSidebarEnabled').checked = false;
  c.get('settingsForm').oninput();
  assert.equal(c.state.settingsDirty, false);
  c.context.fixtureSettings = {...settings, article_sidebar_enabled: true};
  c.run('applySiteSettings(fixtureSettings)');
  assert.equal(c.get('articleSidebarEnabled').checked, true);
  assert.equal(c.state.settingsDirty, false);
  c.run('clearSessionContent()');
  assert.equal(c.get('articleSidebarEnabled').checked, false);
});

test('article sidebar saves explicit booleans and preserves a rejected draft', async () => {
  let reject = false;
  const submitted = [];
  const c = setup(call => {
    if (call.path === 'session') return {data: session};
    assert.equal(call.path, 'settings');
    const payload = JSON.parse(call.options.body);
    submitted.push(payload);
    return reject ? {status: 503, data: {error: '保存失败'}} : {data: payload};
  });
  for (const enabled of [true, false]) {
    c.get('articleSidebarEnabled').checked = enabled;
    c.get('settingsForm').oninput();
    await c.get('settingsForm').onsubmit({preventDefault() {}});
    assert.equal(submitted.at(-1).article_sidebar_enabled, enabled);
    assert.equal(c.state.settings.article_sidebar_enabled, enabled);
    assert.equal(c.state.settingsDirty, false);
  }
  reject = true;
  c.get('articleSidebarEnabled').checked = true;
  c.get('settingsForm').oninput();
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.get('articleSidebarEnabled').checked, true);
  assert.equal(c.state.settings.article_sidebar_enabled, false);
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.get('settingsForm').inert, false);
});

test('a delayed settings read cannot replace an article sidebar draft', async () => {
  const {pending, respond} = deferredReads(), c = setup(respond);
  const read = c.run('loadSettings()');
  c.get('articleSidebarEnabled').checked = true;
  c.get('settingsForm').oninput();
  pending[0]({data: settings});
  await read;
  assert.equal(c.get('articleSidebarEnabled').checked, true);
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.get('settingsForm').inert, false);
});

async function waitFor(predicate) {
  for (let i = 0; !predicate() && i < 40; i++) await new Promise(resolve => setImmediate(resolve));
  assert.ok(predicate(), 'the expected request did not start');
}

test('the latest settings read wins and an older response cannot clear a new draft', async () => {
  const {pending, respond} = deferredReads(), c = setup(respond);
  const first = c.run('loadSettings()'), second = c.run('loadSettings()');
  assert.equal(c.get('settingsForm').inert, true);
  assert.equal(c.get('settingsSaveButton').disabled, true);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.calls.length, 2, 'loading must prevent saving stale or default values');
  pending[1]({data: {...settings, title: 'Latest site'}});
  await second;
  c.get('siteTitle').value = 'New manual draft';
  c.get('settingsForm').oninput();
  pending[0]({data: {...settings, title: 'Old site'}});
  await first;
  assert.equal(c.get('siteTitle').value, 'New manual draft');
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.get('settingsForm').inert, false);
});

for (const kind of ['settings', 'translation']) {
  const load = kind === 'settings' ? 'loadSettings()' : 'loadTranslationConfig()';
  const field = kind === 'settings' ? 'siteTitle' : 'translationModel';
  const form = kind === 'settings' ? 'settingsForm' : 'translationForm';
  const saved = kind === 'settings' ? settings : config;
  test(kind + ' reads do not overwrite content changed while the request was pending', async () => {
    const {pending, respond} = deferredReads(), c = setup(respond, kind);
    const read = c.run(load);
    c.get(field).value = 'Changed while pending';
    c.get(form).oninput();
    pending[0]({data: saved});
    await read;
    assert.equal(c.get(field).value, 'Changed while pending');
    assert.equal(c.state[kind === 'settings' ? 'settingsDirty' : 'translationDirty'], true);
    assert.equal(c.get(form).inert, false);
  });

  test(kind + ' requests abandoned by navigation cannot replace the next visit', async () => {
    const {pending, respond} = deferredReads(), c = setup(respond, kind);
    const old = c.run(load);
    c.run("showView('posts')");
    c.run("showView('" + kind + "')");
    const latest = c.run(load);
    pending[0]({data: saved});
    await old;
    assert.equal(c.get(form).inert, true, 'old cleanup must not unlock the current request');
    pending[1]({data: kind === 'settings' ? {...settings, title: 'Current visit'} : {...config, model: 'Current visit'}});
    await latest;
    assert.equal(c.get(field).value, 'Current visit');
    assert.equal(c.get(form).inert, false);
  });

  test(kind + ' responses from a logged-out session cannot restore discarded account data', async () => {
    const {pending, respond} = deferredReads(), c = setup(respond, kind);
    const read = c.run(load);
    c.context.anonymous = {authenticated: false, configured: true, csrf: 'anonymous-fixture'};
    await c.run('showLogin(false, anonymous)');
    pending[0]({data: saved});
    await read;
    assert.equal(c.state[kind === 'settings' ? 'settings' : 'translationSaved'], null);
    assert.equal(c.get(form).inert, true);
  });
}

test('failed initial settings and translation reads never enable default-value saves', async () => {
  for (const [load, form, button] of [['loadSettings()', 'settingsForm', 'settingsSaveButton'], ['loadTranslationConfig()', 'translationForm', 'translationSaveButton']]) {
    const c = client(() => ({status: 503, data: {error: 'temporarily unavailable'}}), '');
    c.state.authenticated = true;
    c.state.user = 'editor';
    await assert.rejects(c.run(load), /请求失败，请稍后重试/);
    assert.equal(c.get(form).inert, true);
    assert.equal(c.get(button).disabled, true);
    await c.get(form).onsubmit({preventDefault() {}});
    assert.equal(c.calls.length, 1);
  }
});

test('translation drafts warn on navigation and unload, while accepted navigation discards them', async () => {
  const c = setup(() => {throw Error('navigation must not write configuration');}, 'translation');
  c.get('translationModel').value = 'unsaved-model';
  c.get('translationForm').oninput();
  let confirmations = 0;
  c.context.window.confirm = () => {confirmations++; return false;};
  await c.run("navigateAdmin('posts')");
  assert.equal(c.state.view, 'translation');
  assert.equal(confirmations, 1);
  assert.equal(c.get('translationModel').value, 'unsaved-model');
  const unload = {preventDefault() {this.prevented = true;}};
  c.events.beforeunload(unload);
  assert.equal(unload.prevented, true);
  c.context.window.confirm = () => true;
  await c.run("navigateAdmin('posts')");
  assert.equal(c.state.view, 'posts');
  assert.equal(c.state.translationDirty, false);
  assert.equal(c.get('translationModel').value, config.model);
});

test('API key replacement and select changes participate in translation dirty tracking', () => {
  const c = setup(() => {throw Error('editing must not send requests');}, 'translation');
  c.get('translationKey').value = 'fixture-key-only';
  c.get('translationForm').oninput();
  assert.equal(c.state.translationDirty, true);
  c.get('translationKey').value = '';
  c.get('translationForm').oninput();
  assert.equal(c.state.translationDirty, false);
  c.get('translationExecutionMode').value = 'paused';
  c.get('translationForm').onchange();
  assert.equal(c.state.translationDirty, true);
});

test('translation save locks through the authoritative reread and sends a single write', async () => {
  let finishWrite, finishRead, submitted;
  const c = setup(({path, options}) => {
    if (path === 'session') return {data: session};
    assert.equal(path, 'translation-config');
    if (options.method === 'POST') {
      submitted = JSON.parse(options.body);
      return new Promise(resolve => {finishWrite = () => resolve({data: {ok: true}});});
    }
    return new Promise(resolve => {finishRead = () => resolve({data: {...submitted, api_key: '', has_api_key: true}});});
  }, 'translation');
  c.get('translationModel').value = 'new-model';
  c.get('translationKey').value = 'fixture-replacement';
  c.get('translationForm').oninput();
  const save = c.get('translationForm').onsubmit({preventDefault() {}});
  assert.equal(c.get('translationForm').inert, true);
  assert.equal(c.get('translationSaveButton').disabled, true);
  assert.equal(c.run('canLeave()'), false);
  await c.get('translationForm').onsubmit({preventDefault() {}});
  await waitFor(() => finishWrite);
  finishWrite();
  await waitFor(() => finishRead);
  assert.equal(c.get('translationForm').inert, true);
  finishRead();
  await save;
  assert.equal(c.calls.filter(call => call.options.method === 'POST').length, 1);
  assert.equal(c.get('translationModel').value, 'new-model');
  assert.equal(c.get('translationKey').value, '');
  assert.equal(c.state.translationDirty, false);
  assert.equal(c.get('translationForm').inert, false);
  assert.equal(c.get('translationSaveButton').disabled, false);
});

for (const phase of ['write', 'reread']) {
  test('a failed translation ' + phase + ' retains the draft and allows an explicit retry', async () => {
    const c = setup(({path, options}) => path === 'session' ? {data: session} : phase === 'reread' && options.method === 'POST' ? {data: {ok: true}} : {status: 503, data: {error: 'temporarily unavailable'}}, 'translation');
    c.get('translationModel').value = 'retry-model';
    c.get('translationKey').value = 'fixture-retry';
    c.get('translationForm').oninput();
    await c.get('translationForm').onsubmit({preventDefault() {}});
    assert.equal(c.get('translationModel').value, 'retry-model');
    assert.equal(c.get('translationKey').value, 'fixture-retry');
    assert.equal(c.state.translationDirty, true);
    assert.equal(c.state.translationSaving, false);
    assert.equal(c.get('translationForm').inert, false);
    assert.equal(c.get('translationSaveButton').disabled, false);
  });
}

test('expired translation saving retains the draft through same-account login without replaying it', async () => {
  const c = setup(() => ({data: {authenticated: false, configured: true, csrf: 'expired-session'}}), 'translation');
  c.get('translationModel').value = 'retained-model';
  c.get('translationKey').value = 'fixture-retained';
  c.get('translationForm').oninput();
  await c.get('translationForm').onsubmit({preventDefault() {}});
  assert.equal(c.state.authenticated, false);
  assert.equal(c.state.resumeSession.view, 'translation');
  await c.run("showApp('editor')");
  assert.equal(c.state.authenticated, true);
  assert.equal(c.state.view, 'translation');
  assert.equal(c.get('translationModel').value, 'retained-model');
  assert.equal(c.get('translationKey').value, 'fixture-retained');
  assert.equal(c.state.translationDirty, true);
  assert.equal(c.get('translationForm').inert, false);
  assert.equal(c.calls.length, 1);
});
