import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import test from 'node:test';
import { bodyFingerprint, classify, excluded, headings, sizeLabel, validate } from './contribution-policy.mjs';
import { readLabels, sync } from './sync-labels.mjs';

export function validPR() {
  return readFileSync(new URL('../pull_request_template.md', import.meta.url), 'utf8')
    .replace('- [ ] Agent', '- [x] Agent').replace('- [ ] bug', '- [x] bug')
    .replace('## Summary', '## Summary\nFix CI recovery after a description changes.')
    .replace('## Validation', '## Validation\nRan the controller regression tests.')
    .replace('## Screenshots / Recordings', '## Screenshots / Recordings\nOnly workflows change; there is no visible product UI. Verified with workflow tests.');
}
function issue(type, author = 'Human') {
  const fields = { bug: ['bug', 'steps', 'expected', 'version'], feat: ['feature', 'motivation'], help: ['help', 'goal', 'attempts', 'environment'] };
  return `### Author\n${author}\n\n### Type\n${type}\n\n` + fields[type].map(key => `### ${headings[key]}\nSpecific reproducible details`).join('\n\n');
}

test('PR template can be completed and enforces unique identity/type choices', () => {
  assert.deepEqual(validate(validPR(), true).errors, []);
  assert.ok(validate(validPR().replace('- [ ] Human', '- [x] Human'), true).errors.length);
  assert.ok(validate(validPR().replace('- [ ] feat', '- [x] feat'), true).errors.length);
  assert.ok(validate(validPR().replace('- [x] bug', '- [x] help'), true).errors.length);
  assert.ok(validate(readFileSync(new URL('../pull_request_template.md', import.meta.url), 'utf8'), true).errors.length);
});
test('CLI issue bodies obey all three templates; agents explain missing screenshots', () => {
  for (const type of ['bug', 'feat', 'help']) {
    assert.deepEqual(validate(issue(type), false).errors, []);
    assert.ok(validate(issue(type, 'Agent'), false).errors.length);
    assert.deepEqual(validate(issue(type, 'Agent') + '\n\n### Screenshots / Recordings\nAPI-only issue with no visible interface; request logs are attached.', false).errors, []);
  }
});
test('fenced examples and comments cannot forge headings or identity', () => {
  const body = `\`\`\`markdown\n${validPR()}\n\`\`\``;
  assert.ok(validate(body, true).errors.length);
  assert.ok(validate(`<!-- ${validPR()} -->`, true).errors.length);
  assert.ok(validate(issue('help').replace('\nHuman\n', '\n```\nHuman\n```\n'), false).errors.length);
  assert.ok(validate(validPR().replace('- [x] Agent', '~~~\n- [x] Agent\n~~~'), true).errors.length);
});
test('fenced reproduction text is allowed and duplicate headings are rejected', () => {
  assert.deepEqual(validate(issue('bug').replace('Specific reproducible details', '```sh\nmemoh start\n```'), false).errors, []);
  assert.ok(validate(validPR() + '\n## Type\n- [x] test', true).errors.some(error => error.includes('Duplicate')));
});
test('single QA checkbox accepts checked and unchecked states without a record', () => {
  assert.deepEqual(validate(validPR(), true).errors, []);
  let human = validPR().replace('- [ ] Human QA passed', '- [x] Human QA passed');
  assert.deepEqual(validate(human, true).errors, []);
  human += '\n@maintainer confirmed the happy path in the PR review.';
  assert.deepEqual(validate(human, true).errors, []);
  assert.deepEqual(validate(human.replace('[x] Human QA passed','[X] Human QA passed'), true).errors, []);
  assert.deepEqual(validate(human.replace('[x] Human QA passed','[ ] Human QA passed'), true).errors, []);
});
test('all size boundaries use the larger total, never the sum', () => {
  for (const [n, label] of [[0,'XS'],[49,'XS'],[50,'S'],[499,'S'],[500,'M'],[999,'M'],[1000,'L'],[3000,'L'],[3001,'XL']]) {
    assert.equal(sizeLabel(n, 0), `size:${label}`);
    assert.equal(sizeLabel(0, n), `size:${label}`);
  }
  assert.equal(sizeLabel(400, 400), 'size:S');
  assert.equal(sizeLabel(80, 1200), 'size:L');
});
test('generated outputs are excluded but hand-authored icons and migrations count', () => {
  const files = [
    { filename:'packages/sdk/src/sdk.gen.ts', additions:10000, deletions:4000 },
    { filename:'internal/db/postgres/sqlc/apps.sql.go', additions:4000, deletions:1000 },
    { filename:'internal/rpc/runtimepb/runtime_grpc.pb.go', additions:2000, deletions:3000 },
    { filename:'pnpm-lock.yaml', additions:12000, deletions:20000 },
    { filename:'packages/icons/src/icons/Codex.vue', additions:40, deletions:10 },
    { filename:'db/postgres/migrations/0153_example.up.sql', additions:15, deletions:0 },
  ];
  const result = classify(files);
  assert.equal(result.additions, 55);
  assert.equal(result.deletions, 10);
  assert.equal(result.size, 'size:S');
  assert.equal(result.ignored, 4);
  assert.deepEqual(result.changes, ['change:desktop','change:migrations','change:server','change:web']);
  assert.equal(excluded('packages/icons/src/icons/CodexColor.vue'), false);
  assert.equal(excluded('packages/icons/src/icons/Misskey.vue'), false);
  assert.equal(excluded('packages/icons/src/icons/Generated.vue'), true);
});
test('renames, deletions and submodule gitlinks classify both affected areas', () => {
  assert.deepEqual(classify([{ filename:'apps/desktop/src/view.ts', previous_filename:'apps/web/src/view.ts', additions:0, deletions:0 }]).changes, ['change:desktop','change:web']);
  assert.deepEqual(classify([{ filename:'packages/ui', additions:1, deletions:1 }]).changes, ['change:web']);
  assert.deepEqual(classify([{ filename:'db/postgres/migrations/old.sql', additions:0, deletions:5 }]).changes, ['change:migrations','change:server']);
  assert.equal(classify([{ filename:'packages/sdk/src/new.ts', previous_filename:'apps/web/manual.ts', additions:70, deletions:0 }]).size, 'size:S');
  assert.deepEqual(classify([{ filename:'docs/help.md', additions:30, deletions:0 }]).changes, []);
});
test('body fingerprint changes with the head, body and target branch', () => {
  const pr = { head:{sha:'a'}, base:{ref:'main'}, body:validPR() };
  for (const changed of [{...pr,body:'changed'},{...pr,head:{sha:'b'}},{...pr,base:{ref:'v1.0'}}]) assert.notEqual(bodyFingerprint(pr), bodyFingerprint(changed));
});
test('issue forms produce valid GitHub-rendered markdown after required fields are completed', () => {
  for (const file of ['bug_report','feature_request','help']) {
    const form = JSON.parse(execFileSync('ruby', ['-ryaml','-rjson','-e','puts YAML.load_file(ARGV[0]).to_json', new URL(`../ISSUE_TEMPLATE/${file}.yml`, import.meta.url).pathname], { encoding:'utf8' }));
    const body = form.body.map(field => `### ${field.attributes.label}\n\n${field.id === 'author' ? 'Human' : field.id === 'type' ? form.labels[0] : field.validations.required ? 'Specific details and actual verification results' : '_No response_'}`).join('\n\n');
    assert.deepEqual(validate(body, false).errors, []);
  }
});
test('automatic PR producers supply valid bodies', () => {
  for (const [file,job,key] of [['agents-md-updater.yml','update','pr_body']]) {
    const workflow = JSON.parse(execFileSync('ruby', ['-ryaml','-rjson','-e','puts YAML.load_file(ARGV[0]).to_json', new URL(`../workflows/${file}`, import.meta.url).pathname], { encoding:'utf8' }));
    const body = key === 'pr_body' ? workflow.jobs[job].with[key] : workflow.jobs[job].steps.find(step => step.with?.body)?.with.body;
    assert.deepEqual(validate(body, true).errors, []);
  }
});
test('label sync is idempotent and does not remove unrelated labels', async () => {
  const desired = await readLabels();
  const calls = [];
  const github = { paginate:async()=>[...desired,{name:'legacy',color:'ffffff',description:''}], rest:{issues:{listLabelsForRepo:'list',createLabel:async x=>calls.push(x),updateLabel:async x=>calls.push(x)}} };
  await sync(github, {owner:'o',repo:'r'});
  assert.deepEqual(calls, []);
});

