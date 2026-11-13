// Trusted base-branch code only. Never execute a pull request's checkout here.
const MAIN = 'product/v1';

class StageError extends Error {}

async function runStage(core, stage, operation) {
  core.notice(`AI Manager stage: ${stage}: start`);
  try {
    const result = await operation();
    core.notice(`AI Manager stage: ${stage}: success`);
    return result;
  } catch (error) {
    if (error instanceof StageError) throw error;
    const status = Number.isInteger(error?.status) && error.status >= 100 && error.status <= 599 ? ` (HTTP ${error.status})` : '';
    // Never log the original message, stack, cause, request URL or response body.
    const message = `AI Manager stage: ${stage}: failed${status}`;
    core.notice(message);
    throw new StageError(message);
  }
}

function telegramConfiguration(env) {
  if (!env.TELEGRAM_BOT_TOKEN && !env.TELEGRAM_CHAT_ID) return 'not-configured';
  if (!env.TELEGRAM_BOT_TOKEN || !env.TELEGRAM_CHAT_ID) throw new Error('Incomplete Telegram configuration');
  return 'ready';
}

async function notify(text, env = process.env, fetcher = fetch) {
  if (telegramConfiguration(env) === 'not-configured') return 'not-configured';
  // Fixed host, no redirects, no PR bodies/code/credentials in notification text.
  const response = await fetcher(`https://api.telegram.org/bot${env.TELEGRAM_BOT_TOKEN}/sendMessage`, {
    method: 'POST', redirect: 'error', signal: AbortSignal.timeout(15000),
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({chat_id: env.TELEGRAM_CHAT_ID, text: text.slice(0, 4000), disable_web_page_preview: true}),
  });
  if (!response.ok) {
    const error = new Error('Telegram HTTP failure');
    error.status = response.status;
    throw error;
  }
  const result = await response.json();
  if (!result.ok) throw new Error('Telegram rejected notification');
  return 'sent';
}

async function manage({github, context, core, env = process.env, fetcher = fetch}) {
  const configuration = await runStage(core, 'configuration', () => telegramConfiguration(env));
  core.notice(`Telegram configuration: ${configuration}`);
  const repo = context.repo;
  const pr = context.payload.pull_request;
  if (pr) {
    if (pr.base.ref !== MAIN) return;
    const latest = await runStage(core, 'github-read-pr', async () => (await github.rest.pulls.get({...repo, pull_number: pr.number})).data);
    if (latest.head.sha !== pr.head.sha) {
      core.notice('Superseded PR event; no review or notification.');
      return;
    }
    const review = context.payload.review;
    const reviewedSHA = review?.commit_id || 'unknown';
    const reviewScope = review && reviewedSHA !== latest.head.sha ? '历史提交审核，不适用于当前 head' : '当前提交审核';
    const status = review ? `审核状态：${review.state}（${reviewScope}）\n审核 SHA：${reviewedSHA}` : `PR 事件：${context.payload.action}${latest.draft ? '（Draft）' : ''}`;
    const notification = await runStage(core, 'telegram-send', () => notify(`算介虚拟技术主管\nPR #${pr.number}\n${status}\n提交：${latest.head.sha}\n${pr.html_url}\n最终审核与合并由项目所有者决定。`, env, fetcher));
    core.notice(`Telegram: ${notification}`);
    await runStage(core, 'report-generation', () => core.summary.addHeading('算介自动化状态').addRaw(`PR #${pr.number} · ${latest.head.sha}\n\n${status}\n\nTelegram: ${notification}\n\n审核由 Codex 原生 Automatic Code Review 管理；本机器人仅通知。最终审核与合并由所有者决定。`).write());
    return;
  }
  const prs = await runStage(core, 'github-read-prs', () => github.paginate(github.rest.pulls.list, {...repo, state: 'open', base: MAIN, per_page: 100}));
  const branch = await runStage(core, 'github-read-branch', async () => (await github.rest.repos.getBranch({...repo, branch: MAIN})).data);
  const {risks} = await runStage(core, 'report-generation', async () => {
    const lines = [`# 算介项目进度与风险`, `生成时间：${new Date().toISOString()}`, `主线：${MAIN} @ ${branch.commit.sha}`, '', '检查结果仅代表对应 SHA；跳过、取消、未运行均不计通过。', ''];
    let risks = 0;
    for (const p of prs) {
      // Octokit already normalizes check_runs into an array for paginate().
      const runs = await runStage(core, 'github-read-checks', () => github.paginate(github.rest.checks.listForRef, {...repo, ref: p.head.sha, per_page: 100}));
      // Keep the latest run for each app/name; old failures remain accessible in GitHub.
      const latest = new Map();
      for (const r of runs) {
        const key = `${r.app?.id}:${r.name}`;
        if (!latest.has(key) || latest.get(key).id < r.id) latest.set(key, r);
      }
      const checks = [...latest.values()];
      const passed = checks.filter(r => r.status === 'completed' && r.conclusion === 'success').length;
      const failed = checks.filter(r => ['failure', 'timed_out', 'action_required', 'startup_failure'].includes(r.conclusion)).length;
      const unknown = checks.length - passed - failed;
      const reviews = await runStage(core, 'github-read-reviews', () => github.paginate(github.rest.pulls.listReviews, {...repo, pull_number: p.number, per_page: 100}));
      // A later COMMENTED review does not withdraw an earlier blocking decision.
      const decisions = new Map();
      for (const r of [...reviews].sort((a, b) => a.id - b.id)) {
        if (['APPROVED', 'CHANGES_REQUESTED', 'DISMISSED'].includes(r.state)) decisions.set(r.user?.login || `unknown-${r.id}`, r);
      }
      const currentReviews = reviews.filter(r => r.commit_id === p.head.sha && r.state !== 'DISMISSED');
      const blocking = [...decisions.values()].filter(r => r.state === 'CHANGES_REQUESTED');
      const approved = [...decisions.values()].filter(r => r.state === 'APPROVED' && r.commit_id === p.head.sha);
      if (failed || unknown || !checks.length || !approved.length || blocking.length) risks++;
      lines.push(`## PR #${p.number}${p.draft ? ' [Draft]' : ''}`, `- URL: ${p.html_url}`, `- SHA: ${p.head.sha}`, `- Checks: ${passed} success / ${failed} failure / ${unknown} pending-or-other${!checks.length ? ' / NO CHECKS' : ''}`, `- 当前 SHA 审核记录：${currentReviews.length}（记录数量不代表批准）；当前有效批准：${approved.length}；未撤回请求修改：${blocking.length}`, ...checks.map(r => `  - ${r.name}: ${r.status}/${r.conclusion || 'pending'} — ${r.html_url}`), '');
    }
    lines.push(`待关注 PR：${risks}/${prs.length}`, '', '商业化风险：代码具备注册/钱包/支付入口不等于可商用；真实配置、回调、对账、退款和三数据库资金不变量仍需独立验收。', '不自动合并、不部署、不更改注册或支付开关。');
    await core.summary.addRaw(lines.join('\n')).write();
    return {risks};
  });
  const result = await runStage(core, 'telegram-send', () => notify(`算介虚拟技术主管日报\n主线：${MAIN}\nSHA：${branch.commit.sha}\n开放 PR：${prs.length}；待关注：${risks}\n报告：https://github.com/${repo.owner}/${repo.repo}/actions/runs/${context.runId}\n未运行和跳过不计通过；最终审核与合并由所有者决定。`, env, fetcher));
  core.notice(`Telegram: ${result}`);
}
module.exports = {manage, notify};
