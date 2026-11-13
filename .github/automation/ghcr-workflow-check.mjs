import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
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
console.log('YAML、手动触发、审批绑定、最小权限、无标签推送及三类同 SHA 检查生产端结构验证通过。');
