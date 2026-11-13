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
const SOURCES = [
  ['OpenRouter', 'https://openrouter.ai/models'],
  ['AIHubMix', 'https://aihubmix.com/'],
  ['302.AI', 'https://302.ai/'],
];

async function runOps({github, context, core, env = process.env, fetcher = fetch, now = new Date()}) {
  core.notice('OPS stage: configuration');
  // Explicit mapping only: never fall back to technical supervisor variables.
  const transportEnv = {TELEGRAM_BOT_TOKEN: env.TELEGRAM_OPS_BOT_TOKEN, TELEGRAM_CHAT_ID: env.TELEGRAM_OPS_CHAT_ID};
  if (!transportEnv.TELEGRAM_BOT_TOKEN || !transportEnv.TELEGRAM_CHAT_ID) throw new Error('OPS configuration incomplete');
  const repo = context.repo;
  core.notice('OPS stage: github-read');
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
      if (data.type !== 'file' || data.encoding !== 'base64' || typeof data.content !== 'string') throw new Error('Unsupported source evidence');
      present = Buffer.from(data.content, 'base64').toString('utf8').includes(marker);
    } catch (error) {
      if (error.status !== 404) throw error;
    }
    features.push(`${name}：${present ? '已有代码入口' : '已知入口未确认'}；专项测试未核验；真实运营条件未核验`);
  }
  // Bounded reads, not a crawl: at most 100 recently updated open issues/PRs/checks.
  const items = (await github.rest.issues.listForRepo({...repo, state: 'open', sort: 'updated', direction: 'desc', per_page: 100})).data;
  const related = items.filter(item => /运营|商业|注册|充值|钱包|支付|对账|退款|定价|风险|register|top.?up|wallet|payment|reconcil|refund|pricing|billing|launch|operation/i.test(item.title));
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
  core.notice('OPS stage: report-generation');
  // The source API is read-only; issue bodies and application/customer data are never used.
  const tasks = related.slice(0, 5).map(item => `#${item.number} ${item.title.replace(/[\r\n\x00-\x1f]/g, ' ').slice(0, 60)}\n${root}/${item.pull_request ? 'pull' : 'issues'}/${item.number}`);
  const priorities = [
    '1. 核实注册开关与封闭客户准入，登记验收证据及负责人。',
    '2. 用合成账户验收充值→额度→支付沙箱→对账/退款边界，不启用真实资金。',
    '3. 人工核对竞品官方页面，保存模型/计价单位/币种/采集日期/URL，审核后再评估运营定价。',
  ];
  const lines = [
    '算介运营主管 V1 周报', `报告日期：${date}（北京时间）`, `主线：${MAIN} @ ${sha}`, '',
    '商业化进度（入口存在不等于功能完整）', ...features,
    '客户支付对账、商户/合规配置、回调验签/重放、退款对账与生产开关仍待专项验收。', '',
    '本周建议优先完成的三个运营事项', ...priorities, '',
    'GitHub 运营任务及风险（最近更新100项、标题匹配、最多展示5项）',
    ...(tasks.length ? tasks : ['未发现标题命中的开放任务；不代表没有运营待办。']),
    `当前SHA检查：成功${statuses.success} / 失败${statuses.failure} / 跳过${statuses.skipped} / 取消${statuses.cancelled} / 进行中${statuses.pending} / 未知${statuses.unknown}`,
    ...(!checks.length ? ['当前SHA未查到检查运行，不计测试通过。'] : []),
    '检查仅取最新100条范围，不含全部历史/传统status；通用CI成功不等于上述功能专项已验收。', '',
    '竞品监测：暂无可验证更新（本版未自动采集，不推断价格或涨跌）',
    ...SOURCES.map(([name, url]) => `${name}：${url}\n采集日期：未采集；币种：未核验（仅为待核验公开来源入口）`),
    '低成本接入：每周人工留存官方页面快照及日期、币种、单位；无稳定公开价格API假设，不接爬虫或付费搜索。', '',
    `完整报告与代码证据：${reportURL}`, `源码：${root}/tree/${sha}`,
    '仅运营建议；不自动修改模型定价、合并、部署、充值、退款或生产数据。',
  ];
  await core.summary.addRaw(lines.join('\n')).write();
  core.notice('OPS stage: telegram-send');
  const result = await notify(lines.join('\n'), transportEnv, fetcher);
  if (result !== 'sent') throw new Error('OPS delivery not confirmed');
  core.notice('Telegram OPS: sent');
}
module.exports = {runOps};
