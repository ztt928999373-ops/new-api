// Reuse the established transport without reading the technical supervisor's Secrets.
const {notify} = require('./manager.cjs');
const MAIN = 'product/v1';
const FEATURES = [
  ['注册', 'controller/user.go', 'func Register('],
  ['充值', 'model/topup.go', 'type TopUp struct'],
  ['钱包（额度余额）', 'model/user.go', 'Quota'],
  ['对账（仅供应商费用入口）', 'controller/supplier_cost.go', 'func ImportSupplierCost('],
  ['支付', 'controller/payment_webhook_availability.go', 'func isStripeTopUpEnabled('],
];
const TOPICS = [
  [/注册|register/i, '注册'], [/充值|top.?up/i, '充值'], [/钱包|wallet/i, '钱包'],
  [/支付|payment/i, '支付'], [/对账|reconcil/i, '对账'], [/退款|refund/i, '退款'],
  [/定价|pricing/i, '定价'], [/billing/i, '账务'], [/商业/i, '商业化'],
  [/风险/i, '风险'], [/launch/i, '上线'], [/运营|operation/i, '运营'],
];
const SOURCES = [
  ['OpenRouter', 'https://openrouter.ai/models'],
  ['AIHubMix', 'https://aihubmix.com/'],
  ['302.AI', 'https://302.ai/'],
];

