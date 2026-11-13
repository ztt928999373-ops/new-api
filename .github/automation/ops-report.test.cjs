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

test('运营周报复用通知、隔离凭据并单次发送五项功能状态', async () => {
  const f = fixture(); await runOps(f);
  assert.equal(f.calls.length, 8);
  assert.equal(f.messages.length, 1);
  const text = f.messages[0];
  assert.match(text, /2026-11-16（北京时间）/);
  for (const name of ['注册', '充值', '钱包', '支付', '对账']) assert.ok(text.includes(name));
  assert.match(text, /代码进度已发现入口；专项测试：暂无可验证数据（未核验）；真实运营条件：暂无可验证数据（未核验）/);
  assert.match(text, /成功1 \/ 失败1 \/ 跳过1 \/ 取消1 \/ 进行中1 \/ 未知1/);
  assert.match(text, /PR #7：支付相关事项/);
  assert.doesNotMatch(text, /private-body|unrelated title/);
  assert.ok(f.notices.includes('运营周报发送成功（Telegram OPS: sent）'));
  assert.equal(f.report(), text);
  assert.ok(text.length < 4000);
});

test('竞品数据不足时保留来源，不编造价格、币种或采集日期', async () => {
  const f = fixture(); await runOps(f); const text = f.report();
  assert.match(text, /暂无可验证数据/);
  for (const url of ['https://openrouter.ai/models', 'https://aihubmix.com/', 'https://302.ai/']) assert.ok(text.includes(url));
  assert.equal((text.match(/采集日期：未采集；币种：未核验/g) || []).length, 3);
  assert.doesNotMatch(text, /USD|CNY|\$\d|¥\d|价格下降|价格上涨/);
});

test('缺少运营凭据时提前失败，不回退读取技术主管凭据', async () => {
  for (const env of [{}, {TELEGRAM_OPS_BOT_TOKEN: 'synthetic'}, {TELEGRAM_OPS_CHAT_ID: 'synthetic'}]) {
    const f = fixture(); f.env = env;
    await assert.rejects(runOps(f), /运营周报配置不完整/);
    assert.equal(f.calls.length, 0); assert.equal(f.messages.length, 0);
  }
});

test('源码证据缺失或检查未运行时，不认定测试通过', async () => {
  const f = fixture();
  f.github.rest.repos.getContent = async () => {throw Object.assign(new Error('not found'), {status: 404});};
  f.github.rest.checks.listForRef = async () => ({data: {check_runs: []}});
  await runOps(f);
  assert.match(f.report(), /未确认已知入口/);
  assert.match(f.report(), /当前 SHA 未查到检查运行，不计测试通过/);
});

test('读取失败停止发送，发送被拒绝时不虚报成功', async () => {
  const f = fixture(); f.github.rest.issues.listForRepo = async () => {throw Error('synthetic-private-response');};
  await assert.rejects(runOps(f)); assert.equal(f.messages.length, 0);
  const g = fixture(); g.fetcher = async () => ({ok: false, status: 403});
  await assert.rejects(runOps(g)); assert.ok(!g.notices.includes('运营周报发送成功（Telegram OPS: sent）'));
});

test('运营事项数量有界，单条通知保留完整栏目', async () => {
  const f = fixture();
  f.github.rest.issues.listForRepo = async () => ({data: Array.from({length: 100}, (_, number) => ({number, title: '支付'.repeat(500)}))});
  await runOps(f);
  const text = f.messages[0];
  assert.equal((text.match(/example\/repo\/issues\//g) || []).length, 5);
  assert.ok(text.length < 4000);
  assert.match(text, /302.AI/); assert.match(text, /不自动修改模型定价/);
});

test('运营工作流仅使用独立凭据，周一定时且错误脱敏', async () => {
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
  assert.equal(failure, '运营周报失败；请查看最后一条阶段提示。未记录敏感错误详情。');
});

test('周报六部分正文为简体中文，外文或繁体任务标题不直接转发', async () => {
  const f = fixture();
  f.github.rest.issues.listForRepo = async () => ({data: [
    {number: 11, title: 'payment refund wallet reconciliation launch', pull_request: {}},
    {number: 12, title: 'billing：錢包風險與對賬，請驗證週報'},
  ]});
  await runOps(f);
  const text = f.messages[0];
  assert.ok(text.startsWith('《算介 API｜运营主管每周报告》'));
  let previous = -1;
  for (const heading of ['一、本周运营概况', '二、AI 模型与竞品动态', '三、商业化 V1 开发进展', '四、主要风险与待解决事项', '五、下周三项重点工作', '六、数据来源与更新时间']) {
    const index = text.indexOf(heading);
    assert.ok(index > previous, heading);
    previous = index;
  }
  assert.match(text, /PR #11：钱包、支付、对账、退款、上线相关事项/);
  assert.match(text, /任务 #12：账务相关事项/);
  assert.doesNotMatch(text, /payment|refund|reconciliation|billing|錢包|風險|對賬|請驗證|週報/);
  const prose = text.replace(/https?:\/\/\S+/g, '').replace(/product\/v1|abc123|OpenRouter|AIHubMix|302\.AI|GitHub|SHA|PR|CI|API|AI|V1/g, '');
  assert.doesNotMatch(prose, /[A-Za-z]/);
  assert.match(text, /不进行价格比较或币种换算/);
  assert.match(text, /专项测试：暂无可验证数据（未核验）；真实运营条件：暂无可验证数据（未核验）/);
  assert.match(text, /结论：本报告仅提供运营建议/);
  assert.equal(f.report(), text);
  assert.ok(text.length < 4000);
});

test('无任务及检查时用简体中文说明数据不足，工作流用户可见名称也为中文', async () => {
  const f = fixture();
  f.github.rest.issues.listForRepo = async () => ({data: []});
  f.github.rest.checks.listForRef = async () => ({data: {check_runs: []}});
  await runOps(f);
  assert.match(f.report(), /暂无可验证数据：未发现标题命中的开放任务/);
  assert.match(f.report(), /暂无可验证数据：当前 SHA 未查到检查运行/);
  assert.ok(f.notices.includes('运营周报阶段：读取 GitHub 数据'));
  assert.ok(f.notices.includes('运营周报发送成功（Telegram OPS: sent）'));
  const yaml = require('node:fs').readFileSync('.github/workflows/v1-ops-weekly.yml', 'utf8');
  assert.match(yaml, /name: 算介 API｜运营主管每周报告/);
  assert.match(yaml, /name: 生成并发送运营周报/);
  assert.match(yaml, /运营周报失败；请查看最后一条阶段提示/);
});
