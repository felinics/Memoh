import assert from 'node:assert/strict';
import test from 'node:test';
import {
  createBudgetedGithub,
  gate,
  governBotPRs,
  hasSufficientQuota,
  inspectPR,
  MAX_API_CALLS,
  MAX_COMMENT_PAGES,
  MAX_FILE_PAGES,
  MIN_REMAINING_QUOTA,
  run,
  syncLabels,
} from './contribution-governance.mjs';

const body = `## Author\n- [x] Agent\n## Type\n- [x] bug\n## Summary\nFix the reported issue\n## Validation\nRegression tests passed\n## Screenshots / Recordings\nBackend-only change without a UI; verified using API requests\n## Human QA\n- [ ] Human QA passed`;
const pr = {
  number: 1,
  state: 'open',
  head: { sha: 'abc', repo: { id: 2 }, ref: 'patch' },
  base: { ref: 'main', repo: { id: 1 } },
  body,
  labels: [],
  user: { login: 'author' },
  changed_files: 1,
  additions: 10,
  deletions: 0,
  comments: 0,
};

const writeNames = new Set([
  'issues.addLabels',
  'issues.removeLabel',
  'issues.createComment',
  'issues.updateComment',
  'repos.createCommitStatus',
  'actions.approveWorkflowRun',
  'actions.cancelWorkflowRun',
  'actions.reRunWorkflow',
]);

function mock({
  fresh = pr,
  issue = { number: 1, state: 'open', body: '', labels: [], user: { login: 'person' }, comments: 0 },
  openPRs = [],
  files = [{ filename: 'apps/web/a.vue', additions: 10, deletions: 0 }],
  comments = [],
  remaining = 4800,
  limit = 5000,
  removeLabelError = null,
} = {}) {
  const calls = [];
  const warnings = [];
  const failures = [];
  const record = (name, args, data = {}) => {
    calls.push({ name, args });
    return { data };
  };

  const rest = {
    rateLimit: {
      get: async () => ({ data: { resources: { core: { remaining, limit } } } }),
    },
    pulls: {
      get: async args => record('pulls.get', args, typeof fresh === 'function' ? fresh(args.pull_number) : fresh),
      list: async args => record('pulls.list', args, openPRs),
      listFiles: async args => {
        const perPage = args.per_page ?? 100;
        const page = args.page ?? 1;
        const list = typeof files === 'function' ? files(args.pull_number) : files;
        const slice = list.slice((page - 1) * perPage, page * perPage);
        return record('pulls.listFiles', args, slice);
      },
    },
    issues: {
      get: async args => record('issues.get', args, issue),
      listComments: async args => {
        const perPage = args.per_page ?? 100;
        const page = args.page ?? 1;
        return record('issues.listComments', args, comments.slice((page - 1) * perPage, page * perPage));
      },
      addLabels: async args => record('issues.addLabels', args),
      removeLabel: async args => {
        calls.push({ name: 'issues.removeLabel', args });
        if (removeLabelError) throw removeLabelError;
        return { data: {} };
      },
      createComment: async args => record('issues.createComment', args),
      updateComment: async args => record('issues.updateComment', args),
    },
    repos: {
      createCommitStatus: async args => record('repos.createCommitStatus', args),
      listCommitStatusesForRef: async args => record('repos.listCommitStatusesForRef', args, []),
    },
    actions: {
      approveWorkflowRun: async args => record('actions.approveWorkflowRun', args),
      cancelWorkflowRun: async args => record('actions.cancelWorkflowRun', args),
      reRunWorkflow: async args => record('actions.reRunWorkflow', args),
      listWorkflowRunsForRepo: async args => record('actions.listWorkflowRunsForRepo', args, { workflow_runs: [] }),
    },
  };

  const summary = {
    chunks: [],
    addHeading(value) { this.chunks.push(value); return this; },
    addRaw(value) { this.chunks.push(value); return this; },
    async write() {},
  };

  return {
    github: { rest, paginate: async () => { throw new Error('raw paginate reached'); } },
    calls,
    get writeCalls() { return calls.filter(call => writeNames.has(call.name)); },
    warnings,
    failures,
    summary,
    core: {
      info() {},
      warning(msg) { warnings.push(msg); },
      setFailed(msg) { failures.push(msg); },
      summary,
    },
    context: {
      eventName: 'pull_request_target',
      payload: { pull_request: { number: 1 } },
      repo: { owner: 'felinics', repo: 'Memoh' },
      serverUrl: 'https://github.com',
      runId: 99,
    },
  };
}

