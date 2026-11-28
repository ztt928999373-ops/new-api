// Disposable fixture only, reachable solely on the internal Compose smoke network.
import { constants, publicEncrypt, randomBytes } from 'node:crypto';
import { readFile } from 'node:fs/promises';

const model = 'gpt-4o-mini'; // Existing gateway price; no pricing mutation.
const upstreamKey = 'suanjie-synthetic-only-not-a-provider-key';
const message = 'Suanjie synthetic container smoke';
const answer = 'suanjie-local-fixture-ok';
const usage = { prompt_tokens: 12, completion_tokens: 8, total_tokens: 20 };

if (process.argv[2] === 'mock') {
  let count = 0;
  Bun.serve({ hostname: '0.0.0.0', port: 8080, maxRequestBodySize: 65536, async fetch(req) {
    const path = new URL(req.url).pathname;
    if (path === '/health' && req.method === 'GET') return Response.json({ count });
    if (path !== '/v1/chat/completions' || req.method !== 'POST') return new Response(null, { status: 404 });
    if (req.headers.get('authorization') !== `Bearer ${upstreamKey}`) return new Response(null, { status: 401 });
    const body = await req.json().catch(() => null);
    if (body?.model !== model || body.stream === true || body.messages?.[0]?.content !== message) {
      return new Response(null, { status: 400 });
    }
    count++;
    return Response.json({ id: 'chatcmpl-suanjie-smoke', object: 'chat.completion', created: 1700000000, model, usage,
      choices: [{ index: 0, finish_reason: 'stop', message: { role: 'assistant', content: answer } }] });
  } });
} else if (process.argv[2] === 'initialize-root') {
  try {
    console.log(JSON.stringify(await initializeRoot()));
  } catch {
    console.error('FAIL: synthetic Root initialization; response bodies and raw errors are omitted.');
    process.exitCode = 1;
  }
} else if (process.argv[2] === 'login-existing') {
  try {
    const credentials = JSON.parse(await readFile(process.argv[3], 'utf8'));
    await verifyExistingRoot(credentials);
    console.log('PASS: restored Root login; registration and top-up remain closed');
  } catch {
    console.error('FAIL: restored Root login or private access flags; credentials and response bodies are omitted.');
    process.exitCode = 1;
  }
} else if (process.argv[2] === 'verify') {
  let stage = 'status';
  try {
    await verify(value => { stage = value; });
    console.log('PASS: status, fresh PostgreSQL setup, encrypted Root login, duplicate setup rejection, registration/payment closed, API key and one local model response');
  } catch {
    console.error(`FAIL: ${stage}; credentials, response bodies and raw errors are omitted.`);
    process.exitCode = 1;
  }
} else {
  process.exitCode = 2;
}

function requireCondition(condition) {
  if (!condition) throw new Error('Smoke contract failed.');
}

async function request(path, { body, bearer, accepted = [200] } = {}) {
  const res = await fetch(`http://app:3000${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { 'Content-Type': 'application/json', ...(bearer ? { Authorization: `Bearer ${bearer}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
    redirect: 'error', signal: AbortSignal.timeout(15000),
  });
  requireCondition(accepted.includes(res.status));
  return res.json();
}

async function loginRoot(username, password) {
  const { data: key, success } = await request('/api/user/login/encryption-key');
  requireCondition(success === true && key?.enabled === true && typeof key.kid === 'string');
  const encrypted = publicEncrypt({ key: key.public_key, oaepHash: 'sha256', padding: constants.RSA_PKCS1_OAEP_PADDING }, Buffer.from(password));
  const login = await request('/api/user/login', { body: { username, password_encrypted: encrypted.toString('base64'), encryption_key_id: key.kid } });
  requireCondition(login.success === true && typeof login.data?.access_token === 'string' && login.data.access_token.length > 0);
  const bearer = login.data.access_token;
  const who = await request('/api/user/self', { bearer });
  requireCondition(who.success === true && who.data?.username === username && who.data?.role === 100);
  return bearer;
}

async function initializeRoot() {
  const health = await request('/api/status');
  const initial = await request('/api/setup');
  requireCondition(health.success === true && health.data.register_enabled === false &&
    initial.success === true && initial.data.status === false && initial.data.root_init === false && initial.data.database_type === 'postgres');
  const username = 'persist-root';
  const password = randomBytes(18).toString('hex');
  const setup = { username, password, confirmPassword: password, SelfUseModeEnabled: false, DemoSiteEnabled: false };
  requireCondition((await request('/api/setup', { body: setup })).success === true);
  requireCondition((await request('/api/setup', { body: setup })).success === false);
  await loginRoot(username, password);
  return { username, password };
}

