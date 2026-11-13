// Read-only GitHub monitoring; never load or execute upstream code.
const {notify} = require('./manager.cjs');
const SHA = /^[a-f0-9]{40}$/;
const OFFICIAL = {owner: 'QuantumNous', repo: 'new-api'};
const categories = ['安全修复', '功能变化', '依赖升级', '数据库迁移', '其他'];

function classify(message) {
  const title = String(message || '').split('\n')[0].slice(0, 500);
  const matches = [];
  if (/security|CVE-\d|vulnerab|\bXSS\b|\bCSRF\b|安全|越权|注入/i.test(title)) matches.push('安全修复');
  if (/^feat(?:\(|:)|feature|功能/i.test(title)) matches.push('功能变化');
  if (/\bdeps\b|dependenc|\bbump\b|依赖/i.test(title)) matches.push('依赖升级');
  if (/migrat|schema|数据库迁移|表结构/i.test(title)) matches.push('数据库迁移');
  return matches.length ? matches : ['其他'];
}

async function compare(github, repo, basehead) {
  let first;
  const commits = [], seen = new Set();
  for (let page = 1; page <= 5; page++) {
    const {data} = await github.rest.repos.compareCommitsWithBasehead({...repo, basehead, per_page: 100, page, request: {timeout: 15000}});
    first ||= data;
    if (!Number.isSafeInteger(first.total_commits) || first.total_commits < 0 || first.total_commits > 500) throw Error('comparison limit');
    for (const commit of data.commits || []) {
      if (!SHA.test(commit.sha) || seen.has(commit.sha)) throw Error('invalid commit page');
      seen.add(commit.sha); commits.push(commit);
    }
    if (commits.length >= first.total_commits) break;
    if (!data.commits?.length) throw Error('incomplete comparison');
  }
  if (commits.length !== first.total_commits) throw Error('incomplete comparison');
  return {...first, commits};
}

async function runUpstream({github, context, core, env = process.env, fetcher = fetch, previous = null, save = async () => {}}) {
  if (context.repo.owner !== 'ztt928999373-ops' || context.repo.repo !== 'new-api' || context.ref !== 'refs/heads/product/v1') throw Error('untrusted context');
  let product, upstream, difference, changes;
  try {
    product = (await github.rest.repos.getBranch({...context.repo, branch: 'product/v1', request: {timeout: 15000}})).data.commit.sha;
    upstream = (await github.rest.repos.getBranch({...OFFICIAL, branch: 'main', request: {timeout: 15000}})).data.commit.sha;
    if (!SHA.test(product) || !SHA.test(upstream)) throw Error('invalid branch');
    // Always compare the current product line, even when notifications dedupe.
    difference = await compare(github, context.repo, `${product}...QuantumNous:${upstream}`);
    if (previous !== null && (previous.version !== 1 || !SHA.test(previous.notifiedHead))) throw Error('invalid checkpoint');
    changes = previous && previous.notifiedHead !== upstream
      ? await compare(github, OFFICIAL, `${previous.notifiedHead}...${upstream}`) : difference;
    if (previous && previous.notifiedHead !== upstream && !['ahead', 'identical'].includes(changes.status)) throw Error('upstream history changed');
  } catch {
    await core.summary.addRaw('## 官方更新监控：待核实\n上游或产品比较失败、超过分页上限、历史改写或检查点异常。未推进通知检查点，不得解读为无更新。请检查 GitHub 权限与访问状态。').write();
    // Fixed diagnostic: never include API error text, URL, token or body.
    core.setFailed('官方更新检查失败；详情待核实，未保存检查点。');
    return {status: 'failed'};
  }
  const duplicate = previous?.notifiedHead === upstream;
  const buckets = Object.fromEntries(categories.map(c => [c, []]));
  for (const commit of changes.commits) for (const category of classify(commit.commit?.message)) buckets[category].push(commit.sha);
  const important = categories.slice(0, 4).some(c => buckets[c].length);
  const lines = ['## New API 官方更新监控', `产品主线：product/v1 @ ${product}`, `官方主线：QuantumNous/new-api main @ ${upstream}`,
    `产品缺少官方提交：${difference.ahead_by}；产品独有提交：${difference.behind_by}。这是提交祖先差异，不能证明是否已用不同 SHA 移植修复。`,
    `比较：https://github.com/${context.repo.owner}/${context.repo.repo}/compare/${product}...QuantumNous:${upstream}`,
    previous ? `本期范围：上次成功通知 ${previous.notifiedHead} 至官方当前提交。` : '首次运行：本期范围为官方相对产品的全部未合并提交。',
    '分类仅依据提交标题关键词，可多分类；安全影响、数据库实际变化及产品适用性均待核实。未识别到关键词不等于没有安全修复。',
    duplicate ? '官方 SHA 未变：去重，不重复发送 Telegram；本次产品差异仍已重新核对。' : important ? '发现需要人工关注的更新线索，禁止自动合并。' : '未识别到重要更新线索；简短报告，内容适用性待核实。'];
  if (!duplicate) {
    for (const category of categories) {
      lines.push(`### ${category}：${buckets[category].length}（待核实）`);
      const limit = important ? 20 : 3;
      for (const sha of buckets[category].slice(0, limit)) lines.push(`- 官方提交：https://github.com/QuantumNous/new-api/commit/${sha}`);
      if (buckets[category].length > limit) lines.push('其余提交请查看上方完整比较链接。');
    }
  }
  lines.push('不修改 main/product/v1，不执行上游代码、不合并、不部署、不调用付费 AI API。');
  await core.summary.addRaw(lines.join('\n')).write();
  if (duplicate) { await save(previous); return {status: 'duplicate'}; }
  let delivery;
  try {
    delivery = await notify(`算介官方更新周报\n${important ? '有重要更新线索（待核实）' : '未识别到重要更新线索（待核实）'}\n${categories.map(c => `${c}：${buckets[c].length}`).join('；')}\n官方：${upstream}\n产品缺少官方提交：${difference.ahead_by}\n报告：https://github.com/${context.repo.owner}/${context.repo.repo}/actions/runs/${context.runId}\n仅监控，不自动合并或部署。`, env, fetcher);
  } catch {
    core.setFailed('Telegram 通知失败，未推进检查点；未记录敏感错误详情。');
    return {status: 'notification-failed'};
  }
  await core.summary.addRaw(`\nTelegram：${delivery === 'sent' ? '发送成功' : '未配置，未发送；未推进检查点'}。`).write();
  if (delivery === 'sent') await save({version: 1, notifiedHead: upstream});
  return {status: delivery};
}

module.exports = {classify, runUpstream};