test('budgeted client forbids unbounded paginate and enforces hard per-run API call cap', async () => {
  const m = mock();
  const client = createBudgetedGithub(m.github, { maxCalls: 3 });
  assert.throws(() => client.paginate(), /paginate is forbidden/i);
  await client.rest.pulls.get({ pull_number: 1 });
  await client.rest.pulls.get({ pull_number: 2 });
  await client.rest.pulls.get({ pull_number: 3 });
  assert.equal(client.getCallCount(), 3);
  await assert.rejects(client.rest.pulls.get({ pull_number: 4 }), /API call budget exceeded/i);
  assert.equal(m.calls.length, 3);
  assert.equal(MAX_API_CALLS, 50);
  assert.equal(MAX_FILE_PAGES, 30);
});

test('quota guard scales dynamically with token limit and allows manual dispatch above minimal floor', async () => {
  const low5k = mock({ limit: 5000, remaining: MIN_REMAINING_QUOTA - 1 });
  assert.equal(await hasSufficientQuota(low5k.github, low5k.core), false);
  assert.equal(low5k.warnings.length, 1);

  const runLow = mock({ limit: 5000, remaining: MIN_REMAINING_QUOTA - 1 });
  await run(runLow);
  assert.deepEqual(runLow.calls, []);

  // On a 1,000/hr token pool, 20% floor is 200 rather than deadlocking at 1,000.
  const ok1k = mock({ limit: 1000, remaining: 999 });
  assert.equal(await hasSufficientQuota(ok1k.github, ok1k.core), true);
  const low1k = mock({ limit: 1000, remaining: 199 });
  assert.equal(await hasSufficientQuota(low1k.github, low1k.core), false);

  // Manual workflow_dispatch still works when remaining is below 1,000 but comfortably above single-run budget.
  const manualMid = mock({
    limit: 5000,
    remaining: 500,
    fresh: number => ({ ...pr, number, labels: [{ name: 'bug' }, { name: 'size:XS' }, { name: 'change:web' }] }),
  });
  manualMid.context.eventName = 'workflow_dispatch';
  manualMid.context.payload = { inputs: { pr_number: '12' } };
  await run(manualMid);
  assert.equal(manualMid.calls[0].args.pull_number, 12);

  const manualExhausted = mock({ limit: 5000, remaining: MAX_API_CALLS - 1 });
  manualExhausted.context.eventName = 'workflow_dispatch';
  manualExhausted.context.payload = { inputs: { pr_number: '12' } };
  await run(manualExhausted);
  assert.deepEqual(manualExhausted.calls, []);

  const botLow = mock({ limit: 5000, remaining: 999, openPRs: [{ number: 5, user: { login: 'github-actions[bot]' } }] });
  await governBotPRs(botLow);
  assert.deepEqual(botLow.calls, []);
});

test('unchanged valid PR is idempotent, writes no commit status or workflow approval, and uses 3 read calls', async () => {
  const m = mock({
    fresh: { ...pr, labels: [{ name: 'bug' }, { name: 'size:XS' }, { name: 'change:web' }] },
  });
  await inspectPR(m, 1);
  assert.deepEqual(m.writeCalls, []);
  assert.deepEqual(m.calls.map(c => c.name), ['pulls.get', 'pulls.listFiles', 'issues.listComments']);
});

