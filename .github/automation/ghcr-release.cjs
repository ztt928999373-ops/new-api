// D2: fixed repository/registry, no upstream code or release credentials in logs.
const REPO = {owner: 'ztt928999373-ops', repo: 'new-api'};
const IMAGE = 'ghcr.io/ztt928999373-ops/suanjie-api';
const SOURCE = 'https://github.com/ztt928999373-ops/new-api';
const ENVIRONMENT = 'd2-ghcr-release';
const {createHash} = require('node:crypto');
const REQUIRED = {
  'v1-security.yml': ['V1 security regression (3 databases)'],
  'ci.yml': ['Backend vet, build, and test', 'Frontend typecheck and test'],
  'v1-d2-check.yml': ['test'],
};
function imageFor(sha) {
  if (!/^[a-f0-9]{40}$/.test(sha || '')) throw Error('D2_INVALID_SHA');
  return `${IMAGE}:sha-${sha}`;
}

async function releaseGate({github, context, sha}) {
  imageFor(sha);
  if (context.eventName !== 'workflow_dispatch' || context.ref !== 'refs/heads/product/v1' ||
      context.repo.owner !== REPO.owner || context.repo.repo !== REPO.repo || context.sha !== sha) throw Error('D2_UNTRUSTED_CONTEXT');
  const branch = await github.rest.repos.getBranch({...REPO, branch: 'product/v1', request: {timeout: 15000}});
  if (branch.data.commit.sha !== sha) throw Error('D2_NOT_CURRENT_PRODUCT_SHA');
  const environment = await github.rest.repos.getEnvironment({...REPO, environment_name: ENVIRONMENT, request: {timeout: 15000}});
  const review = environment.data.protection_rules?.find(rule => rule.type === 'required_reviewers');
  if (environment.data.can_admins_bypass !== false) throw Error('D2_ADMIN_BYPASS_ENABLED');
  if (!review?.reviewers?.some(r => (r.type === 'Team' || (r.type === 'User' && r.reviewer?.type === 'User')) && Number.isSafeInteger(r.reviewer?.id) && r.reviewer.id > 0)) throw Error('D2_HUMAN_REVIEW_NOT_CONFIGURED');
  const checks = {};
  for (const [workflow, requiredJobs] of Object.entries(REQUIRED)) {
    const runs = await github.rest.actions.listWorkflowRuns({...REPO, workflow_id: workflow,
      branch: 'product/v1', event: 'push', head_sha: sha, per_page: 100, request: {timeout: 15000}});
    const latest = runs.data.workflow_runs.filter(r => r.head_sha === sha && r.head_branch === 'product/v1' && r.event === 'push')
      .sort((a, b) => b.id - a.id)[0];
    if (!latest || latest.status !== 'completed' || latest.conclusion !== 'success') throw Error('D2_CI_NOT_SUCCESS');
    const {data} = await github.rest.actions.listJobsForWorkflowRun({...REPO, run_id: latest.id, filter: 'latest', per_page: 100, request: {timeout: 15000}});
    if (!Array.isArray(data.jobs) || data.total_count !== data.jobs.length ||
        data.jobs.some(j => j.head_sha !== sha || j.status !== 'completed' || j.conclusion !== 'success') ||
        requiredJobs.some(name => data.jobs.filter(j => j.name === name).length !== 1)) throw Error('D2_CI_JOB_NOT_SUCCESS');
    checks[workflow] = latest.id;
  }
  return {sha, image: imageFor(sha), checks};
}

function validateMetadata(image, sha) {
  imageFor(sha);
  const labels = image?.Config?.Labels;
  if (image?.Os !== 'linux' || image?.Architecture !== 'amd64' ||
      labels?.['org.opencontainers.image.revision'] !== sha ||
      labels?.['org.opencontainers.image.source'] !== SOURCE ||
      labels?.['org.opencontainers.image.version'] !== `sha-${sha}` ||
      labels?.['org.opencontainers.image.licenses'] !== 'AGPL-3.0-only' ||
      JSON.stringify(image?.Config?.Entrypoint) !== '["/new-api"]') throw Error('D2_METADATA_MISMATCH');
  if ((image.Config.Env || []).some(v => /^(SESSION_SECRET|CRYPTO_SECRET|GITHUB_TOKEN|.*PASSWORD|.*API_KEY|.*BOT_TOKEN)=/i.test(v))) throw Error('D2_SECRET_IN_IMAGE');
}

