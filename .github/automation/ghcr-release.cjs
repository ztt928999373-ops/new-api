// D2: fixed repository/registry, no upstream code or release credentials in logs.
const REPO = {owner: 'ztt928999373-ops', repo: 'new-api'};
const IMAGE = 'ghcr.io/ztt928999373-ops/suanjie-api';
const SOURCE = 'https://github.com/ztt928999373-ops/new-api';
const ENVIRONMENT = 'd2-ghcr-release';
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
  if (!review?.reviewers?.some(r => (r.type === 'Team' || (r.type === 'User' && r.reviewer?.type === 'User')) && Number.isSafeInteger(r.reviewer?.id) && r.reviewer.id > 0)) throw Error('D2_HUMAN_REVIEW_NOT_CONFIGURED');
  const runs = await github.rest.actions.listWorkflowRuns({...REPO, workflow_id: 'v1-security.yml',
    branch: 'product/v1', event: 'push', per_page: 100, request: {timeout: 15000}});
  const latest = runs.data.workflow_runs.filter(r => r.head_sha === sha && r.head_branch === 'product/v1' && r.event === 'push')
    .sort((a, b) => b.id - a.id)[0];
  if (!latest || latest.status !== 'completed' || latest.conclusion !== 'success') throw Error('D2_SECURITY_CI_NOT_SUCCESS');
  return {sha, image: imageFor(sha), securityRun: latest.id};
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

async function assertTagAbsent({sha, actor, token, fetcher = fetch}) {
  const image = imageFor(sha);
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
  const head = await fetcher(`https://ghcr.io/v2/ztt928999373-ops/suanjie-api/manifests/${image.split(':')[1]}`, {
    method: 'HEAD', redirect: 'error', signal: AbortSignal.timeout(15000),
    headers: {Authorization: `Bearer ${bearer}`, Accept: 'application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json'},
  });
  if (head.status === 200) throw Error('D2_TAG_ALREADY_EXISTS');
  if (head.status !== 404) throw Error('D2_REGISTRY_ACCESS_FAILED');
}

module.exports = {releaseGate, imageFor, validateMetadata, assertTagAbsent};