test('invalid PR description adds needs:format and posts English feedback without touching CI runs or statuses', async () => {
  const invalid = { ...pr, body: body.replace('- [x] Agent', '- [x] Agent\n- [x] Human') };
  const m = mock({ fresh: invalid });
  await inspectPR(m, 1);
  assert.ok(!m.calls.some(c => c.name.startsWith('actions.') || c.name.startsWith('repos.')));
  const added = m.calls.find(c => c.name === 'issues.addLabels');
  assert.ok(added.args.labels.includes('needs:format'));
  assert.ok(added.args.labels.includes('size:XS'));
  assert.ok(added.args.labels.includes('change:web'));
  const comment = m.calls.find(c => c.name === 'issues.createComment').args.body;
  assert.match(comment, /@author Please complete/);
  assert.match(comment, /Author/);
  assert.doesNotMatch(comment, /\p{Script=Han}/u);
});

test('fixed descriptions update existing bot comments even beyond the first 100 or 500 discussion comments', async () => {
  for (const fresh of [{ ...pr, body: '' }, pr]) {
    const m = mock({
      fresh,
      comments: [{ id: 5, user: { login: 'github-actions[bot]' }, body: '<!-- memoh-contribution-format:v1 -->\nPrevious feedback' }],
    });
    await inspectPR(m, 1);
    assert.equal(m.calls.filter(c => c.name === 'issues.updateComment').length, 1);
    assert.equal(m.calls.filter(c => c.name === 'issues.createComment').length, 0);
    const updated = m.calls.find(c => c.name === 'issues.updateComment').args.body;
    assert.match(updated, fresh.body ? /Format check passed/ : /Please complete/);
    assert.doesNotMatch(updated, /\p{Script=Han}/u);
  }

  // Bot comment on page 2 (comment #106) is found and updated rather than posting duplicates.
  const filler = count => Array.from({ length: count }, (_, idx) => ({ id: idx + 1, user: { login: 'reviewer' }, body: `note ${idx}` }));
  const page2Comments = [
    ...filler(105),
    { id: 900, user: { login: 'github-actions[bot]' }, body: '<!-- memoh-contribution-format:v1 -->\nPrevious error' },
  ];
  const over100 = mock({ fresh: { ...pr, comments: 106 }, comments: page2Comments });
  await inspectPR(over100, 1);
  assert.equal(over100.calls.filter(c => c.name === 'issues.listComments').length, 2);
  assert.equal(over100.calls.filter(c => c.name === 'issues.updateComment').length, 1);
  assert.equal(over100.calls.find(c => c.name === 'issues.updateComment').args.comment_id, 900);
  assert.equal(over100.calls.filter(c => c.name === 'issues.createComment').length, 0);

  // Bot comment near the tail of a 720-comment thread is still reached within MAX_COMMENT_PAGES calls.
  const deepComments = [
    ...filler(714),
    { id: 901, user: { login: 'github-actions[bot]' }, body: '<!-- memoh-contribution-format:v1 -->\nPrevious error' },
    ...filler(5),
  ];
  const over500 = mock({ fresh: { ...pr, comments: 720 }, comments: deepComments });
  await inspectPR(over500, 1);
  assert.ok(over500.calls.filter(c => c.name === 'issues.listComments').length <= MAX_COMMENT_PAGES);
  assert.equal(over500.calls.find(c => c.name === 'issues.updateComment').args.comment_id, 901);
});

