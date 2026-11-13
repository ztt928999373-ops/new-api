const {test} = require('node:test');
const assert = require('node:assert/strict');
const {runOps} = require('./ops-report.cjs');

function fixture() {
  const calls = [], notices = [], messages = [];
  let report = '';
  const env = new Proxy({TELEGRAM_OPS_BOT_TOKEN: 'synthetic-ops-token', TELEGRAM_OPS_CHAT_ID: 'synthetic-ops-chat'}, {
    get(target, key) {
      assert.notEqual(key, 'TELEGRAM_BOT_TOKEN');
      assert.notEqual(key, 'TELEGRAM_CHAT_ID');
      return target[key];
    },
  });
  const github = {rest: {
    repos: {
      getBranch: async args => {calls.push(args); return {data: {commit: {sha: 'abc123'}}};},
      getContent: async args => {
        calls.push(args); assert.equal(args.ref, 'abc123');
        const code = 'func Register( type TopUp struct Quota func ImportSupplierCost( func isStripeTopUpEnabled(';
        return {data: {type: 'file', encoding: 'base64', content: Buffer.from(code).toString('base64')}};
      },
    },
    issues: {listForRepo: async args => {
      calls.push(args);
      return {data: [
        {number: 7, title: '支付沙箱待验收', pull_request: {}, body: 'private-body-must-not-send'},
        {number: 8, title: 'unrelated title'},
      ]};
    }},
    checks: {listForRef: async args => {
      calls.push(args);
      return {data: {check_runs: [
        {id: 1, name: 'CI', status: 'completed', conclusion: 'failure'},
        {id: 2, name: 'CI', status: 'completed', conclusion: 'success'},
        {id: 3, name: 'skip', status: 'completed', conclusion: 'skipped'},
        {id: 4, name: 'cancel', status: 'completed', conclusion: 'cancelled'},
        {id: 5, name: 'pending', status: 'in_progress'},
        {id: 6, name: 'unknown', status: 'completed', conclusion: 'neutral'},
        {id: 7, name: 'security', status: 'completed', conclusion: 'failure'},
      ]}};
    }},
  }};
  const core = {notice: text => notices.push(text), summary: {addRaw(text) {report = text; return this;}, async write() {}}};
  const fetcher = async (url, options) => {
    assert.equal(new URL(url).hostname, 'api.telegram.org');
    assert.ok(url.includes('synthetic-ops-token'));
    assert.equal(options.redirect, 'error');
    const body = JSON.parse(options.body);
    assert.equal(body.chat_id, 'synthetic-ops-chat');
    messages.push(body.text);
    return {ok: true, json: async () => ({ok: true})};
  };
  return {github, core, env, fetcher, context: {repo: {owner: 'example', repo: 'repo'}, runId: 123}, now: new Date('2026-11-15T16:05:00Z'), calls, notices, messages, report: () => report};
}

test('OPS weekly report reuses transport, isolates Secrets and sends all five feature states once', async () => {
  const f = fixture(); await runOps(f);
  assert.equal(f.calls.length, 8);
  assert.equal(f.messages.length, 1);
  const text = f.messages[0];
  assert.match(text, /2026-11-16（北京时间）/);
  for (const name of ['注册', '充值', '钱包', '支付', '对账']) assert.ok(text.includes(name));
  assert.match(text, /已有代码入口；专项测试未核验；真实运营条件未核验/);
  assert.match(text, /成功1 \/ 失败1 \/ 跳过1 \/ 取消1 \/ 进行中1 \/ 未知1/);
  assert.match(text, /#7 支付沙箱待验收/);
  assert.doesNotMatch(text, /private-body|unrelated title/);
  assert.ok(f.notices.includes('Telegram OPS: sent'));
  assert.equal(f.report(), text);
  assert.ok(text.length < 4000);
});

test('competitor fallback has source URLs, no fabricated price, currency or collection date', async () => {
  const f = fixture(); await runOps(f); const text = f.report();
  assert.match(text, /暂无可验证更新/);
  for (const url of ['https://openrouter.ai/models', 'https://aihubmix.com/', 'https://302.ai/']) assert.ok(text.includes(url));
  assert.equal((text.match(/采集日期：未采集；币种：未核验/g) || []).length, 3);
  assert.doesNotMatch(text, /USD|CNY|\$\d|¥\d|价格下降|价格上涨/);
});

test('missing OPS Secrets fails before reads or sending with no technical fallback', async () => {
  for (const env of [{}, {TELEGRAM_OPS_BOT_TOKEN: 'synthetic'}, {TELEGRAM_OPS_CHAT_ID: 'synthetic'}]) {
    const f = fixture(); f.env = env;
    await assert.rejects(runOps(f), /OPS configuration incomplete/);
    assert.equal(f.calls.length, 0); assert.equal(f.messages.length, 0);
  }
});

test('404 source evidence and absent checks remain unconfirmed rather than tested', async () => {
  const f = fixture();
  f.github.rest.repos.getContent = async () => {throw Object.assign(new Error('not found'), {status: 404});};
  f.github.rest.checks.listForRef = async () => ({data: {check_runs: []}});
  await runOps(f);
  assert.match(f.report(), /已知入口未确认/);
  assert.match(f.report(), /当前SHA未查到检查运行，不计测试通过/);
});

test('GitHub errors stop delivery and Telegram rejection never reports sent', async () => {
  const f = fixture(); f.github.rest.issues.listForRepo = async () => {throw Error('synthetic-private-response');};
  await assert.rejects(runOps(f)); assert.equal(f.messages.length, 0);
  const g = fixture(); g.fetcher = async () => ({ok: false, status: 403});
  await assert.rejects(runOps(g)); assert.ok(!g.notices.includes('Telegram OPS: sent'));
});

test('bounded operational titles leave all sections in the single Telegram message', async () => {
  const f = fixture();
  f.github.rest.issues.listForRepo = async () => ({data: Array.from({length: 100}, (_, number) => ({number, title: '支付'.repeat(500)}))});
  await runOps(f);
  const text = f.messages[0];
  assert.equal((text.match(/example\/repo\/issues\//g) || []).length, 5);
  assert.ok(text.length < 4000);
  assert.match(text, /302.AI/); assert.match(text, /不自动修改模型定价/);
});

test('OPS workflow uses only OPS Secrets, Monday UTC schedule and redacts failures', async () => {
  const fs = require('node:fs');
  const yaml = fs.readFileSync('.github/workflows/v1-ops-weekly.yml', 'utf8');
  assert.match(yaml, /10 1 \* \* 1/);
  assert.doesNotMatch(yaml, /secrets\.TELEGRAM_BOT_TOKEN|secrets\.TELEGRAM_CHAT_ID|pull_request:|push:|write|secrets: inherit/);
  const source = yaml.split('          script: |\n')[1].split('\n').map(l => l.slice(12)).join('\n');
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  let failure = '';
  await new AsyncFunction('require', 'github', 'context', 'core', source)(
    () => ({runOps: async () => {throw Error('synthetic-ops-token synthetic-ops-chat private-response sensitive-url');}}), {}, {}, {setFailed: text => {failure = text;}},
  );
  assert.equal(failure, 'OPS report failed; inspect the last fixed stage marker. No sensitive error details logged.');
});
