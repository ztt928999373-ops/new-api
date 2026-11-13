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
  await assert.rejects(notify('status', env, async () => ({ok: false, status: 403})), {status: 403});
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
  f.github.paginate = async (method, params, mapPage) => {
    const data = method === 'prs' ? [{number: 22, head: {sha: 'abc'}, draft: true, html_url: 'url'}] : method === 'reviews' ? [] : [
    {id: 1, name: 'CI', status: 'completed', conclusion: 'failure'},
    {id: 2, name: 'CI', status: 'completed', conclusion: 'success'},
    {id: 3, name: 'Security', status: 'completed', conclusion: 'skipped'},
  ];
    // Octokit normalizes check_runs to response.data before invoking mapPage.
    return mapPage ? [].concat(mapPage({data})) : data;
  };
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

test('manual report sends once through normalized pagination and marks Telegram sent', async () => {
  const f = fixture(); delete f.context.payload.pull_request;
  f.env = {TELEGRAM_BOT_TOKEN: 'synthetic-token', TELEGRAM_CHAT_ID: 'synthetic-chat'};
  f.github.rest.repos = {getBranch: async () => ({data: {commit: {sha: 'base'}}})};
  Object.assign(f.github.rest.pulls, {list: 'prs', listReviews: 'reviews'});
  f.github.rest.checks = {listForRef: 'checks'};
  f.github.paginate = async (method, params, mapPage) => {
    const data = method === 'prs' ? [{number: 23, head: {sha: 'abc'}, html_url: 'github-pr'}] : method === 'reviews' ? [] : [{id: 1, name: 'CI', status: 'completed', conclusion: 'success'}];
    return mapPage ? [].concat(mapPage({data})) : data;
  };
  let sent = 0;
  f.fetcher = async () => {sent++; return {ok: true, json: async () => ({ok: true})};};
  await manage(f);
  assert.equal(sent, 1);
  assert.ok(f.notices.includes('Telegram: sent'));
  assert.ok(f.notices.indexOf('AI Manager stage: report-generation: success') < f.notices.indexOf('AI Manager stage: telegram-send: start'));
});

test('failure stages distinguish configuration, GitHub, report and Telegram without leaking inputs', async () => {
  for (const phase of ['configuration', 'github-read-prs', 'github-read-branch', 'github-read-checks', 'github-read-reviews', 'report-generation', 'telegram-send-http', 'telegram-send-network']) {
    const f = fixture(); delete f.context.payload.pull_request;
    f.env = {TELEGRAM_BOT_TOKEN: 'synthetic-token', TELEGRAM_CHAT_ID: 'synthetic-chat'};
    const tainted = Object.assign(new Error('synthetic-token synthetic-chat private-response https://example.invalid/request'), {status: 403});
    f.github.rest.repos = {getBranch: async () => {
      if (phase === 'github-read-branch') throw tainted;
      return {data: {commit: {sha: 'base'}}};
    }};
    Object.assign(f.github.rest.pulls, {list: 'prs', listReviews: 'reviews'});
    f.github.rest.checks = {listForRef: 'checks'};
    f.github.paginate = async method => {
      if (phase === `github-read-${method}`) throw tainted;
      return method === 'prs' ? [{number: 23, head: {sha: 'abc'}, html_url: 'github-pr'}] : [];
    };
    if (phase === 'configuration') delete f.env.TELEGRAM_CHAT_ID;
    if (phase === 'report-generation') f.core.summary.write = async () => {throw tainted;};
    let sent = 0;
    f.fetcher = async () => {
      sent++;
      if (phase === 'telegram-send-network') throw tainted;
      return {ok: false, status: 403};
    };
    const stage = phase.startsWith('telegram-send') ? 'telegram-send' : phase;
    await assert.rejects(manage(f), error => {
      assert.match(error.message, new RegExp(`^AI Manager stage: ${stage}: failed`));
      assert.doesNotMatch(error.message, /synthetic-token|synthetic-chat|private-response|https:/);
      return true;
    });
    assert.doesNotMatch(f.notices.join('\n'), /synthetic-token|synthetic-chat|private-response|https:/);
    assert.equal(sent, phase.startsWith('telegram-send') ? 1 : 0);
    assert.ok(!f.notices.includes('Telegram: sent'));
  }
});