async function runOps({github, context, core, env = process.env, fetcher = fetch, now = new Date()}) {
  core.notice('运营周报阶段：配置检查');
  // Explicit mapping only: never fall back to technical supervisor variables.
  const transportEnv = {TELEGRAM_BOT_TOKEN: env.TELEGRAM_OPS_BOT_TOKEN, TELEGRAM_CHAT_ID: env.TELEGRAM_OPS_CHAT_ID};
  if (!transportEnv.TELEGRAM_BOT_TOKEN || !transportEnv.TELEGRAM_CHAT_ID) throw new Error('运营周报配置不完整');
  const repo = context.repo;
  core.notice('运营周报阶段：读取 GitHub 数据');
  const branch = (await github.rest.repos.getBranch({...repo, branch: MAIN})).data;
  const sha = branch.commit.sha;
  const root = `https://github.com/${repo.owner}/${repo.repo}`;
  const reportURL = `${root}/actions/runs/${context.runId}`;
  const date = new Intl.DateTimeFormat('sv-SE', {timeZone: 'Asia/Shanghai', dateStyle: 'short'}).format(now);
  const features = [];
  for (const [name, path, marker] of FEATURES) {
    let present = false;
    try {
      const {data} = await github.rest.repos.getContent({...repo, path, ref: sha});
      if (data.type !== 'file' || data.encoding !== 'base64' || typeof data.content !== 'string') throw new Error('源码证据格式不受支持');
      present = Buffer.from(data.content, 'base64').toString('utf8').includes(marker);
    } catch (error) {
      if (error.status !== 404) throw error;
    }
    features.push(`${name}：代码进度${present ? '已发现入口' : '暂无可验证数据（未确认已知入口）'}；专项测试：暂无可验证数据（未核验）；真实运营条件：暂无可验证数据（未核验）`);
  }
  // Bounded reads, not a crawl: at most 100 recently updated open issues/PRs/checks.
  const items = (await github.rest.issues.listForRepo({...repo, state: 'open', sort: 'updated', direction: 'desc', per_page: 100})).data;
  const related = items.filter(item => TOPICS.some(([pattern]) => pattern.test(item.title)));
  const checks = (await github.rest.checks.listForRef({...repo, ref: sha, per_page: 100})).data.check_runs;
  const latest = new Map();
  for (const check of checks) {
    const key = `${check.app?.id}:${check.name}`;
    if (!latest.has(key) || latest.get(key).id < check.id) latest.set(key, check);
  }
  const statuses = {success: 0, failure: 0, skipped: 0, cancelled: 0, pending: 0, unknown: 0};
  for (const check of latest.values()) {
    if (check.status !== 'completed') statuses.pending++;
    else if (['failure', 'timed_out', 'action_required', 'startup_failure'].includes(check.conclusion)) statuses.failure++;
    else if (Object.hasOwn(statuses, check.conclusion)) statuses[check.conclusion]++;
    else statuses.unknown++;
  }
  core.notice('运营周报阶段：生成报告');
  // The source API is read-only; issue bodies and application/customer data are never used.
  // Use Chinese topic labels rather than echoing unverified foreign-language titles.
  const tasks = related.slice(0, 5).map(item => {
    const topics = TOPICS.filter(([pattern]) => pattern.test(item.title)).map(([, label]) => label).join('、');
    return `${item.pull_request ? 'PR' : '任务'} #${item.number}：${topics}相关事项，具体内容与状态请核对来源。\n${root}/${item.pull_request ? 'pull' : 'issues'}/${item.number}`;
  });
  const priorities = [
    '1. 核实注册开关与封闭客户准入，登记验收证据及负责人。',
    '2. 用合成账户验收充值→额度→支付沙箱→对账/退款边界，不启用真实资金。',
    '3. 人工核对竞品官方页面，保存模型、计价单位、币种、采集日期和来源网址，审核后再评估运营定价。',
  ];
  const lines = [
    '《算介 API｜运营主管每周报告》', '',
    '一、本周运营概况', `报告日期：${date}（北京时间）`, `主线：${MAIN} @ ${sha}`,
    `最近更新的100项开放事项中，筛选到${related.length}项运营相关事项，最多展示5项。`,
    '本报告基于源码入口和 GitHub 元数据，不代表实际经营数据或上线验收结论。', '',
    '二、AI 模型与竞品动态',
    '暂无可验证数据。本版未自动采集模型价格、可用性或涨跌信息，不进行价格比较或币种换算。',
    ...SOURCES.map(([name]) => `${name}：暂无可验证数据，公开来源入口见第六部分。`), '',
    '三、商业化 V1 开发进展', ...features,
    '已有代码入口、已完成专项测试、已具备真实运营条件是三个不同阶段，不能互相替代。', '',
    '四、主要风险与待解决事项',
    ...(tasks.length ? tasks : ['暂无可验证数据：未发现标题命中的开放任务，不代表没有运营待办。']),
    `当前 SHA 检查：成功${statuses.success} / 失败${statuses.failure} / 跳过${statuses.skipped} / 取消${statuses.cancelled} / 进行中${statuses.pending} / 未知${statuses.unknown}`,
    ...(!checks.length ? ['暂无可验证数据：当前 SHA 未查到检查运行，不计测试通过。'] : []),
    '事项按中英文标题关键词筛选，仅以中文归类，原始标题和详情见来源链接。',
    '检查仅取最新100条范围，不含全部历史或传统提交状态；通用 CI 成功不等于功能专项已验收。',
    '客户支付对账、商户和合规配置、回调验签与重放防护、退款对账及生产开关仍待专项验收。', '',
    '五、下周三项重点工作', ...priorities, '',
    '六、数据来源与更新时间',
    `源码及检查记录对应主线 SHA：${sha}；报告生成日期：${date}（北京时间），不是竞品采集日期。`,
    ...SOURCES.map(([name, url]) => `${name}：${url}\n采集日期：未采集；币种：未核验。暂无可验证数据，仅提供待核验的公开来源入口。`),
    '后续低成本方案：每周人工留存官方页面快照、模型、计价单位、币种和采集日期；不假设稳定的公开价格 API，不接入爬虫或付费搜索。',
    `完整报告与代码证据：${reportURL}`, `源码：${root}/tree/${sha}`,
    '结论：本报告仅提供运营建议，尚不能据此认定已具备真实运营条件；不自动修改模型定价、合并、部署、充值、退款或生产数据。',
  ];
  await core.summary.addRaw(lines.join('\n')).write();
  core.notice('运营周报阶段：发送 Telegram 通知');
  const result = await notify(lines.join('\n'), transportEnv, fetcher);
  if (result !== 'sent') throw new Error('运营周报送达尚未确认');
  core.notice('运营周报发送成功（Telegram OPS: sent）');
}
module.exports = {runOps};
