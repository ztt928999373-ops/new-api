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

async function registryBearer({actor, token, fetcher, readOnly = false}) {
  const anonymous = readOnly && !actor && !token;
  if (!anonymous && (!actor || !token)) throw Error('D2_REGISTRY_AUTH_MISSING');
  // Auth scope and endpoints are fixed; do not infer absence from access errors.
  const response = await fetcher('https://ghcr.io/token?service=ghcr.io&scope=repository%3Aztt928999373-ops%2Fsuanjie-api%3A' + (readOnly ? 'pull' : 'pull%2Cpush'), {
    redirect: 'error', signal: AbortSignal.timeout(15000),
    headers: anonymous ? {} : {Authorization: `Basic ${Buffer.from(`${actor}:${token}`).toString('base64')}`},
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
  try {
    imageFor(sha);
    if (![digest, configDigest].every(d => /^sha256:[a-f0-9]{64}$/.test(d || ''))) throw Error('D2_INVALID_DIGEST');
    let bearer;
    try { bearer = await registryBearer({actor, token, fetcher, readOnly: true}); }
    catch { throw Error('D2_REGISTRY_AUTH_FAILED'); }
    async function read(path) {
      // One deadline covers the Registry request, optional storage hop and body.
      const signal = AbortSignal.timeout(15000);
      const safeCodes = new Set(['D2_MANIFEST_REDIRECT_REJECTED', 'D2_BLOB_REDIRECT_REJECTED',
        'D2_BLOB_REDIRECT_LIMIT', 'D2_REGISTRY_BODY_LIMIT']);
      try {
        signal.throwIfAborted();
        let response = await fetcher(`https://ghcr.io/v2/ztt928999373-ops/suanjie-api/${path}`, {
          redirect: 'manual', signal,
          headers: {Authorization: `Bearer ${bearer}`, Accept: 'application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'},
        });
        if (response.status >= 300 && response.status < 400) {
          if (!path.startsWith('blobs/')) throw Error('D2_MANIFEST_REDIRECT_REJECTED');
          let target;
          try { target = new URL(response.headers.get('location')); }
          catch { throw Error('D2_BLOB_REDIRECT_REJECTED'); }
          if (response.status !== 307 || target.protocol !== 'https:' ||
              target.hostname !== 'pkg-containers.githubusercontent.com' || target.port ||
              target.username || target.password || target.hash) throw Error('D2_BLOB_REDIRECT_REJECTED');
          await response.body?.cancel();
          signal.throwIfAborted();
          // Fresh options: never forward Registry credentials or caller headers.
          response = await fetcher(target.href, {
            redirect: 'manual', signal, credentials: 'omit', headers: {Accept: 'application/octet-stream'},
          });
          if (response.status >= 300 && response.status < 400) throw Error('D2_BLOB_REDIRECT_LIMIT');
        }
        if (!response.ok) {
          const code = `D2_REGISTRY_HTTP_${response.status}`;
          if (/^D2_REGISTRY_HTTP_[1-5][0-9]{2}$/.test(code)) safeCodes.add(code);
          throw Error(code);
        }
        const chunks = []; let size = 0;
        for await (const chunk of response.body) {
          signal.throwIfAborted();
          size += chunk.length;
          if (size > 1024 * 1024) throw Error('D2_REGISTRY_BODY_LIMIT');
          chunks.push(Buffer.from(chunk));
        }
        signal.throwIfAborted();
        return Buffer.concat(chunks);
      } catch (error) {
        // No cause/response/URL: signed storage parameters and credentials stay private.
        if (signal.aborted) throw Error('D2_REGISTRY_TIMEOUT');
        throw Error(safeCodes.has(error?.message) ? error.message : 'D2_REGISTRY_READ_FAILED');
      }
    }
    const bytes = await read(`manifests/${digest}`);
    if (`sha256:${createHash('sha256').update(bytes).digest('hex')}` !== digest) throw Error('D2_MANIFEST_HASH_MISMATCH');
    let manifest;
    try { manifest = JSON.parse(bytes.toString('utf8')); }
    catch { throw Error('D2_MANIFEST_JSON_INVALID'); }
    if (!manifest || manifest.schemaVersion !== 2 || manifest.config?.digest !== configDigest) throw Error('D2_CONFIG_MISMATCH');
    const config = await read(`blobs/${configDigest}`);
    if (`sha256:${createHash('sha256').update(config).digest('hex')}` !== configDigest) throw Error('D2_CONFIG_HASH_MISMATCH');
    let parsed;
    try { parsed = JSON.parse(config.toString('utf8')); }
    catch { throw Error('D2_CONFIG_JSON_INVALID'); }
    try { validateMetadata({Os: parsed?.os, Architecture: parsed?.architecture, Config: parsed?.config}, sha); }
    catch (error) { throw Error(error?.message === 'D2_SECRET_IN_IMAGE' ? 'D2_SECRET_IN_IMAGE' : 'D2_METADATA_MISMATCH'); }
    return `${IMAGE}@${digest}`;
  } catch (error) {
    const code = /^D2_[A-Z0-9_]{1,64}$/.test(error?.message || '') ? error.message : 'D2_REGISTRY_VERIFY_FAILED';
    // The workflow intentionally swallows raw errors; emit only this safe code.
    console.error(code);
    throw Error(code);
  }
}

function publicationState({attempted, pushSucceeded, verified}) {
  if (!attempted) return '未执行推送';
  if (verified) return 'Registry 已验证发布';
  return pushSucceeded ? '推送命令成功但 Digest 未验证' : '推送状态未知';
}

module.exports = {releaseGate, imageFor, validateMetadata, assertTagAbsent, verifyRemoteDigest, publicationState};
