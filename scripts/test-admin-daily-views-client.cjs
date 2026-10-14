'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

// Reuse the existing real-script DOM/network harness without registering its tests.
const harness = fs.readFileSync(path.join(__dirname, 'test-admin-session-client.cjs'), 'utf8');
const harnessRequire = id => id === 'node:test' ? {test() {}} : require(id);
const {client, html} = new Function('require', '__dirname', harness + '\nreturn {client, html};')(harnessRequire, __dirname);

function applySettings(c, dailyViews) {
  const settings = {title: '每日保底测试', base_url: 'https://ganwen.test', posts_per_page: 10, footer_text: '', public_daily_views: dailyViews};
  c.run("state.authenticated=true;state.user='editor';state.view='settings';applySiteSettings(" + JSON.stringify(settings) + ');');
}

function setDailyViews(c, value) {
  c.get('publicDailyViews').value = value;
  c.get('settingsForm').oninput();
}

const session = {authenticated: true, configured: true, user: 'editor', csrf: 'daily-views-session'};

test('virtual counts and component visibility load, disable and save independently', async () => {
  const submitted = [];
  const c = client(call => {
    if (call.path === 'session') return {data: session};
    const payload = JSON.parse(call.options.body);
    submitted.push(payload);
    return {data: payload};
  });
  applySettings(c, 2500);
  assert.equal(c.get('publicVirtualViewsEnabled').checked, true, 'legacy settings keep display behavior');
  assert.equal(c.get('publicStatsEnabled').checked, true);
  c.get('publicVirtualViewsEnabled').checked = false;
  c.get('publicVirtualViewsEnabled').onchange();
  assert.equal(c.get('publicDailyViews').disabled, true);
  assert.equal(c.state.settingsDirty, true);
  c.get('publicStatsEnabled').checked = false;
  c.get('settingsForm').oninput();
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(submitted.at(-1).public_virtual_views_enabled, false);
  assert.equal(submitted.at(-1).public_stats_enabled, false);
  assert.equal(submitted.at(-1).public_daily_views, 2500, 'disabling retains the daily target');
  assert.equal(c.get('publicDailyViews').disabled, true);
  assert.equal(c.state.settingsDirty, false);
  c.run('applySiteSettings({title:"Fresh",posts_per_page:10,public_virtual_views_enabled:false,public_stats_enabled:true})');
  assert.equal(c.get('publicVirtualViewsEnabled').checked, false);
  assert.equal(c.get('publicStatsEnabled').checked, true);
});

test('daily views input exposes its whole-number range and UTC display explanation', () => {
  const field = html.match(/<input\b[^>]*\bid="publicDailyViews"[^>]*>/)?.[0];
  assert.ok(field, 'the settings control is missing');
  for (const attribute of ['type="number"', 'min="0"', 'max="1000000"', 'step="1"', 'required']) {
    assert.ok(field.includes(attribute), 'missing input constraint: ' + attribute);
  }
  assert.match(html, /label for="publicDailyViews"[^>]*>每日保底浏览/);
  assert.match(html, /保底按全天逐步增长，速度会自然波动，并叠加真实浏览；后台仍统计真实数据。统计日使用 UTC。/);
});

test('loading defaults to 1000, preserves explicit zero and tracks unsaved input changes', () => {
  const c = client(() => {throw Error('loading settings must not write');});
  for (const [input, expected] of [[undefined, '1000'], [null, '1000'], [0, '0'], [1000000, '1000000']]) {
    applySettings(c, input);
    assert.equal(c.get('publicDailyViews').value, expected);
    assert.equal(c.state.settingsDirty, false);
  }
  applySettings(c, undefined);
  setDailyViews(c, '3500');
  assert.equal(c.state.settingsDirty, true);
  setDailyViews(c, '1000');
  assert.equal(c.state.settingsDirty, false);
  setDailyViews(c, '');
  assert.equal(c.state.settingsDirty, true, 'a temporarily empty input must remain an unsaved draft');
});

