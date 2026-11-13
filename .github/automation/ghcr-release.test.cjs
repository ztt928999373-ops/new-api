const {test} = require('node:test');
const assert = require('node:assert/strict');
const {releaseGate, imageFor, validateMetadata, assertTagAbsent} = require('./ghcr-release.cjs');
const SHA = 'a'.repeat(40);
function fixture() {
  return {
    sha: SHA,
    context: {eventName: 'workflow_dispatch', ref: 'refs/heads/product/v1', sha: SHA, repo: {owner: 'ztt928999373-ops', repo: 'new-api'}},
    github: {rest: {repos: {
      getBranch: async () => ({data: {commit: {sha: SHA}}}),
      getEnvironment: async () => ({data: {protection_rules: [{type: 'required_reviewers', reviewers: [{type: 'User', reviewer: {id: 1, type: 'User'}}]}]}}),
    }, actions: {listWorkflowRuns: async () => ({data: {workflow_runs: [{id: 5, head_sha: SHA, head_branch: 'product/v1', event: 'push', status: 'completed', conclusion: 'success'}]}})}}},
  };
}
test('手动、当前产品 SHA、人工审批规则和精确 SHA 安全回归满足后才放行', async () => {
  const result = await releaseGate(fixture());
  assert.equal(result.image, `ghcr.io/ztt928999373-ops/suanjie-api:sha-${SHA}`);
  assert.equal(result.securityRun, 5);
});
test('拒绝短 SHA、latest、命令注入、非产品分支及其他仓库/事件', async () => {
  for (const sha of ['latest', SHA.slice(0, 12), `${SHA}; echo unsafe`, 'A'.repeat(40), '']) assert.throws(() => imageFor(sha));
  for (const change of [{eventName: 'pull_request'}, {ref: 'refs/heads/main'}, {sha: 'b'.repeat(40)}, {repo: {owner: 'QuantumNous', repo: 'new-api'}}]) {
    const f = fixture();Object.assign(f.context, change);await assert.rejects(releaseGate(f), /D2_UNTRUSTED_CONTEXT/);
  }
});
test('主线移动、缺少审批人或上游读取失败均失败关闭', async () => {
  for (const mode of ['moved', 'review', 'bot', 'api']) {
    const f = fixture();
    if (mode === 'moved') f.github.rest.repos.getBranch = async () => ({data: {commit: {sha: 'b'.repeat(40)}}});
    if (mode === 'review') f.github.rest.repos.getEnvironment = async () => ({data: {protection_rules: []}});
    if (mode === 'bot') f.github.rest.repos.getEnvironment = async () => ({data: {protection_rules: [{type: 'required_reviewers', reviewers: [{type: 'User', reviewer: {id: 1, type: 'Bot'}}]}]}});
    if (mode === 'api') f.github.rest.repos.getEnvironment = async () => {throw Error('synthetic failure');};
    await assert.rejects(releaseGate(f));
  }
});
test('旧 SHA 成功、跳过、失败、取消、待运行都不能代替当前安全回归成功', async () => {
  for (const conclusion of ['skipped', 'failure', 'cancelled', null]) {
    const f = fixture();
    f.github.rest.actions.listWorkflowRuns = async () => ({data: {workflow_runs: [
      {id: 9, head_sha: SHA, head_branch: 'product/v1', event: 'push', status: conclusion ? 'completed' : 'in_progress', conclusion},
      {id: 8, head_sha: SHA, head_branch: 'product/v1', event: 'push', status: 'completed', conclusion: 'success'},
      {id: 10, head_sha: 'b'.repeat(40), head_branch: 'product/v1', event: 'push', status: 'completed', conclusion: 'success'},
    ]}});
    await assert.rejects(releaseGate(f), /D2_SECURITY_CI_NOT_SUCCESS/);
  }
});
test('镜像元数据必须匹配自有源码、完整 SHA、架构、许可及原入口，不得内置秘密', () => {
  const image = {Os: 'linux', Architecture: 'amd64', Config: {Entrypoint: ['/new-api'], Env: ['PATH=/bin'], Labels: {
    'org.opencontainers.image.source': 'https://github.com/ztt928999373-ops/new-api',
    'org.opencontainers.image.revision': SHA, 'org.opencontainers.image.version': `sha-${SHA}`,
    'org.opencontainers.image.licenses': 'AGPL-3.0-only',
  }}};
  validateMetadata(image, SHA);
  for (const field of Object.keys(image.Config.Labels)) {
    const changed = structuredClone(image);changed.Config.Labels[field] = 'unexpected';
    assert.throws(() => validateMetadata(changed, SHA), /D2_METADATA_MISMATCH/);
  }
  const changed = structuredClone(image);changed.Config.Env.push('GITHUB_TOKEN=synthetic-only');
  assert.throws(() => validateMetadata(changed, SHA), /D2_SECRET_IN_IMAGE/);
  for (const change of [{Os: 'windows'}, {Architecture: 'arm64'}, {Config: {...image.Config, Entrypoint: ['/unexpected']}}]) {
    assert.throws(() => validateMetadata({...image, ...change}, SHA), /D2_METADATA_MISMATCH/);
  }
});
test('GHCR 只有已认证的明确 404 才允许新标签，不能覆盖或把访问错误当作缺失', async () => {
  for (const status of [404, 200, 401, 403, 429, 500]) {
    const calls = [];
    const fetcher = async (url, opts) => {
      calls.push({url, opts});
      assert.equal(new URL(url).hostname, 'ghcr.io');assert.equal(opts.redirect, 'error');
      return calls.length === 1 ? {ok: true, json: async () => ({token: 'synthetic-bearer'})} : {status};
    };
    const operation = assertTagAbsent({sha: SHA, actor: 'synthetic', token: 'synthetic-only', fetcher});
    if (status === 404) await operation;else await assert.rejects(operation);
    assert.equal(calls[1].opts.method, 'HEAD');assert.ok(calls[1].url.endsWith(`sha-${SHA}`));
  }
});
test('无凭据、认证失败及网络异常不得推送或泄露凭据', async () => {
  await assert.rejects(assertTagAbsent({sha: SHA, actor: '', token: ''}), /D2_REGISTRY_AUTH_MISSING/);
  await assert.rejects(assertTagAbsent({sha: SHA, actor: 'synthetic', token: 'synthetic-only', fetcher: async () => ({ok: false})}), /D2_REGISTRY_ACCESS_FAILED/);
  await assert.rejects(assertTagAbsent({sha: SHA, actor: 'synthetic', token: 'synthetic-only', fetcher: async () => {throw Error('synthetic network failure');}}));
});
