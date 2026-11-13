const {test} = require('node:test');
const assert = require('node:assert/strict');
const {manage, notify} = require('./manager.cjs');

function fixture(overrides = {}) {
  const comments = [], notices = [];
  const pr = {number: 22, base: {ref: 'product/v1'}, head: {sha: 'abc'}, state: 'open', draft: false, html_url: 'https://github.com/example/repo/pull/22', ...overrides};
  const github = {rest: {pulls: {get: async () => ({data: pr})}, issues: {
    listComments: 'comments', createComment: async c => comments.push(c),
  }}, paginate: async () => comments.map(c => ({body: c.body, user: {type: 'Bot'}}))};
  const context = {repo: {owner: 'example', repo: 'repo'}, eventName: 'pull_request_target', payload: {pull_request: pr, action: 'opened'}};
  const core = {notice: s => notices.push(s), summary: {addHeading() {return this;}, addRaw() {return this;}, async write() {}}};
  return {github, context, core, comments, notices};
}

test('all PR events are notification-only and never write GitHub', async () => {
  for (const eventName of ['pull_request_target', 'pull_request_review']) {
    const f = fixture(); f.context.eventName = eventName;
    f.github.rest.issues = new Proxy({}, {get() {throw Error('No GitHub writes allowed');}});
    await manage(f); assert.equal(f.comments.length, 0);
  }
});

test('stale review is attributed to its own SHA, never the current head', async () => {
  const f = fixture(); let summary = '';
  f.context.eventName = 'pull_request_review';
  f.context.payload.review = {state: 'approved', commit_id: 'older'};
  f.core.summary.addRaw = s => {summary = s; return f.core.summary;};
  await manage(f);
  assert.match(summary, /审核 SHA：older/);
  assert.match(summary, /历史提交审核，不适用于当前 head/);
});

test('missing configuration skips delivery; Telegram rejects and HTTP failures fail visibly', async () => {
  assert.equal(await notify('status', {}, () => {throw Error('must not call');}), 'not-configured');
  const env = {TELEGRAM_BOT_TOKEN: 'synthetic', TELEGRAM_CHAT_ID: '123'};
  await assert.rejects(notify('status', env, async () => ({ok: false, status: 403})), /HTTP 403/);
  await assert.rejects(notify('status', env, async () => ({ok: true, json: async () => ({ok: false})})), /rejected/);
  assert.equal(await notify('status', env, async (url, options) => {
    assert.equal(new URL(url).hostname, 'api.telegram.org');
    assert.equal(options.redirect, 'error');
    assert.equal(JSON.parse(options.body).disable_web_page_preview, true);
    return {ok: true, json: async () => ({ok: true})};
  }), 'sent');
});

test('report distinguishes absent, skipped and failed checks from success and ignores old runs', async () => {
  const f = fixture(); delete f.context.payload.pull_request;
  let report = '';
  f.core.summary.addRaw = s => {report = s; return f.core.summary;};
  f.github.rest.repos = {getBranch: async () => ({data: {commit: {sha: 'base'}}})};
  f.github.rest.pulls.list = 'prs'; f.github.rest.pulls.listReviews = 'reviews';
  f.github.rest.checks = {listForRef: 'checks'};
  f.github.paginate = async method => method === 'prs' ? [{number: 22, head: {sha: 'abc'}, draft: true, html_url: 'url'}] : method === 'reviews' ? [] : [
    {id: 1, name: 'CI', status: 'completed', conclusion: 'failure'},
    {id: 2, name: 'CI', status: 'completed', conclusion: 'success'},
    {id: 3, name: 'Security', status: 'completed', conclusion: 'skipped'},
  ];
  await manage(f);
  assert.match(report, /1 success \/ 0 failure \/ 1 pending-or-other/);
  assert.match(report, /待关注 PR：1\/1/);
  assert.match(report, /当前 SHA 审核记录：0/);
});


test('outstanding change requests remain risks after comment or new head; new approval clears decision', async () => {
  for (const [lastState, sha, expected] of [['COMMENTED', 'abc', 1], ['APPROVED', 'abc', 0], ['APPROVED', 'old', 1]]) {
    const f = fixture(); delete f.context.payload.pull_request; let report = '';
    f.core.summary.addRaw = s => {report = s; return f.core.summary;};
    f.github.rest.repos = {getBranch: async () => ({data: {commit: {sha: 'base'}}})};
    Object.assign(f.github.rest.pulls, {list: 'prs', listReviews: 'reviews'});
    f.github.rest.checks = {listForRef: 'checks'};
    f.github.paginate = async method => method === 'prs' ? [{number: 23, head: {sha: 'abc'}, html_url: 'url'}] : method === 'checks' ? [{id: 1, name: 'CI', status: 'completed', conclusion: 'success'}] : [
      {id: 1, user: {login: 'reviewer'}, commit_id: 'old', state: 'CHANGES_REQUESTED'},
      {id: 2, user: {login: 'reviewer'}, commit_id: sha, state: lastState},
    ];
    await manage(f); assert.match(report, new RegExp(`待关注 PR：${expected}/1`));
  }
});

test('workflow bootstrap skips absent trusted script without requiring any PR code', async () => {
  const fs = require('node:fs');
  const source = fs.readFileSync('.github/workflows/v1-ai-manager.yml', 'utf8').split('          script: |\n')[1].split('\n').map(l => l.slice(12)).join('\n');
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  const f = fixture(); let loaded = false;
  const loader = name => {
    if (name === 'node:fs') return {existsSync: () => false};
    if (name === 'node:path') return require('node:path');
    loaded = true; throw Error('PR code must never be loaded');
  };
  await new AsyncFunction('require', 'process', 'github', 'context', 'core', source)(loader, {env: {GITHUB_WORKSPACE: '/synthetic/trusted-base'}}, f.github, f.context, f.core);
  assert.equal(loaded, false);
  assert.match(f.notices[0], /safely skipped/);
});

test('installed manager load/transport errors fail with fixed message and redact token-bearing errors', async () => {
  const fs = require('node:fs');
  const source = fs.readFileSync('.github/workflows/v1-ai-manager.yml', 'utf8').split('          script: |\n')[1].split('\n').map(l => l.slice(12)).join('\n');
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  for (const phase of ['load', 'transport']) {
    const f = fixture(); let failure = '';
    f.core.setFailed = message => {failure = message;};
    const loader = name => {
      if (name === 'node:fs') return {existsSync: () => true};
      if (name === 'node:path') return require('node:path');
      if (phase === 'load') throw Error('missing dependency: synthetic-secret');
      return {manage: async () => {throw Error('https://api.telegram.org/botsynthetic-secret/sendMessage');}};
    };
    await new AsyncFunction('require', 'process', 'github', 'context', 'core', source)(loader, {env: {GITHUB_WORKSPACE: '/synthetic/trusted-base'}}, f.github, f.context, f.core);
    assert.equal(failure, 'AI manager failed; check permissions, integration and secret configuration.');
    assert.doesNotMatch(failure, /synthetic-secret/);
    assert.equal(f.notices.length, 0);
  }
});