test('ordinary PR CI has no format job or dependency', () => {
  const dir=new URL('../workflows/',import.meta.url);
  let checked=0;
  for(const file of readdirSync(dir).filter(name=>name.endsWith('.yml'))) {
    const workflow=JSON.parse(execFileSync('ruby',['-ryaml','-rjson','-e','puts YAML.load_file(ARGV[0]).to_json',new URL(file,dir).pathname],{encoding:'utf8'}));
    const events=workflow.on??workflow.true;
    if(!events || !Object.hasOwn(events,'pull_request')) continue;
    checked++;
    assert.equal(workflow.jobs.format,undefined,file);
    for(const [name,job] of Object.entries(workflow.jobs)) {
      const needs=Array.isArray(job.needs)?job.needs:[job.needs];
      assert.ok(!needs.includes('format'),`${file}: ${name} waits for format`);
      assert.ok(!job.if?.includes('needs.format'),file);
    }
    assert.ok(Object.values(workflow.permissions).every(value=>value==='read'),file);
  }
  assert.equal(checked,9);
});

test('both the read-only gate and privileged controller check out default-branch rules', () => {
  const load=file=>JSON.parse(execFileSync('ruby',['-ryaml','-rjson','-e','puts YAML.load_file(ARGV[0]).to_json',new URL(`../workflows/${file}`,import.meta.url).pathname],{encoding:'utf8'}));
  const gate=load('contribution-format.yml');
  const checkout=gate.jobs.check.steps.find(step=>step.uses?.startsWith('actions/checkout@'));
  assert.ok(checkout.with.ref.includes('default_branch'));
  assert.equal(checkout.with['persist-credentials'],false);
  assert.ok(Object.values(gate.permissions).every(permission=>permission==='read'));
  const controller=load('contribution-governance.yml');
  assert.ok(controller.jobs.govern.steps.find(step=>step.uses?.startsWith('actions/checkout@')).with.ref.includes('default_branch'));
});

