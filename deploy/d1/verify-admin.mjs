// Only the fixed loopback D1 instance. Never print passwords, cookies or tokens.
import assert from 'node:assert/strict';
import {randomBytes, publicEncrypt, constants} from 'node:crypto';

const base = 'http://127.0.0.1:5781';
async function request(path, body, access) {
  const response = await fetch(base + path, {
    method: body === undefined ? 'GET' : 'POST',
    redirect: 'error', signal: AbortSignal.timeout(15000),
    headers: {'Content-Type': 'application/json', ...(access ? {Authorization: `Bearer ${access}`} : {})},
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  assert.equal(response.status, 200, 'D1 接口状态异常');
  return response.json();
}
let stage = '初始化状态';
try {
  const password = randomBytes(24).toString('hex');
  const initial = await request('/api/setup');
  assert.equal(initial.data.status, false, '仅允许全新合成实例');
  stage = '创建合成管理员';
  const setup = await request('/api/setup', {username: 'd1-admin', password, confirmPassword: password, SelfUseModeEnabled: true, DemoSiteEnabled: false});
  assert.equal(setup.success, true);
  stage = '获取加密公钥';
  const encryption = await request('/api/user/login/encryption-key');
  assert.equal(encryption.data.enabled, true, '不绕过登录加密');
  const encrypted = publicEncrypt({key: encryption.data.public_key, oaepHash: 'sha256', padding: constants.RSA_PKCS1_OAEP_PADDING}, Buffer.from(password));
  stage = '加密登录';
  const login = await request('/api/user/login', {username: 'd1-admin', password_encrypted: encrypted.toString('base64'), encryption_key_id: encryption.data.kid});
  assert.equal(login.success, true);
  stage = '管理员自身查询';
  const self = await request('/api/user/self', undefined, login.data.access_token);
  assert.equal(self.success, true);
  assert.equal(self.data.role, 100);
  stage = '管理员健康访问';
  const health = await request('/api/status', undefined, login.data.access_token);
  assert.equal(health.success, true);
  console.log('D1 合成管理员初始化、加密登录、自身查询及健康访问通过。');
} catch {
  console.error('D1 管理员容器验收失败：' + stage + '；未输出敏感响应或错误正文。');
  process.exitCode = 1;
}