async function verifyExistingRoot(credentials) {
  requireCondition(typeof credentials?.username === 'string' && typeof credentials?.password === 'string');
  const bearer = await loginRoot(credentials.username, credentials.password);
  const health = await request('/api/status');
  requireCondition(health.success === true && health.data.register_enabled === false);
  const registration = await request('/api/user/register', { body: { username: 'restore-denied', password: 'synthetic-invalid-registration-password', email: 'restore-denied@example.invalid' } });
  requireCondition(registration.success === false);
  const topup = await request('/api/user/self/topup/info', { bearer });
  requireCondition(topup.success === true && topup.data?.payment_compliance_confirmed === false &&
    ['online', 'stripe', 'creem', 'waffo', 'waffo_pancake'].every(name => topup.data[`enable_${name}_topup`] === false));
}

async function mockCount() {
  const res = await fetch('http://127.0.0.1:8080/health', { redirect: 'error', signal: AbortSignal.timeout(5000) });
  requireCondition(res.ok);
  return (await res.json()).count;
}

async function verify(setStage) {
  const health = await request('/api/status');
  requireCondition(health.success === true && health.data.register_enabled === false);
  const initial = await request('/api/setup');
  requireCondition(initial.success === true && initial.data.status === false && initial.data.root_init === false);
  requireCondition(initial.data.database_type === 'postgres');
  requireCondition(await mockCount() === 0);

  setStage('fresh Root setup');
  const username = 'smoke-root';
  const password = randomBytes(18).toString('hex');
  const setup = { username, password, confirmPassword: password, SelfUseModeEnabled: false, DemoSiteEnabled: false };
  requireCondition((await request('/api/setup', { body: setup })).success === true);
  requireCondition((await request('/api/setup')).data?.status === true);
  requireCondition((await request('/api/setup', { body: setup })).success === false);

  setStage('encrypted Root login');
  const { data: key, success } = await request('/api/user/login/encryption-key');
  requireCondition(success === true && key?.enabled === true && typeof key.kid === 'string');
  const encrypted = publicEncrypt({ key: key.public_key, oaepHash: 'sha256', padding: constants.RSA_PKCS1_OAEP_PADDING }, Buffer.from(password));
  const login = await request('/api/user/login', { body: { username, password_encrypted: encrypted.toString('base64'), encryption_key_id: key.kid } });
  requireCondition(login.success === true && typeof login.data?.access_token === 'string' && login.data.access_token.length > 0);
  const bearer = login.data.access_token;
  const who = await request('/api/user/self', { bearer });
  requireCondition(who.success === true && who.data?.username === username && who.data?.role === 100);

  setStage('registration and online top-up closed');
  const registration = await request('/api/user/register', { body: { username: 'smoke-denied', password, email: 'smoke-denied@example.invalid' } });
  requireCondition(registration.success === false && (await request('/api/status')).data?.register_enabled === false);
  const topup = await request('/api/user/self/topup/info', { bearer });
  requireCondition(topup.success === true && topup.data?.payment_compliance_confirmed === false &&
    ['online', 'stripe', 'creem', 'waffo', 'waffo_pancake'].every(name => topup.data[`enable_${name}_topup`] === false));

  setStage('synthetic channel creation');
  const channel = await request('/api/channel/', { bearer, body: { mode: 'single', channel: {
    name: 'smoke-local-only', type: 8, status: 1, group: 'default', models: model, base_url: 'http://mock:8080/v1/chat/completions', key: upstreamKey,
  } } });
  requireCondition(channel.success === true);

  setStage('model key creation and retrieval');
  const name = 'smoke-model-key';
  requireCondition((await request('/api/token/', { bearer, body: {
    name, group: 'default', expired_time: -1, remain_quota: 100000, unlimited_quota: false, model_limits_enabled: true, model_limits: model,
  } })).success === true);
  const tokens = await request('/api/token/', { bearer });
  const matches = tokens.data?.items?.filter(item => item.name === name) ?? [];
  requireCondition(tokens.success === true && matches.length === 1 && Number.isSafeInteger(matches[0].id));
  const reveal = await request(`/api/token/${matches[0].id}/key`, { bearer, body: {} });
  requireCondition(reveal.success === true && typeof reveal.data?.key === 'string' && reveal.data.key.length > 16);
  const modelKey = reveal.data.key.startsWith('sk-') ? reveal.data.key : `sk-${reveal.data.key}`;

  setStage('single model relay to isolated fixture');
  requireCondition(await mockCount() === 0);
  const completion = await request('/v1/chat/completions', { bearer: modelKey, body: {
    model, messages: [{ role: 'user', content: message }], stream: false,
  } });
  requireCondition(completion.object === 'chat.completion' && completion.choices?.[0]?.message?.content === answer &&
    Object.entries(usage).every(([field, value]) => completion.usage?.[field] === value));
  requireCondition(await mockCount() === 1);
}