test('English template structure accepts free-form responses in any language', () => {
  const multilingual = validPR()
    .replace('Fix CI recovery after a description changes.', '修复描述修改后 CI 未恢复的问题。')
    .replace('Ran the controller regression tests.', '回帰テストを実行し、成功しました。')
    .replace('Only workflows change; there is no visible product UI. Verified with workflow tests.', 'Solo cambia el flujo de CI; no hay una interfaz visible.');
  assert.deepEqual(validate(multilingual, true).errors, []);
  const report = issue('help').replaceAll('Specific reproducible details', '这是用户填写的具体求助内容。');
  assert.deepEqual(validate(report, false).errors, []);
});

test('subheadings remain part of their template section, including repeated subsection names', () => {
  for(const heading of ['##','###','####']) {
    const body=validPR()
      .replace('Ran the controller regression tests.',`${heading} 自动测试\n测试通过。\n${heading} 自动测试\n补充验证。`)
      .replace('Fix CI recovery after a description changes.',`${heading} 背景\n修复 CI。`);
    assert.deepEqual(validate(body,true).errors,[]);
  }
  assert.ok(validate(validPR()+'\n## Validation\n重复字段',true).errors.some(e=>e.includes('Duplicate')));
});
test('QA checkbox is visible, unique and allows follow-up notes', () => {
  assert.deepEqual(validate(validPR()+'\n\n补充：仍等待真人验收。',true).errors,[]);
  const choice='- [ ] Human QA passed';
  for(const replacement of [`<!-- ${choice} -->`,`\`\`\`\n${choice}\n\`\`\``, '', `${choice}\n- [x] Human QA passed`, '- [ ] Unknown QA']) {
    assert.ok(validate(validPR().replace(choice,replacement),true).errors.length);
  }
  const human=validPR().replace(choice,'- [x] Human QA passed');
  for(const evidence of ['<!-- @reviewer confirmed -->','\`\`\`\n@reviewer confirmed\n\`\`\`']) {
    assert.deepEqual(validate(human+'\n'+evidence,true).errors, []);
  }
});
test('bare completion placeholders are reported without a minimum word count', () => {
  for(const value of ['ok','OK','done','passed','已完成','通过']) {
    assert.ok(validate(validPR().replace('Ran the controller regression tests.',value),true).errors.length);
  }
  assert.deepEqual(validate(validPR().replace('Ran the controller regression tests.','单测 3 项通过。'),true).errors,[]);
});


test('legacy QA labels accept checking without a record and remain unique', () => {
  const legacy = validPR().replace('Human QA passed', '已通过真人 QA');
  assert.deepEqual(validate(legacy, true).errors, []);
  const confirmed = legacy.replace('- [ ] 已通过真人 QA', '- [x] 已通过真人 QA');
  assert.deepEqual(validate(confirmed, true).errors, []);
  assert.deepEqual(validate(confirmed + '\n@reviewer confirmed in review #123.', true).errors, []);
  const mixed = legacy + '\n- [ ] Human QA passed';
  assert.ok(validate(mixed, true).errors.some(error => error.includes('exactly one')));
});
