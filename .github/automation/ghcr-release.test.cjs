const {test} = require('node:test');
const assert = require('node:assert/strict');
const {releaseGate, imageFor, validateMetadata, assertTagAbsent, verifyRemoteDigest, publicationState} = require('./ghcr-release.cjs');
const SHA = 'a'.repeat(40);
function fixture() {
  return {
    sha: SHA,
    context: {eventName: 'workflow_dispatch', ref: 'refs/heads/product/v1', sha: SHA, repo: {owner: 'ztt928999373-ops', repo: 'new-api'}},
    github: {rest: {repos: {
      getBranch: async () => ({data: {commit: {sha: SHA}}}),
      getEnvironment: async () => ({data: {can_admins_bypass: false, protection_rules: [{type: 'required_reviewers', reviewers: [{type: 'User', reviewer: {id: 1, type: 'User'}}]}]}}),
    }, actions: {
      listWorkflowRuns: async p => ({data: {workflow_runs: [{id: {'v1-security.yml':5,'ci.yml':6,'v1-d2-check.yml':7}[p.workflow_id], head_sha: SHA, head_branch: 'product/v1', event: 'push', status: 'completed', conclusion: 'success'}]}}),
      listJobsForWorkflowRun: async p => {const names={5:['V1 security regression (3 databases)'],6:['Backend vet, build, and test','Frontend typecheck and test'],7:['test']}[p.run_id];return {data:{total_count:names.length,jobs:names.map(name=>({name,head_sha:SHA,status:'completed',conclusion:'success'}))}};},
    }}},
  };
}
test('手动、当前产品 SHA、人工审批规则和精确 SHA 安全回归满足后才放行', async () => {
  const result = await releaseGate(fixture());
  assert.equal(result.image, `ghcr.io/ztt928999373-ops/suanjie-api:sha-${SHA}`);
  assert.equal(result.checks['v1-security.yml'], 5);
  assert.equal(Object.keys(result.checks).length,3);
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
    if (mode === 'review') f.github.rest.repos.getEnvironment = async () => ({data: {can_admins_bypass: false, protection_rules: []}});
    if (mode === 'bot') f.github.rest.repos.getEnvironment = async () => ({data: {can_admins_bypass: false, protection_rules: [{type: 'required_reviewers', reviewers: [{type: 'User', reviewer: {id: 1, type: 'Bot'}}]}]}});
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
    await assert.rejects(releaseGate(f), /D2_CI_NOT_SUCCESS/);
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

test('三类工作流逐一拒绝失败、跳过、取消、未运行、未知和错误 SHA', async t => {
 for (const workflow of ['v1-security.yml','ci.yml','v1-d2-check.yml']) {
  for (const state of ['failure','skipped','cancelled','pending','missing','unknown','wrong-sha']) {
   await t.test(`${workflow}/${state}`,async()=>{
    const f=fixture(), original=f.github.rest.actions.listWorkflowRuns;
    f.github.rest.actions.listWorkflowRuns=async p=>{
     const result=await original(p);if(p.workflow_id!==workflow)return result;
     if(state==='missing')return {data:{workflow_runs:[]}};
     const run=result.data.workflow_runs[0];
     if(state==='wrong-sha')run.head_sha='b'.repeat(40);
     else {run.status=state==='pending'?'in_progress':'completed';run.conclusion=state;}
     return result;
    };
    await assert.rejects(releaseGate(f),/D2_CI_NOT_SUCCESS/);
   });
  }
 }
});

test('工作流总成功但 Job 跳过、缺失、未知或分页不完整仍阻断',async t=>{
 for(const state of ['skipped','failure','cancelled','missing','wrong-sha','unknown','truncated']){
  await t.test(state,async()=>{
   const f=fixture(), original=f.github.rest.actions.listJobsForWorkflowRun;
   f.github.rest.actions.listJobsForWorkflowRun=async p=>{
    const r=await original(p);
    if(state==='missing')r.data.jobs=[];
    else if(state==='truncated')r.data.total_count++;
    else if(state==='wrong-sha')r.data.jobs[0].head_sha='b'.repeat(40);
    else r.data.jobs[0].conclusion=state;
    return r;
   };
   await assert.rejects(releaseGate(f),/D2_CI_JOB_NOT_SUCCESS/);
  });
 }
});

test('管理员可绕过保护时拒绝发布，API 异常不放行',async()=>{
 const f=fixture();f.github.rest.repos.getEnvironment=async()=>({data:{can_admins_bypass:true}});
 await assert.rejects(releaseGate(f),/D2_ADMIN_BYPASS_ENABLED/);
 const g=fixture();g.github.rest.actions.listJobsForWorkflowRun=async()=>{throw Error('synthetic api unavailable')};
 await assert.rejects(releaseGate(g));
});

function remoteFixture(mode='valid'){
 const {createHash}=require('node:crypto');
 const hash=b=>'sha256:'+createHash('sha256').update(b).digest('hex');
 let config=Buffer.from(JSON.stringify({os:'linux',architecture:'amd64',config:{Entrypoint:['/new-api'],Labels:{
  'org.opencontainers.image.source':'https://github.com/ztt928999373-ops/new-api',
  'org.opencontainers.image.revision':mode==='wrong-source'?'b'.repeat(40):SHA,
  'org.opencontainers.image.version':`sha-${SHA}`,'org.opencontainers.image.licenses':'AGPL-3.0-only',
 }}}));
 if(mode==='invalid-json')config=Buffer.from('synthetic-private:{');
 const configDigest=hash(config);
 const manifest=Buffer.from(JSON.stringify({schemaVersion:2,config:{digest:mode==='wrong-config'?'sha256:'+'0'.repeat(64):configDigest}}));
 const digest=hash(manifest);
 return {sha:SHA,digest,configDigest,actor:'synthetic',token:'synthetic-only',fetcher:async(url,opts)=>{
  assert.equal(new URL(url).hostname,'ghcr.io');assert.equal(opts.redirect,url.includes('/token?')?'error':'manual');
  if(url.includes('/token?'))return {ok:true,json:async()=>({token:'synthetic-bearer'})};
  if(mode==='missing')return new Response('',{status:404});
  const content=url.includes('/manifests/')?manifest:config;
  return new Response(mode==='tampered'?Buffer.from('tampered'):mode==='oversized'?Buffer.alloc(1024*1024+1):content);
 }};
}

test('Registry Digest、配置摘要及源码身份全量验证成功',async()=>{
 const f=remoteFixture();assert.equal(await verifyRemoteDigest(f),`ghcr.io/ztt928999373-ops/suanjie-api@${f.digest}`);
});

test('Digest 缺失、清单损坏、错误配置、错误源码、缺失或过大响应阻断确认',async t=>{
 for(const mode of ['missing','tampered','wrong-config','wrong-source','oversized']){
  await t.test(mode,()=>assert.rejects(verifyRemoteDigest(remoteFixture(mode))));
 }
 const f=remoteFixture();f.digest=undefined;await assert.rejects(verifyRemoteDigest(f),/D2_INVALID_DIGEST/);
});

test('明确区分未推送、命令成功但 Digest 失败、推送未知和 Registry 已验证',()=>{
 assert.equal(publicationState({attempted:false,pushSucceeded:false,verified:false}),'未执行推送');
 assert.equal(publicationState({attempted:true,pushSucceeded:true,verified:false}),'推送命令成功但 Digest 未验证');
 assert.equal(publicationState({attempted:true,pushSucceeded:false,verified:false}),'推送状态未知');
 assert.equal(publicationState({attempted:true,pushSucceeded:false,verified:true}),'Registry 已验证发布');
});


function redirectedFixture(location = 'https://pkg-containers.githubusercontent.com/blob?sig=synthetic-private') {
 const f = remoteFixture(), original = f.fetcher, calls = [];
 let blob;
 f.fetcher = async (url, opts) => {
  calls.push({url, opts});
  if (new URL(url).hostname === 'ghcr.io') {
   const response = await original(url, opts);
   if (url.includes('/blobs/')) {
    blob = Buffer.from(await response.arrayBuffer());
    return new Response(null, {status: 307, headers: {location}});
   }
   return response;
  }
  return new Response(blob);
 };
 return {f, calls};
}

test('Config blob 仅跟随一次官方 HTTPS 307，存储请求不含任何认证头并共享超时', async () => {
 const {f, calls} = redirectedFixture();
 assert.equal(await verifyRemoteDigest(f), `ghcr.io/ztt928999373-ops/suanjie-api@${f.digest}`);
 const registry = calls.find(c => c.url.includes('/blobs/'));
 const storage = calls.at(-1);
 assert.equal(new URL(storage.url).hostname, 'pkg-containers.githubusercontent.com');
 assert.deepEqual(storage.opts.headers, {Accept: 'application/octet-stream'});
 assert.equal(storage.opts.credentials, 'omit');
 assert.equal(storage.opts.redirect, 'manual');
 assert.equal(storage.opts.signal, registry.opts.signal);
});

test('拒绝非法 blob 跳转且安全错误不含签名参数、响应或凭据', async t => {
 for (const location of [
  'https://evil.example/blob?sig=synthetic-private',
  'https://pkg-containers.githubusercontent.com.evil.example/blob',
  'http://pkg-containers.githubusercontent.com/blob',
  'https://user:synthetic-private@pkg-containers.githubusercontent.com/blob',
  'https://pkg-containers.githubusercontent.com:8443/blob',
  '/relative?sig=synthetic-private', 'not-a-url',
 ]) {
  await t.test(location.split('?')[0], async () => {
   const {f, calls} = redirectedFixture(location);
   await assert.rejects(verifyRemoteDigest(f), {message: 'D2_BLOB_REDIRECT_REJECTED'});
   assert.equal(calls.length, 3);
  });
 }
});

test('Manifest 跳转和 Config 连续跳转均禁止，不请求第二跳', async () => {
 const m = remoteFixture(), original = m.fetcher;
 m.fetcher = async (url, opts) => url.includes('/manifests/')
  ? new Response(null, {status: 307, headers: {location: 'https://pkg-containers.githubusercontent.com/manifest'}})
  : original(url, opts);
 await assert.rejects(verifyRemoteDigest(m), {message: 'D2_MANIFEST_REDIRECT_REJECTED'});
 const {f, calls} = redirectedFixture(), wrapped = f.fetcher;
 f.fetcher = async (url, opts) => {
  const response = await wrapped(url, opts);
  return new URL(url).hostname === 'pkg-containers.githubusercontent.com'
   ? new Response(null, {status: 307, headers: {location: 'https://evil.example/private?sig=synthetic-private'}})
   : response;
 };
 await assert.rejects(verifyRemoteDigest(f), {message: 'D2_BLOB_REDIRECT_LIMIT'});
 assert.equal(calls.length, 4);
});

test('正常跳转后仍验证 Config 哈希并限制响应大小', async () => {
 for (const [content, code] of [['tampered', 'D2_CONFIG_HASH_MISMATCH'], [Buffer.alloc(1024 * 1024 + 1), 'D2_REGISTRY_BODY_LIMIT']]) {
  const {f} = redirectedFixture(), original = f.fetcher;
  f.fetcher = async (url, opts) => new URL(url).hostname === 'pkg-containers.githubusercontent.com'
   ? new Response(content) : original(url, opts);
  await assert.rejects(verifyRemoteDigest(f), {message: code});
 }
});

test('超时、网络异常和响应流异常只暴露安全错误码', async t => {
 const controller = new AbortController();
 t.mock.method(AbortSignal, 'timeout', milliseconds => {assert.equal(milliseconds, 15000); return controller.signal;});
 const {f} = redirectedFixture(), original = f.fetcher;
 f.fetcher = async (url, opts) => {
  if (new URL(url).hostname === 'pkg-containers.githubusercontent.com') {
   controller.abort();
   throw Error('synthetic-private token and signed URL');
  }
  return original(url, opts);
 };
 await assert.rejects(verifyRemoteDigest(f), {message: 'D2_REGISTRY_TIMEOUT'});
 t.mock.restoreAll();
 for (const mode of ['network', 'stream', 'status']) {
  const {f} = redirectedFixture(), original = f.fetcher;
  f.fetcher = async (url, opts) => {
   if (new URL(url).hostname !== 'pkg-containers.githubusercontent.com') return original(url, opts);
   if (mode === 'network') throw Error('https://private.example/?sig=synthetic-private');
   if (mode === 'stream') return new Response(new ReadableStream({start(c) {c.error(Error('synthetic-private'));}}));
   return new Response('synthetic-private', {status: 403});
  };
  await assert.rejects(verifyRemoteDigest(f), {message: mode === 'status' ? 'D2_REGISTRY_HTTP_403' : 'D2_REGISTRY_READ_FAILED'});
 }
});


test('错误诊断只记录安全码，不记录认证异常或已通过哈希检查的无效 JSON', async t => {
 const messages = [];
 t.mock.method(console, 'error', message => messages.push(message));
 const f = remoteFixture();
 f.fetcher = async () => {throw Error('synthetic-private token and URL');};
 await assert.rejects(verifyRemoteDigest(f), {message: 'D2_REGISTRY_AUTH_FAILED'});
 await assert.rejects(verifyRemoteDigest(remoteFixture('invalid-json')), {message: 'D2_CONFIG_JSON_INVALID'});
 assert.deepEqual(messages, ['D2_REGISTRY_AUTH_FAILED', 'D2_CONFIG_JSON_INVALID']);
});


test('公共镜像可匿名只读复核，仅申请 pull；写入前的标签门禁仍拒绝无凭据', async () => {
 const {f} = redirectedFixture(), original = f.fetcher;
 delete f.actor; delete f.token;
 f.fetcher = async (url, opts) => {
  if (url.includes('/token?')) {
   assert.equal(new URL(url).searchParams.get('scope'), 'repository:ztt928999373-ops/suanjie-api:pull');
   assert.deepEqual(opts.headers, {});
  }
  return original(url, opts);
 };
 assert.equal(await verifyRemoteDigest(f), `ghcr.io/ztt928999373-ops/suanjie-api@${f.digest}`);
 await assert.rejects(assertTagAbsent({sha: SHA, fetcher: f.fetcher}), /D2_REGISTRY_AUTH_MISSING/);
});