async function registryBearer({actor, token, fetcher}) {
  if (!actor || !token) throw Error('D2_REGISTRY_AUTH_MISSING');
  // Auth scope and endpoints are fixed; do not infer absence from access errors.
  const response = await fetcher('https://ghcr.io/token?service=ghcr.io&scope=repository%3Aztt928999373-ops%2Fsuanjie-api%3Apull%2Cpush', {
    redirect: 'error', signal: AbortSignal.timeout(15000),
    headers: {Authorization: `Basic ${Buffer.from(`${actor}:${token}`).toString('base64')}`},
  });
  if (!response.ok) throw Error('D2_REGISTRY_ACCESS_FAILED');
  const data = await response.json();
  const bearer = data.token || data.access_token;
  if (typeof bearer !== 'string' || !bearer.length) throw Error('D2_REGISTRY_ACCESS_FAILED');
  return bearer;
}

async function assertTagAbsent({sha, actor, token, fetcher = fetch}) {
  const image = imageFor(sha);
  const bearer = await registryBearer({actor, token, fetcher});
  const head = await fetcher(`https://ghcr.io/v2/ztt928999373-ops/suanjie-api/manifests/${image.split(':')[1]}`, {
    method: 'HEAD', redirect: 'error', signal: AbortSignal.timeout(15000),
    headers: {Authorization: `Bearer ${bearer}`, Accept: 'application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json'},
  });
  if (head.status === 200) throw Error('D2_TAG_ALREADY_EXISTS');
  if (head.status !== 404) throw Error('D2_REGISTRY_ACCESS_FAILED');
}

async function verifyRemoteDigest({sha, digest, configDigest, actor, token, fetcher = fetch}) {
  imageFor(sha);
  if (![digest, configDigest].every(d => /^sha256:[a-f0-9]{64}$/.test(d || ''))) throw Error('D2_INVALID_DIGEST');
  const bearer = await registryBearer({actor, token, fetcher});
  async function read(path) {
    const response = await fetcher(`https://ghcr.io/v2/ztt928999373-ops/suanjie-api/${path}`, {
      redirect: 'error', signal: AbortSignal.timeout(15000),
      headers: {Authorization: `Bearer ${bearer}`, Accept: 'application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'},
    });
    if (!response.ok) throw Error('D2_REGISTRY_VERIFY_FAILED');
    const chunks = []; let size = 0;
    for await (const chunk of response.body) {
      size += chunk.length;
      if (size > 1024 * 1024) throw Error('D2_REGISTRY_BODY_LIMIT');
      chunks.push(Buffer.from(chunk));
    }
    return Buffer.concat(chunks);
  }
  const bytes = await read(`manifests/${digest}`);
  if (`sha256:${createHash('sha256').update(bytes).digest('hex')}` !== digest) throw Error('D2_MANIFEST_HASH_MISMATCH');
  const manifest = JSON.parse(bytes.toString('utf8'));
  if (manifest.schemaVersion !== 2 || manifest.config?.digest !== configDigest) throw Error('D2_CONFIG_MISMATCH');
  const config = await read(`blobs/${configDigest}`);
  if (`sha256:${createHash('sha256').update(config).digest('hex')}` !== configDigest) throw Error('D2_CONFIG_HASH_MISMATCH');
  const parsed = JSON.parse(config.toString('utf8'));
  validateMetadata({Os: parsed.os, Architecture: parsed.architecture, Config: parsed.config}, sha);
  return `${IMAGE}@${digest}`;
}

function publicationState({attempted, pushSucceeded, verified}) {
  if (!attempted) return '未执行推送';
  if (verified) return 'Registry 已验证发布';
  return pushSucceeded ? '推送命令成功但 Digest 未验证' : '推送状态未知';
}

module.exports = {releaseGate, imageFor, validateMetadata, assertTagAbsent, verifyRemoteDigest, publicationState};
