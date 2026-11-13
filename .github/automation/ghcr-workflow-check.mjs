import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createRequire} from 'node:module';
const load = name => Bun.YAML.parse(readFileSync(`.github/workflows/${name}`, 'utf8'));
const workflow = load('v1-d2-ghcr.yml');
assert.deepEqual(Object.keys(workflow.on), ['workflow_dispatch']);
assert.equal(workflow.jobs.publish.environment.name, 'd2-ghcr-release');
assert.equal(workflow.jobs.publish.needs, 'qualification');
assert.equal(workflow.jobs.publish.permissions.packages, 'write');
assert.equal(workflow.jobs.qualification.permissions.packages, undefined);
assert.equal(workflow.permissions.packages, undefined);
const steps = workflow.jobs.publish.steps;
const push = steps.find(step => step.id === 'push');
assert.match(push.run, /push-by-digest=true/);
assert.doesNotMatch(push.run, /docker push|--tag|--tags| -t /);
assert.match(push.run, /name=ghcr\.io\/ztt928999373-ops\/suanjie-api,/);
assert.equal(steps.find(step => step.name?.includes('始终记录')).if, 'always()');
assert.match(steps.find(step => step.name?.includes('始终记录')).with.script, /core\.setFailed/);
for (const name of ['ci.yml', 'v1-d2-check.yml', 'v1-security.yml']) {
  assert.deepEqual(load(name).on.push.branches, ['product/v1']);
}
assert.equal(load('v1-d2-check.yml').permissions.packages, undefined);
// Execute the actual final-report script without credentials, filesystem state
// or Registry access. This catches step-level failure swallowing, not just the
// publicationState helper's classification.
const requireLocal = createRequire(import.meta.url);
const release = requireLocal('./ghcr-release.cjs');
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
const report = new AsyncFunction('require', 'process', 'core', 'context', steps.find(step => step.name?.includes('始终记录')).with.script);
for (const [attempted, outcome, state, failed] of [
  [false, 'skipped', '未执行推送', false],
  [false, 'failure', '未执行推送', true],
  [true, 'success', '推送命令成功但 Digest 未验证', true],
  [true, 'failure', '推送状态未知', true],
  [true, 'cancelled', '推送状态未知', true],
]) {
  let summary = '', didFail = false;
  const mockRequire = name => name === 'node:fs'
    ? {existsSync: () => attempted, readFileSync: () => {throw Error('synthetic missing digest');}}
    : release;
  await report(mockRequire, {env: {RELEASE_SHA: 'a'.repeat(40), PUSH_OUTCOME: outcome}}, {
    summary: {addRaw(s) {summary += s;return this;}, async write() {}},
    setFailed() {didFail = true;},
  }, {actor: 'synthetic'});
  assert.ok(summary.includes(state));assert.equal(didFail, failed);
}
console.log('YAML、手动触发、审批绑定、最小权限、无标签推送及三类同 SHA 检查生产端结构验证通过。');
