const {test} = require('node:test');
const assert = require('node:assert/strict');
const {classify, runUpstream} = require('./upstream-monitor.cjs');
const A = 'a'.repeat(40), B = 'b'.repeat(40), C = 'c'.repeat(40);

function fixture() {
  const summaries = [], failures = [], sends = [], saved = [], requests = [];
  const commits = [{sha: B, commit: {message: 'fix(security): prevent XSS'}}];
  const f = {
    github: {rest: {repos: {
      getBranch: async p => {requests.push(p); return {data: {commit: {sha: p.owner === 'QuantumNous' ? B : A}}};},
      compareCommitsWithBasehead: async p => {requests.push(p); return {data: {status: 'ahead', ahead_by: commits.length, behind_by: 2, total_commits: commits.length, commits}};},
    }}},
    context: {repo: {owner: 'ztt928999373-ops', repo: 'new-api'}, ref: 'refs/heads/product/v1', runId: 123},
    core: {setFailed: m => failures.push(m), summary: {addRaw(s) {summaries.push(s);return this;}, async write() {}}},
    env: {TELEGRAM_BOT_TOKEN: 'synthetic-only', TELEGRAM_CHAT_ID: '123'},
    fetcher: async (url, opts) => {sends.push({url, opts});return {ok: true, json: async () => ({ok: true})};},
    save: async state => saved.push(state),
  };
  return {f, summaries, failures, sends, saved, requests, commits};
}

test('分类为可多选线索，未知内容不冒充安全结论', () => {
  assert.deepEqual(classify('fix(security): CVE-2026-1234'), ['安全修复']);
  assert.deepEqual(classify('feat: schema migration'), ['功能变化', '数据库迁移']);
  assert.deepEqual(classify('chore(deps): bump package'), ['依赖升级']);
  assert.deepEqual(classify('fix: unrelated change'), ['其他']);
});
test('有更新：官方原始链接、中文待核实报告及复用通知，成功后保存', async () => {
  const x = fixture(); assert.equal((await runUpstream(x.f)).status, 'sent');
  assert.equal(x.sends.length, 1); assert.deepEqual(x.saved, [{version: 1, notifiedHead: B}]);
  const text = x.summaries.join('\n');
  assert.match(text, /安全修复：1（待核实）/);assert.ok(text.includes(`https://github.com/QuantumNous/new-api/commit/${B}`));
  const body = JSON.parse(x.sends[0].opts.body).text;
  assert.match(body, /待核实/); assert.ok(!body.includes('fix(security)'));
  assert.equal(new URL(x.sends[0].url).hostname, 'api.telegram.org');
  assert.equal(x.sends[0].opts.redirect, 'error');
  assert.equal(x.requests[2].basehead, `${A}...QuantumNous:${B}`);
});
test('官方无变化：仍比较最新产品，不重复通知并续存可信检查点', async () => {
  const x = fixture();x.f.previous = {version: 1, notifiedHead: B};
  assert.equal((await runUpstream(x.f)).status, 'duplicate');
  assert.equal(x.sends.length, 0);assert.equal(x.requests.length, 3);assert.equal(x.saved.length, 1);
});
test('首次没有差异或仅其他变化：简短通知不宣称不存在漏洞', async () => {
  for (const empty of [true, false]) {
    const x = fixture();x.commits.splice(0, 1, ...empty ? [] : [{sha: B, commit: {message: 'docs: text'}}]);
    assert.equal((await runUpstream(x.f)).status, 'sent');
    assert.match(JSON.parse(x.sends[0].opts.body).text, /未识别到重要更新线索（待核实）/);
  }
});
test('增量报告只列上次通知之后的提交，仍报告完整产品差异', async () => {
  const x = fixture();x.f.previous = {version: 1, notifiedHead: C};
  await runUpstream(x.f);assert.equal(x.requests[3].owner, 'QuantumNous');assert.equal(x.requests[3].basehead, `${C}...${B}`);
});
test('上游失败、分页截断、异常检查点及历史改写不当作无更新，也不推进状态', async () => {
  for (const mode of ['access', 'limit', 'truncated', 'checkpoint', 'rewrite']) {
    const x = fixture();
    if (mode === 'access') x.f.github.rest.repos.getBranch = async () => {throw Error('secret-url-token');};
    if (mode === 'checkpoint') x.f.previous = {version: 1, notifiedHead: 'invalid'};
    if (['limit','truncated','rewrite'].includes(mode)) {
      x.f.github.rest.repos.compareCommitsWithBasehead = async () => ({data: {status: 'diverged', total_commits: mode === 'limit' ? 501 : mode === 'truncated' ? 2 : 1, commits: x.commits}});
      if (mode === 'rewrite') x.f.previous = {version: 1, notifiedHead: C};
    }
    assert.equal((await runUpstream(x.f)).status, 'failed', mode);
    assert.equal(x.sends.length, 0);assert.equal(x.saved.length, 0);assert.equal(x.failures.length, 1);
    assert.ok(!JSON.stringify(x.summaries).includes('secret-url-token'));
  }
});
test('跨页完整读取且拒绝重复页，不静默丢弃提交', async () => {
  const x = fixture();
  x.f.github.rest.repos.compareCommitsWithBasehead = async p => ({data: {status: 'ahead', total_commits: 2, ahead_by: 2, behind_by: 0, commits: p.page === 1 ? x.commits : [{sha: C, commit: {message: 'feat: synthetic'}}]}});
  assert.equal((await runUpstream(x.f)).status, 'sent');assert.match(x.summaries.join(''), /功能变化：1/);
});
test('Telegram 未配置、部分配置、发送失败：不推进检查点且不泄漏错误', async () => {
  for (const mode of ['missing','partial','failure']) {
    const x = fixture();
    if (mode === 'missing') x.f.env = {};
    if (mode === 'partial') x.f.env = {TELEGRAM_BOT_TOKEN: 'synthetic-only'};
    if (mode === 'failure') x.f.fetcher = async () => {throw Error('secret-url-token');};
    assert.equal((await runUpstream(x.f)).status, mode === 'missing' ? 'not-configured' : 'notification-failed');
    assert.equal(x.saved.length, 0);assert.ok(!JSON.stringify(x.failures).includes('secret-url-token'));
  }
});
test('拒绝非可信分支，不发起任何网络请求', async () => {
  const x = fixture();x.f.context.ref = 'refs/heads/main';
  await assert.rejects(runUpstream(x.f));assert.equal(x.requests.length, 0);
});
