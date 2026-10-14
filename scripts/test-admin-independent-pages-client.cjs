'use strict';
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

// Execute the shipped admin script with the existing DOM/network contract double.
const harness = fs.readFileSync(path.join(__dirname, 'test-admin-session-client.cjs'), 'utf8');
const harnessRequire = id => id === 'node:test' ? {test() {}} : require(id);
const {client, html} = new Function('require', '__dirname', harness + '\nreturn {client, html};')(harnessRequire, __dirname);

const session = {authenticated: true, configured: true, user: 'editor', csrf: 'independent-session'};
const independent = {id: 29, version: 7, type: 'page', title: 'Bewijs', status: 'publish', unlisted: true, public_path: '/bewijs.html', canonical_url: 'https://ganwen.test/bewijs.html', content_html: '<p>Evidence</p><img src="/wp-content/uploads/evidence.png"><video controls src="/wp-content/uploads/evidence.mp4"></video>', excerpt_html: '<p>Caption</p>', categories: [], tags: [], seo: {}, translations: {}};

function independentClient(respond, query) {
  const c = client(respond, query);
  const createElement = c.context.document.createElement;
  c.context.document.createElement = tag => {
    const node = createElement(tag);
    if (tag === 'template') node.content = {querySelectorAll() {return [];}};
    return node;
  };
  return c;
}

function openFixture(respond, query = '?view=independent&edit=29') {
  const c = independentClient(call => {
    if (call.path === 'session') return {data: session};
    if (call.path === 'post?id=29') return {data: {post: independent, source_hash: 'source-hash'}};
    if (call.path === 'posts') return {data: [independent]};
    if (call.path === 'settings') return {data: {categories: [], tags: []}};
    return respond(call);
  }, query);
  c.state.authenticated = true;
  c.state.user = 'editor';
  c.state.mode = 'source';
  return c;
}

test('independent pages have separate navigation, address controls and direct-access explanation', () => {
  for (const id of ['view-independent', 'newIndependentButton', 'navIndependentCount', 'independentRows', 'publicPath', 'unlistedPage']) {
    assert.match(html, new RegExp('id="' + id + '"'));
  }
  assert.match(html, /data-view="independent"/);
  assert.match(html, /placeholder="\/bewijs\.html"/);
  assert.match(html, /知道地址的人仍可访问/);
  assert.match(html, /返回博客/);
});

test('list membership, address search and counts keep independently exposed pages in their own tab', () => {
  const c = openFixture(() => {throw Error('lists must not fetch');});
  c.state.posts = [
    {id: 1, type: 'post', title: '文章', status: 'publish'},
    {id: 2, type: 'page', title: '关于', status: 'publish'},
    independent,
    {...independent, id: 30, title: '公开证据', unlisted: false, public_path: '/public-proof'},
    {...independent, id: 31, title: '草稿证据', status: 'draft', public_path: '/draft-proof'},
  ];
  c.run('renderPosts()');
  assert.equal(c.get('navPostsCount').textContent, '1');
  assert.equal(c.get('navPagesCount').textContent, '1');
  assert.equal(c.get('navIndependentCount').textContent, '3');
  assert.equal(c.get('pageRows').children.length, 1);
  assert.equal(c.get('independentRows').children.length, 3);
  const direct = c.get('independentRows').children[0].children[2].children[0];
  assert.equal(direct.textContent, '/bewijs.html');
  assert.equal(direct.href, '/bewijs.html');
  assert.equal(direct.rel, 'noopener');
  assert.equal(c.get('independentRows').children[2].children[2].children[0].href, undefined, 'drafts must not offer a public link');
  c.get('independentSearch').value = '/public-proof';
  c.run("renderDocumentList('independent')");
  assert.equal(c.get('independentRows').children.length, 1);
  assert.equal(c.get('independentRows').children[0].children[0].children[0].textContent, '公开证据');
  assert.equal(c.get('pageRows').children.length, 1);
  c.get('clearIndependentFilters').onclick();
  assert.equal(c.get('independentRows').children.length, 3);
});

test('new independent pages start as an address-only draft and normal pages retain their existing editor', () => {
  const c = openFixture(() => {throw Error('new pages must not fetch');}, '?view=independent');
  c.get('newIndependentButton').onclick();
  assert.equal(c.state.post.type, 'page');
  assert.equal(c.state.post.status, 'draft');
  assert.equal(c.state.post.unlisted, true);
  assert.equal(c.state.post.public_path, '');
  assert.equal(c.state.returnView, 'independent');
  assert.equal(c.get('editorHeading').textContent, '新建独立页面');
  assert.equal(c.get('savePostButton').textContent, '保存独立页面');
  assert.equal(c.get('backPosts').textContent, '← 返回独立页面');
  assert.equal(c.get('publicPathField').classList.contains('hidden'), false);
  assert.equal(c.get('unlistedPage').checked, true);
  assert.equal(c.state.dirty, false);
  c.run("newDocument('page')");
  assert.equal(c.state.returnView, 'pages');
  assert.equal(c.get('editorHeading').textContent, '新建页面');
  assert.equal(c.get('publicPathField').classList.contains('hidden'), true);
  assert.equal(c.state.post.unlisted, false);
});