test('synchronization removes obsolete managed labels, survives heavy 5-page label churn within budget, and tolerates 404s', async () => {
  const m = mock();
  await syncLabels(m.github, m.context.repo, { number: 1, labels: [{ name: 'size:XS' }, { name: 'custom' }] }, ['size:M'], name => name.startsWith('size:'));
  assert.deepEqual(m.calls.map(c => c.name), ['issues.addLabels', 'issues.removeLabel']);
  assert.equal(m.calls[1].args.name, 'size:XS');

  const race = mock({ removeLabelError: Object.assign(new Error('Not Found'), { status: 404 }) });
  await syncLabels(race.github, race.context.repo, { number: 1, labels: [{ name: 'needs:format' }] }, [], name => name === 'needs:format');
  assert.equal(race.calls.length, 1);

  // 450 files (5 pages) + removing 7 stale labels + updating bot comment stays within MAX_API_CALLS (25).
  const heavyFiles = Array.from({ length: 450 }, (_, i) => ({ filename: `docs/f${i}.md`, additions: 2, deletions: 0 }));
  const heavy = mock({
    fresh: {
      ...pr,
      changed_files: 450,
      labels: [
        { name: 'feat' },
        { name: 'needs:format' },
        { name: 'size:XL' },
        { name: 'change:web' },
        { name: 'change:desktop' },
        { name: 'change:migrations' },
        { name: 'change:server' },
      ],
    },
    files: heavyFiles,
    comments: [{ id: 5, user: { login: 'github-actions[bot]' }, body: '<!-- memoh-contribution-format:v1 -->\nPrevious error' }],
  });
  await inspectPR(heavy, 1);
  assert.equal(heavy.calls.filter(c => c.name === 'issues.removeLabel').length, 7);
  assert.ok(heavy.calls.length <= MAX_API_CALLS);
});

test('file classification excludes generated files past 500 files up to 3,000 and preserves labels above 3,000', async () => {
  const makeFiles = count => Array.from({ length: count }, (_, i) => ({ filename: `apps/web/f${i}.ts`, additions: 1, deletions: 0 }));

  const exact100 = mock({ fresh: { ...pr, changed_files: 100 }, files: makeFiles(100) });
  await inspectPR(exact100, 1);
  assert.equal(exact100.calls.filter(c => c.name === 'pulls.listFiles').length, 1);
  assert.ok(exact100.calls.find(c => c.name === 'issues.addLabels').args.labels.includes('size:S'));

  // 501 generated icon files (+5,010 raw lines) still exclude generated outputs and classify as size:XS, never size:XL.
  const generatedIcons = Array.from({ length: 501 }, (_, i) => ({
    filename: `packages/icons/src/icons/GeneratedIcon${i}.vue`,
    additions: 10,
    deletions: 0,
  }));
  const iconPR = mock({
    fresh: { ...pr, changed_files: 501, additions: 5010, deletions: 0, labels: [{ name: 'size:XS' }] },
    files: generatedIcons,
  });
  await inspectPR(iconPR, 1);
  assert.equal(iconPR.calls.filter(c => c.name === 'pulls.listFiles').length, 6);
  assert.ok(!iconPR.calls.some(c => c.name === 'issues.removeLabel' && c.args.name === 'size:XS'));
  assert.deepEqual(iconPR.calls.find(c => c.name === 'issues.addLabels').args.labels, ['bug', 'change:web']);

  // Beyond GitHub REST API's 3,000-file ceiling, preserve existing size/change labels rather than using unfiltered totals.
  const hugeCount = MAX_FILE_PAGES * 100 + 1;
  const oversized = mock({
    fresh: {
      ...pr,
      changed_files: hugeCount,
      additions: 4000,
      deletions: 100,
      labels: [{ name: 'size:XS' }, { name: 'change:server' }],
    },
    files: [],
  });
  await inspectPR(oversized, 1);
  assert.equal(oversized.calls.filter(c => c.name === 'pulls.listFiles').length, 0);
  assert.deepEqual(oversized.calls.find(c => c.name === 'issues.addLabels').args.labels, ['bug']);
  assert.ok(!oversized.calls.some(c => c.name === 'issues.removeLabel'));

  const truncated = mock({ files: [], fresh: { ...pr, changed_files: 2, labels: [{ name: 'size:L' }] } });
  await assert.rejects(inspectPR(truncated, 1), /Incomplete PR file list/);
  assert.ok(!truncated.calls.some(c => c.name === 'issues.removeLabel' && c.args.name === 'size:L'));
  assert.ok(!truncated.calls.some(c => c.name === 'issues.addLabels' && c.args.labels.includes('needs:format')));
});

