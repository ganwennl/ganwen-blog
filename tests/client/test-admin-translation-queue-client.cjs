'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const harness = fs.readFileSync(path.join(__dirname, 'test-admin-session-client.cjs'), 'utf8');
const harnessRequire = id => id === 'node:test' ? {test() {}} : require(id);
const {client} = new Function('require', '__dirname', harness + '\nreturn {client};')(harnessRequire, __dirname);
const result = patch => ({ok: true, queued: 0, active: 0, skipped: 0, protected: 0, up_to_date: 0, failed: 0, ...patch});

function setup(respond) {
  const c = client(call => call.path === 'session' ? {data: {authenticated: true, configured: true, user: 'editor', csrf: 'queue-fixture'}} : respond(call), '');
  c.run("state.authenticated=true;state.user='editor';state.csrf='queue-fixture';state.post={id:2,status:'publish'};state.dirty=false;reconcileEditorDirty=()=>{}");
  return c;
}

test('ordinary translation reports protected stale manual languages without claiming ten new jobs', async () => {
  const c = setup(call => {
    assert.equal(call.path, 'translate');
    assert.deepEqual(JSON.parse(call.options.body), {id: 2, force: false});
    return {data: result({skipped: 10, protected: 10})};
  });
  c.context.window.confirm = () => {throw Error('ordinary translation must not request overwrite permission');};
  await c.get('queuePostButton').onclick();
  assert.match(c.get('toast').textContent, /新增排队 0 种/);
  assert.match(c.get('toast').textContent, /10 种人工译文受到保护/);
  assert.match(c.get('toast').textContent, /重新翻译/);
  assert.doesNotMatch(c.get('toast').textContent, /已加入十种/);
});

test('explicit retranslation explains manual replacement and cancellation never sends force', async () => {
  const c = setup(() => {throw Error('cancelled overwrite must not call the service');});
  const confirmations = [];
  c.context.window.confirm = message => {confirmations.push(message); return false;};
  await c.get('retranslatePostButton').onclick();
  assert.equal(c.calls.length, 0);
  assert.equal(confirmations.length, 1);
  assert.match(confirmations[0], /覆盖现有译文（包括人工译文）/);
  assert.match(confirmations[0], /公开\/私密状态保持不变/);
});

test('confirmed retranslation sends force and reports actual newly queued versus existing work', async () => {
  const c = setup(call => {
    assert.deepEqual(JSON.parse(call.options.body), {id: 2, force: true});
    return {data: result({queued: 3, active: 7})};
  });
  let confirmations = 0;
  c.context.window.confirm = () => {confirmations++; return true;};
  await c.get('retranslatePostButton').onclick();
  assert.equal(confirmations, 1);
  assert.match(c.get('toast').textContent, /新增排队 3 种；已有任务 7 种；跳过 0 种/);
  assert.equal(c.get('queuePostButton').disabled, false);
  assert.equal(c.get('retranslatePostButton').disabled, false);
});

test('pending translation prevents duplicate normal and force requests and restores both buttons', async () => {
  let release;
  const c = setup(() => new Promise(resolve => {release = resolve;}));
  const first = c.get('queuePostButton').onclick();
  assert.equal(c.get('queuePostButton').disabled, true);
  assert.equal(c.get('retranslatePostButton').disabled, true);
  await c.get('queuePostButton').onclick();
  await c.get('retranslatePostButton').onclick();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(c.calls.filter(call => call.path === 'translate').length, 1);
  release({data: result({queued: 10})});
  await first;
  assert.equal(c.get('queuePostButton').disabled, false);
  assert.equal(c.get('retranslatePostButton').disabled, false);
});

test('dirty editor blocks normal and forced translation before any confirmation or request', async () => {
  const c = setup(() => {throw Error('unsaved source must not be queued');});
  c.state.dirty = true;
  c.context.window.confirm = () => {throw Error('save first');};
  await c.get('queuePostButton').onclick();
  await c.get('retranslatePostButton').onclick();
  assert.equal(c.calls.length, 0);
  assert.match(c.get('toast').textContent, /请先保存当前修改/);
});

test('private sources, unchanged languages, failures and legacy responses never pretend new jobs exist', async () => {
  for (const [data, wanted] of [
    [result({reason: 'not_public', skipped: 10}), /原文尚未公开，未加入翻译队列/],
    [result({reason: 'no_chinese', skipped: 10}), /没有需要翻译的中文，未加入翻译队列/],
    [result({skipped: 10, up_to_date: 10}), /10 种译文已与当前原文一致/],
    [result({skipped: 10, failed: 10}), /10 种失败任务尚未重试/],
    [{ok: true}, /请刷新队列核对结果/]
  ]) {
    const c = setup(() => ({data}));
    await c.get('queuePostButton').onclick();
    assert.match(c.get('toast').textContent, wanted);
    assert.doesNotMatch(c.get('toast').textContent, /已加入十种/);
  }
  const c = setup(() => ({status: 400, data: {error: '翻译服务未配置或当前无法加入队列'}}));
  await c.get('queuePostButton').onclick();
  assert.match(c.get('toast').textContent, /翻译服务未配置或当前无法加入队列/);
  assert.equal(c.get('toast').className, 'toast bad');
  assert.equal(c.get('retranslatePostButton').disabled, false);
});
