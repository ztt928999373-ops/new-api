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

test('review request is deduplicated by exact SHA and repeated on new SHA', async () => {
  const f = fixture();
  await manage(f); await manage(f);
  assert.equal(f.comments.length, 1);
  assert.match(f.comments[0].body, /@codex review/);
  f.context.payload.pull_request.head.sha = 'def';
  await manage(f);
  assert.equal(f.comments.length, 2);
});

test('Draft, wrong base, closed PR and superseded event never request review', async () => {
  for (const changes of [{draft: true}, {base: {ref: 'main'}}, {state: 'closed'}]) {
    const f = fixture(changes); await manage(f); assert.equal(f.comments.length, 0);
  }
  const f = fixture();
  f.github.rest.pulls.get = async () => ({data: {...f.context.payload.pull_request, head: {sha: 'new'}}});
  await manage(f); assert.equal(f.comments.length, 0);
});

test('review notifications never trigger another review request', async () => {
  const f = fixture(); f.context.eventName = 'pull_request_review';
  f.context.payload.review = {state: 'commented'};
  await manage(f); assert.equal(f.comments.length, 0);
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