test('issues use current API body, closed items do nothing, and dispatch or unknown events never scan all PRs', async () => {
  const issueMock = mock({ issue: { number: 7, state: 'open', body: '', labels: [], user: { login: 'person' }, comments: 0 } });
  issueMock.context.eventName = 'issues';
  issueMock.context.payload = { issue: { number: 7, body: 'stale valid body' } };
  await run(issueMock);
  assert.ok(issueMock.calls.find(c => c.name === 'issues.createComment').args.body.includes('@person'));
  assert.ok(issueMock.calls.find(c => c.name === 'issues.addLabels').args.labels.includes('needs:format'));

  const closedPR = mock({ fresh: { ...pr, state: 'closed' } });
  await run(closedPR);
  assert.deepEqual(closedPR.calls.map(c => c.name), ['pulls.get']);

  const closedIssue = mock({ issue: { number: 8, state: 'closed', body: '', labels: [], user: { login: 'person' } } });
  closedIssue.context.eventName = 'issues';
  closedIssue.context.payload = { issue: { number: 8 } };
  await run(closedIssue);
  assert.deepEqual(closedIssue.calls.map(c => c.name), ['issues.get']);

  const dispatchOK = mock({ fresh: number => ({ ...pr, number, labels: [{ name: 'bug' }, { name: 'size:XS' }, { name: 'change:web' }] }) });
  dispatchOK.context.eventName = 'workflow_dispatch';
  dispatchOK.context.payload = { inputs: { pr_number: '42' } };
  await run(dispatchOK);
  assert.equal(dispatchOK.calls[0].args.pull_number, 42);

  for (const bad of ['', 'abc', '0', '-3']) {
    const dispatchBad = mock();
    dispatchBad.context.eventName = 'workflow_dispatch';
    dispatchBad.context.payload = { inputs: { pr_number: bad } };
    await assert.rejects(run(dispatchBad), /positive integer pr_number/);
    assert.deepEqual(dispatchBad.calls, []);
  }

  for (const eventName of ['schedule', 'workflow_run']) {
    const unsupported = mock();
    unsupported.context.eventName = eventName;
    unsupported.context.payload = {};
    await assert.rejects(run(unsupported), /Unsupported event/);
    assert.deepEqual(unsupported.calls, []);
  }
});

test('governBotPRs isolates per-PR failures and budgets while inspecting only open github-actions[bot] PRs', async () => {
  const m = mock({
    openPRs: [
      { number: 10, user: { login: 'external-contributor' } },
      { number: 11, user: { login: 'github-actions[bot]' } },
      { number: 12, user: { login: 'github-actions[bot]' } },
    ],
    fresh: number => ({ ...pr, number, changed_files: number === 11 ? 2 : 1, user: { login: 'github-actions[bot]' } }),
    files: number => (number === 11 ? [] : [{ filename: 'AGENTS.md', additions: 5, deletions: 2 }]),
  });
  await governBotPRs(m);
  assert.equal(m.calls.filter(c => c.name === 'pulls.list').length, 1);
  assert.deepEqual(m.calls.filter(c => c.name === 'pulls.get').map(c => c.args.pull_number), [11, 12]);
  assert.equal(m.failures.length, 1);
  assert.match(m.failures[0], /PR #11/);
  assert.ok(m.calls.some(c => c.name === 'issues.addLabels' && c.args.issue_number === 12));
});

test('legacy gate requires no API status, body validation, or controller wait', async () => {
  let message;
  await gate({ core: { info(value) { message = value; } } });
  assert.ok(message.includes('does not wait'));
});