test('address and visibility changes participate in dirty checks and cancelled navigation preserves them', async () => {
  const c = openFixture(() => {throw Error('dirty checks must not fetch');});
  await c.run('openPost(29)');
  assert.equal(c.state.returnView, 'independent');
  c.get('publicPath').value = '/new-proof';
  assert.equal(c.run('reconcileEditorDirty()'), true);
  c.context.window.confirm = () => false;
  assert.equal(c.run('closeEditor()'), false);
  assert.equal(c.get('publicPath').value, '/new-proof');
  c.get('publicPath').value = '/bewijs.html';
  assert.equal(c.run('reconcileEditorDirty()'), false);
  c.get('unlistedPage').checked = false;
  assert.equal(c.run('reconcileEditorDirty()'), true);
  c.get('unlistedPage').checked = true;
  assert.equal(c.run('reconcileEditorDirty()'), false);
});

test('saving edits carries the version, evidence HTML, address and visibility without losing list routing', async () => {
  let saved;
  const c = openFixture(call => {
    assert.equal(call.path, 'post');
    saved = JSON.parse(call.options.body);
    return {data: {post: {...saved, version: 8, canonical_url: 'https://ganwen.test/new-proof'}, source_hash: 'updated-hash'}};
  });
  await c.run('openPost(29)');
  c.get('publicPath').value = ' /new-proof ';
  c.get('unlistedPage').checked = false;
  c.get('postTitle').value = 'Edited proof';
  c.run('refreshEditorDirty()');
  await c.run('saveEditor()');
  assert.equal(saved.id, 29);
  assert.equal(saved.version, 7);
  assert.equal(saved.type, 'page');
  assert.equal(saved.public_path, '/new-proof');
  assert.equal(saved.unlisted, false);
  assert.equal(saved.content_html, independent.content_html);
  assert.equal(saved.excerpt_html, independent.excerpt_html);
  assert.equal(saved.title, 'Edited proof');
  assert.equal(saved.translations, undefined);
  assert.equal(saved.source_html, undefined);
  assert.equal(c.state.post.version, 8);
  assert.equal(c.state.returnView, 'independent');
  assert.equal(c.state.dirty, false);
  assert.equal(c.get('publicPath').value, '/new-proof');
  assert.equal(c.get('savePostButton').textContent, '保存独立页面');
  assert.equal(c.messages.length, 0);
  assert.equal(c.get('toast').textContent, '已保存');
});

test('a new address-only draft uses the create API and remains private to the author until publishing', async () => {
  let payload;
  const c = openFixture(call => {
    assert.equal(call.path, 'create');
    payload = JSON.parse(call.options.body);
    return {data: {post: {...payload, id: 35, version: 1}, source_hash: 'created-hash'}};
  }, '?view=independent');
  c.run("newDocument('independent')");
  c.get('publicPath').value = '/bewijs.html';
  c.get('postTitle').value = 'Proof draft';
  c.get('sourceEditor').value = independent.content_html;
  await c.run('saveEditor()');
  assert.equal(payload.type, 'page');
  assert.equal(payload.status, 'draft');
  assert.equal(payload.unlisted, true);
  assert.equal(payload.public_path, '/bewijs.html');
  assert.equal(payload.content_html, independent.content_html);
  assert.equal(c.state.post.id, 35);
  assert.equal(c.state.post.version, 1);
  assert.equal(c.state.returnView, 'independent');
  assert.equal(c.state.dirty, false);
});

test('discarding a shared-setting draft when changing languages restores its saved path and visibility', async () => {
  const c = openFixture(() => {throw Error('language changes must not write');});
  await c.run('openPost(29)');
  c.get('publicPath').value = '/discarded-proof';
  c.get('unlistedPage').checked = false;
  c.state.lang = 'en';
  c.run('renderLanguage()');
  assert.equal(c.get('publicPath').value, '/bewijs.html');
  assert.equal(c.get('unlistedPage').checked, true);
  assert.equal(c.get('publicPath').disabled, true);
  c.state.lang = 'zh';
  c.run('renderLanguage()');
  assert.equal(c.get('publicPath').value, '/bewijs.html');
  assert.equal(c.get('unlistedPage').checked, true);
  assert.equal(c.get('publicPath').disabled, false);
  assert.equal(c.state.dirty, false);
});

