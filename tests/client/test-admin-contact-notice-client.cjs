'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const harness = fs.readFileSync(path.join(__dirname, 'test-admin-session-client.cjs'), 'utf8');
const harnessRequire = id => id === 'node:test' ? {test() {}} : require(id);
const {client} = new Function('require', '__dirname', harness + '\nreturn {client};')(harnessRequire, __dirname);
const session = {authenticated: true, configured: true, user: 'editor', csrf: 'contact-notice-fixture'};
const base = {title: 'Contact notice test', base_url: 'https://ganwen.test', posts_per_page: 10, footer_text: ''};

function setup(respond, notice = {}) {
  const c = client(respond, '');
  c.context.savedSettings = {...base, ...notice};
  c.run("state.authenticated=true;state.user='editor';state.view='settings';applySiteSettings(savedSettings)");
  return c;
}

function edit(c, enabled, text) {
  c.get('contactNoticeEnabled').checked = enabled;
  c.get('contactNoticeEnabled').onchange();
  c.get('contactNoticeText').value = text;
  c.get('settingsForm').oninput();
}

test('legacy settings default to off and both notice fields participate in unsaved changes', () => {
  const c = setup(() => {throw Error('editing must not make requests');});
  assert.equal(c.get('contactNoticeEnabled').checked, false);
  assert.equal(c.get('contactNoticeText').value, '');
  assert.equal(c.get('contactNoticeText').required, false);
  assert.equal(c.state.settingsDirty, false);
  edit(c, true, '');
  assert.equal(c.state.settingsDirty, true, 'the toggle alone is a draft');
  assert.equal(c.get('contactNoticeText').required, true);
  edit(c, false, '请直接说明来意。');
  assert.equal(c.state.settingsDirty, true, 'disabled notice text is still a draft');
  c.context.window.confirm = () => false;
  assert.equal(c.run('canLeave()'), false);
  edit(c, false, '');
  assert.equal(c.state.settingsDirty, false);
});

test('saving a notice preserves multiline plain text and accepts 4000 Unicode characters', async () => {
  const submitted = [];
  const c = setup(call => {
    if (call.path === 'session') return {data: session};
    assert.equal(call.path, 'settings');
    const payload = JSON.parse(call.options.body);
    submitted.push(payload);
    return {data: payload};
  });
  for (const text of ['请先说明来意。\n<p>这只是文字，不是 HTML。</p>', '🙂'.repeat(4000)]) {
    edit(c, true, text);
    await c.get('settingsForm').onsubmit({preventDefault() {}});
    assert.equal(submitted.at(-1).contact_notice_enabled, true);
    assert.equal(submitted.at(-1).contact_notice_text, text);
    assert.equal(c.get('contactNoticeText').value, text);
    assert.equal(c.state.settingsDirty, false);
    assert.equal(c.state.settingsSaving, false);
    assert.equal(c.get('settingsForm').inert, false);
  }
  edit(c, false, '保留文案，暂时关闭');
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(submitted.at(-1).contact_notice_enabled, false);
  assert.equal(submitted.at(-1).contact_notice_text, '保留文案，暂时关闭');
  assert.equal(c.get('contactNoticeText').required, false);
});

test('enabled empty notices and overlong Unicode text fail before the settings API', async () => {
  for (const [enabled, text, error] of [[true, '', /请填写提示内容/], [true, ' \n\t ', /请填写提示内容/], [true, '🙂'.repeat(4001), /4000/], [false, '字'.repeat(4001), /4000/]]) {
    const c = setup(() => {throw Error('invalid settings must not be sent');});
    edit(c, enabled, text);
    await c.get('settingsForm').onsubmit({preventDefault() {}});
    assert.equal(c.calls.length, 0);
    assert.match(c.get('toast').textContent, error);
    assert.equal(c.get('contactNoticeText').value, text);
    assert.equal(c.state.settingsDirty, true);
    assert.equal(c.state.settingsSaving, false);
    assert.equal(c.get('settingsForm').inert, false);
    assert.equal(c.get('settingsSaveButton').disabled, false);
  }
});

test('server rejection retains the contact notice draft and restores the form', async () => {
  const c = setup(call => call.path === 'session' ? {data: session} : {status: 503, data: {error: '保存暂时失败'}}, {contact_notice_enabled: true, contact_notice_text: '原提示'});
  edit(c, false, '未保存的新提示');
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.get('contactNoticeText').value, '未保存的新提示');
  assert.equal(c.get('contactNoticeEnabled').checked, false);
  assert.equal(c.state.settings.contact_notice_text, '原提示');
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.get('settingsForm').inert, false);
  assert.equal(c.get('settingsSaveButton').disabled, false);
});

test('pending notice saves lock the form and prevent duplicate writes', async () => {
  let release;
  const c = setup(call => {
    if (call.path === 'session') return {data: session};
    const payload = JSON.parse(call.options.body);
    return new Promise(resolve => {release = () => resolve({data: payload});});
  });
  edit(c, true, '请先阅读');
  const saving = c.get('settingsForm').onsubmit({preventDefault() {}});
  for (let n = 0; !release && n < 40; n++) await new Promise(resolve => setImmediate(resolve));
  assert.ok(release);
  assert.equal(c.get('settingsForm').inert, true);
  assert.equal(c.get('settingsSaveButton').disabled, true);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.calls.filter(call => call.path === 'settings').length, 1);
  release();
  await saving;
  assert.equal(c.state.settingsDirty, false);
  assert.equal(c.get('settingsForm').inert, false);
});

test('late settings reads do not overwrite notice drafts', async () => {
  let release;
  const c = setup(() => new Promise(resolve => {release = resolve;}));
  const reading = c.run('loadSettings()');
  edit(c, true, '请求期间填写的提示');
  release({data: {...base, contact_notice_enabled: false, contact_notice_text: '较旧内容'}});
  await reading;
  assert.equal(c.get('contactNoticeText').value, '请求期间填写的提示');
  assert.equal(c.get('contactNoticeEnabled').checked, true);
  assert.equal(c.state.settingsDirty, true);
});

test('accepted logout clears the notice while cancelled logout retains its draft', async () => {
  let authenticated = true;
  const c = setup(call => {
    if (call.path === 'session') return {data: {...session, authenticated}};
    assert.equal(call.path, 'logout');
    authenticated = false;
    return {data: {}};
  });
  edit(c, true, '未保存的提示');
  c.context.window.confirm = () => false;
  await c.get('logoutButton').onclick();
  assert.equal(c.get('contactNoticeText').value, '未保存的提示');
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.calls.length, 0);
  c.context.window.confirm = () => true;
  await c.get('logoutButton').onclick();
  assert.equal(c.get('contactNoticeText').value, '');
  assert.equal(c.get('contactNoticeEnabled').checked, false);
  assert.equal(c.get('contactNoticeText').required, false);
  assert.equal(c.state.settingsDirty, false);
});
