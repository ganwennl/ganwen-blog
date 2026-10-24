'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const {createHash} = require('node:crypto');

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
  c.get('contactNoticeText').oninput();
  c.get('settingsForm').oninput();
}

const sourceHash = text => createHash('sha256').update(text.trim()).digest('hex');
const flushAsync = async () => {for (let n = 0; n < 6; n++) await new Promise(resolve => setImmediate(resolve));};

function editTranslation(c, language, text) {
  c.get('contactNoticeLanguage').value = language;
  c.get('contactNoticeLanguage').onchange();
  c.get('contactNoticeTranslation').value = text;
  c.get('contactNoticeTranslation').oninput();
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
  editTranslation(c, 'en', 'Unsaved translated notice');
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
  assert.equal(c.get('contactNoticeTranslation').value, '');
  assert.equal(Object.keys(c.state.contactNoticeDrafts).length, 0);
});

test('manual translations save with their original source hash, reload, and support RTL', async () => {
  let payload;
  const c = setup(call => {
    if (call.path === 'session') return {data: session};
    assert.equal(call.path, 'settings');
    payload = JSON.parse(call.options.body);
    return {data: payload};
  }, {contact_notice_enabled: true, contact_notice_text: '  请说明来意。  '});
  await flushAsync();
  assert.equal(c.get('contactNoticeLanguage').children.length, 10);
  editTranslation(c, 'en', 'Please explain why you are contacting me.');
  editTranslation(c, 'ar', 'يرجى توضيح سبب التواصل.');
  assert.equal(c.get('contactNoticeTranslation').dir, 'rtl');
  assert.equal(c.state.settingsDirty, true);
  assert.match(c.get('contactNoticeTranslationStatus').textContent, /对应当前原文/);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(payload.contact_notice_text, '请说明来意。');
  assert.deepEqual(payload.contact_notice_translations.ar, {text: 'يرجى توضيح سبب التواصل.', source_hash: sourceHash('请说明来意。')});
  assert.equal(payload.contact_notice_translations.en.source_hash, sourceHash('请说明来意。'));
  assert.equal(c.get('contactNoticeTranslation').value, 'يرجى توضيح سبب التواصل.');
  assert.equal(c.state.settingsDirty, false);
  c.get('contactNoticeLanguage').value = 'en';
  c.get('contactNoticeLanguage').onchange();
  assert.equal(c.get('contactNoticeTranslation').dir, 'ltr');
  assert.equal(c.get('contactNoticeTranslation').value, payload.contact_notice_translations.en.text);
});

test('changing source marks old translations stale until explicitly confirmed', async () => {
  const original = '原提示', oldHash = sourceHash(original);
  let payload;
  const c = setup(call => call.path === 'session' ? {data: session} : (payload = JSON.parse(call.options.body), {data: payload}), {
    contact_notice_enabled: true, contact_notice_text: original,
    contact_notice_translations: {en: {text: 'Existing translation', source_hash: oldHash}}
  });
  await flushAsync();
  assert.match(c.get('contactNoticeTranslationStatus').textContent, /对应当前原文/);
  edit(c, true, '新提示');
  await flushAsync();
  assert.match(c.get('contactNoticeTranslationStatus').textContent, /原文已变化/);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(payload.contact_notice_translations.en.source_hash, oldHash, 'saving source must not relabel an old translation');
  await flushAsync();
  c.get('contactNoticeConfirmButton').onclick();
  assert.equal(c.state.settingsDirty, true);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(payload.contact_notice_translations.en.source_hash, sourceHash('新提示'));
});

test('AI translation requires an explicit click and blocks saving while pending', async () => {
  let release, submitted;
  const c = setup(call => {
    if (call.path === 'session') return {data: session};
    assert.equal(call.path, 'contact-notice/translate');
    submitted = JSON.parse(call.options.body);
    return new Promise(resolve => {release = resolve;});
  }, {contact_notice_enabled: true, contact_notice_text: '请说明来意。'});
  await flushAsync();
  assert.equal(c.calls.length, 0);
  const translating = c.get('contactNoticeTranslateButton').onclick();
  await flushAsync();
  assert.deepEqual(submitted, {contact_notice_text: '请说明来意。', language: 'en'});
  assert.equal(c.get('settingsSaveButton').disabled, true);
  assert.equal(c.run('canLeave()'), false);
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  await c.get('contactNoticeTranslateButton').onclick();
  assert.equal(c.calls.filter(call => call.path === 'contact-notice/translate').length, 1);
  release({data: {text: 'Please explain why you are contacting me.', source_hash: sourceHash('请说明来意。')}});
  await translating;
  assert.equal(c.get('contactNoticeTranslation').value, 'Please explain why you are contacting me.');
  assert.equal(c.state.settingsDirty, true);
  assert.equal(c.get('settingsSaveButton').disabled, false);
});

test('late AI results never replace edited source, edited translations, or a new session', async () => {
  for (const mutation of ['source', 'translation', 'session']) {
    let release;
    const c = setup(call => call.path === 'session' ? {data: session} : new Promise(resolve => {release = resolve;}), {contact_notice_enabled: true, contact_notice_text: '原文'});
    const translating = c.get('contactNoticeTranslateButton').onclick();
    await flushAsync();
    if (mutation === 'source') edit(c, true, '改变后的原文');
    if (mutation === 'translation') editTranslation(c, 'en', 'My manual change');
    if (mutation === 'session') {
      c.run("clearSessionContent();state.authenticated=true;state.user='editor';applySiteSettings(savedSettings)");
    }
    release({data: {text: 'Old AI response', source_hash: sourceHash('原文')}});
    await translating;
    assert.notEqual(c.get('contactNoticeTranslation').value, 'Old AI response');
    if (mutation === 'translation') assert.equal(c.get('contactNoticeTranslation').value, 'My manual change');
    if (mutation === 'session') assert.equal(c.state.settingsDirty, false);
    assert.equal(c.state.contactNoticeTranslating, false);
  }
});

test('translation failure preserves drafts and overlong translations are not saved', async () => {
  const c = setup(call => call.path === 'session' ? {data: session} : {status: 503, data: {error: '翻译暂时失败'}}, {contact_notice_enabled: true, contact_notice_text: '原文'});
  editTranslation(c, 'en', 'Existing draft');
  await c.get('contactNoticeTranslateButton').onclick();
  assert.equal(c.get('contactNoticeTranslation').value, 'Existing draft');
  assert.equal(c.get('settingsSaveButton').disabled, false);
  assert.match(c.get('toast').textContent, /请求失败/);
  const before = c.calls.length;
  editTranslation(c, 'en', '🙂'.repeat(4001));
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.calls.length, before);
  assert.match(c.get('toast').textContent, /4000/);
});

test('without browser hashing a stale manual translation cannot be rebound to new source', async () => {
  const c = setup(() => {throw Error('stale translation must not be sent');}, {contact_notice_enabled: true, contact_notice_text: '原文'});
  c.context.crypto = {};
  edit(c, true, '原文');
  await flushAsync();
  editTranslation(c, 'en', 'Old manual translation');
  edit(c, true, '新原文');
  await c.get('settingsForm').onsubmit({preventDefault() {}});
  assert.equal(c.calls.length, 0);
  assert.match(c.get('toast').textContent, /旧联系提示译文尚未确认/);
});