test('saving sends a numeric daily target including zero and reloads the authoritative response', async () => {
  const submitted = [];
  const c = client(call => {
    if (call.path === 'session') return {data: session};
    assert.equal(call.path, 'settings');
    const payload = JSON.parse(call.options.body);
    submitted.push(payload);
    return {data: payload};
  });
  applySettings(c, 1000);
  for (const value of [0, 1000, 1000000]) {
    setDailyViews(c, String(value));
    await c.get('settingsForm').onsubmit({preventDefault() {}});
    assert.equal(submitted.at(-1).public_daily_views, value);
    assert.equal(typeof submitted.at(-1).public_daily_views, 'number');
    assert.equal(c.get('publicDailyViews').value, String(value));
    assert.equal(c.state.settingsDirty, false);
    assert.equal(c.state.settingsSaving, false);
    assert.equal(c.get('settingsForm').inert, false);
    assert.equal(c.get('settingsSaveButton').disabled, false);
  }
  assert.equal(submitted.length, 3);
});

test('empty, fractional, nonfinite and out-of-range targets never reach the settings API', async () => {
  for (const invalid of ['', ' ', '-1', '1000001', '1.25', 'NaN', 'Infinity']) {
    const c = client(() => {throw Error('an invalid target must not issue a request');});
    applySettings(c, 1000);
    setDailyViews(c, invalid);
    await c.get('settingsForm').onsubmit({preventDefault() {}});
    assert.equal(c.calls.length, 0, 'invalid value wrote to the server: ' + JSON.stringify(invalid));
    assert.equal(c.get('publicDailyViews').value, invalid);
    assert.equal(c.state.settingsDirty, true);
    assert.equal(c.state.settingsSaving, false);
    assert.equal(c.get('settingsForm').inert, false);
    assert.equal(c.get('settingsSaveButton').disabled, false);
    assert.match(c.get('toast').textContent, /每日保底浏览.*整数/);
  }
});

test('server rejection retains the target draft and restores save interaction', async () => {
  const c = client(call => call.path === 'session' ? {data: session} : {status: 503, data: {error: '保存暂时失败'}});
  applySettings(c, 1000);
  setDailyViews(c, '2300');
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.get('publicDailyViews').value, '2300');
  assert.equal(c.state.settings.public_daily_views, 1000);
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.get('settingsForm').inert, false);
  assert.equal(c.get('settingsSaveButton').disabled, false);
});

test('existing upload, translation and duplicate-save guards keep the daily target untouched', async () => {
  for (const busy of ['avatarUploadPending', 'footerTranslating', 'settingsSaving']) {
    const c = client(() => {throw Error('busy settings must not save');});
    applySettings(c, 1000);
    setDailyViews(c, '2300');
    c.state[busy] = true;
    await c.get('settingsForm').onsubmit({preventDefault() {}});
    assert.equal(c.calls.length, 0);
    assert.equal(c.get('publicDailyViews').value, '2300');
    assert.equal(c.state.settingsDirty, true);
    assert.equal(c.state[busy], true);
  }
});

test('accepted logout resets the target to 1000 while cancelled logout keeps its draft', async () => {
  let authenticated = true;
  const c = client(call => {
    if (call.path === 'session') return {data: {...session, authenticated}};
    assert.equal(call.path, 'logout');
    authenticated = false;
    return {data: {}};
  });
  applySettings(c, 1000);
  setDailyViews(c, '2300');
  c.context.window.confirm = () => false;
  await c.get('logoutButton').onclick();
  assert.equal(c.get('publicDailyViews').value, '2300');
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.calls.length, 0);
  c.context.window.confirm = () => true;
  await c.get('logoutButton').onclick();
  assert.equal(c.get('publicDailyViews').value, '1000');
  assert.equal(c.state.settingsDirty, false);
  assert.equal(c.state.settings, null);
  assert.equal(c.state.authenticated, false);
});