test('invalid addresses never upload evidence or call the save API, even for a draft', async () => {
  for (const value of ['', 'proof', '//evil.test/proof', '/\\evil.test/proof', '/proof?x=1', '/proof#evidence', '/proof page', 'https://evil.test/proof']) {
    const c = openFixture(() => {throw Error('invalid paths must not write');}, '?view=independent');
    c.run("newDocument('independent');normalizeInlineImages=async()=>{throw Error('invalid path reached media upload')}");
    c.get('publicPath').value = value;
    c.get('postTitle').value = 'Proof draft';
    c.run('refreshEditorDirty()');
    await c.run('saveEditor()');
    assert.equal(c.calls.length, 0, 'invalid address reached the API: ' + value);
    assert.match(c.get('toast').textContent, /请填写以 \/ 开头/);
    assert.equal(c.get('publicPath').value, value);
    assert.equal(c.state.dirty, true);
    assert.equal(c.state.saving, false);
    assert.equal(c.get('editorGrid').inert, false);
  }
});

test('a rejected save retains address, visibility and evidence draft for a version-preserving retry', async () => {
  let attempts = 0;
  const c = openFixture(call => {
    assert.equal(call.path, 'post');
    const payload = JSON.parse(call.options.body);
    assert.equal(payload.version, 7);
    if (++attempts === 1) return {status: 409, data: {error: '访问地址已被占用'}};
    return {data: {post: {...payload, version: 8}, source_hash: 'retry-hash'}};
  });
  await c.run('openPost(29)');
  c.get('publicPath').value = '/new-proof';
  c.get('sourceEditor').value = '<p>Changed evidence</p>';
  c.run('refreshEditorDirty()');
  await c.run('saveEditor()');
  assert.equal(c.state.post.public_path, '/bewijs.html');
  assert.equal(c.get('publicPath').value, '/new-proof');
  assert.equal(c.get('sourceEditor').value, '<p>Changed evidence</p>');
  assert.equal(c.state.dirty, true);
  assert.equal(c.state.post.version, 7);
  assert.equal(c.get('savePostButton').disabled, false);
  await c.run('saveEditor()');
  assert.equal(attempts, 2);
  assert.equal(c.state.dirty, false);
  assert.equal(c.state.post.public_path, '/new-proof');
});

test('embedded saves announce the authoritative saved address with the matching editor session', async () => {
  const c = openFixture(call => {
    assert.equal(call.path, 'post');
    const payload = JSON.parse(call.options.body);
    assert.equal(payload.public_path, '/entered-proof');
    return {data: {post: {...payload, public_path: '/saved-proof', version: 8}, source_hash: 'saved-hash', public_languages: ['zh']}};
  }, '?embed=1&edit=29&editor_session=proof-path-instance');
  await c.run('openPost(29)');
  c.get('publicPath').value = '/entered-proof';
  await c.run('saveEditor()');
  const saved = c.messages.at(-1);
  assert.equal(saved.message.type, 'ganwen-editor-saved');
  assert.equal(saved.message.publicPath, '/saved-proof');
  assert.equal(saved.message.session, 'proof-path-instance');
  assert.equal(saved.message.id, 29);
  assert.equal(saved.origin, c.context.location.origin);
  c.state.post.public_path = '';
  c.run("notifyEditorParent('ganwen-editor-saved',{public_languages:['zh']})");
  assert.equal(c.messages.at(-1).message.publicPath, '', 'removing an address must be an explicit update');
});

test('translated embedded editing locks shared settings and preserves its session and independent return view', async () => {
  const post = {...independent, translations: {en: {title: 'English proof', content_html: '<p>Translated evidence</p>', source_hash: 'source-hash', status: 'publish', seo: {}}}};
  let payload;
  const c = independentClient(call => {
    if (call.path === 'session') return {data: session};
    if (call.path === 'settings') return {data: {categories: [], tags: []}};
    if (call.path === 'post?id=29') return {data: {post, source_hash: 'source-hash'}};
    assert.equal(call.path, 'translation');
    payload = JSON.parse(call.options.body);
    return {data: {post: {...post, version: 8, translations: {en: payload.translation}}, source_hash: 'source-hash', public_languages: ['zh', 'en']}};
  }, '?embed=1&edit=29&editor_session=proof-instance&editor_language=en');
  c.state.mode = 'source';
  await c.run('start()');
  assert.equal(c.state.returnView, 'independent');
  assert.equal(c.get('publicPath').disabled, true);
  assert.equal(c.get('unlistedPage').disabled, true);
  c.get('postTitle').value = 'Updated English proof';
  await c.run('saveEditor()');
  assert.equal(payload.id, 29);
  assert.equal(payload.version, 7);
  assert.equal(payload.language, 'en');
  assert.equal(payload.translation.public_path, undefined);
  assert.equal(payload.translation.unlisted, undefined);
  assert.equal(c.state.post.public_path, '/bewijs.html');
  assert.equal(c.state.post.unlisted, true);
  const saved = c.messages.at(-1);
  assert.equal(saved.message.type, 'ganwen-editor-saved');
  assert.equal(saved.message.id, 29);
  assert.equal(saved.message.session, 'proof-instance');
  assert.equal(saved.message.publicPath, '/bewijs.html');
  assert.equal(saved.origin, c.context.location.origin);
});
