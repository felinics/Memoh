import assert from 'node:assert/strict';
import test from 'node:test';
import {
  createBudgetedGithub,
  gate,
  governBotPRs,
  hasSufficientQuota,
  inspectPR,
  MAX_API_CALLS,
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
  issue = { number: 1, state: 'open', body: '', labels: [], user: { login: 'person' } },
  openPRs = [],
  files = [{ filename: 'apps/web/a.vue', additions: 10, deletions: 0 }],
  comments = [],
  remaining = 4800,
  removeLabelError = null,
} = {}) {
  const calls = [];
  const warnings = [];
  const record = (name, args, data = {}) => {
    calls.push({ name, args });
    return { data };
  };

  const rest = {
    rateLimit: {
      get: async () => ({ data: { resources: { core: { remaining, limit: 5000 } } } }),
    },
    pulls: {
      get: async args => record('pulls.get', args, typeof fresh === 'function' ? fresh(args.pull_number) : fresh),
      list: async args => record('pulls.list', args, openPRs),
      listFiles: async args => {
        const perPage = args.per_page ?? 100;
        const page = args.page ?? 1;
        const slice = files.slice((page - 1) * perPage, page * perPage);
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
    summary,
    core: {
      info() {},
      warning(msg) { warnings.push(msg); },
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
  assert.equal(MAX_API_CALLS, 15);
});

test('low installation rate-limit quota skips governance cleanly with zero business API calls', async () => {
  const low = mock({ remaining: MIN_REMAINING_QUOTA - 1 });
  assert.equal(await hasSufficientQuota(low.github, low.core), false);
  assert.equal(low.warnings.length, 1);

  const runLow = mock({ remaining: MIN_REMAINING_QUOTA - 1 });
  await run(runLow);
  assert.deepEqual(runLow.calls, []);
  assert.equal(runLow.warnings.length, 1);

  const botLow = mock({ remaining: MIN_REMAINING_QUOTA - 1, openPRs: [{ number: 5, user: { login: 'github-actions[bot]' } }] });
  await governBotPRs(botLow);
  assert.deepEqual(botLow.calls, []);

  const ok = mock({ remaining: MIN_REMAINING_QUOTA });
  assert.equal(await hasSufficientQuota(ok.github, ok.core), true);
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

test('fixed descriptions update the existing bot comment in English instead of posting another', async () => {
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
});

test('synchronization removes obsolete managed labels, preserves unrelated labels, and tolerates 404 removals', async () => {
  const m = mock();
  await syncLabels(m.github, m.context.repo, { number: 1, labels: [{ name: 'size:XS' }, { name: 'custom' }] }, ['size:M'], name => name.startsWith('size:'));
  assert.deepEqual(m.calls.map(c => c.name), ['issues.addLabels', 'issues.removeLabel']);
  assert.equal(m.calls[1].args.name, 'size:XS');

  const race = mock({ removeLabelError: Object.assign(new Error('Not Found'), { status: 404 }) });
  await syncLabels(race.github, race.context.repo, { number: 1, labels: [{ name: 'needs:format' }] }, [], name => name === 'needs:format');
  assert.equal(race.calls.length, 1);
});

test('file classification handles 100-file boundary, multi-page diffs, oversized PRs, and truncated responses', async () => {
  const makeFiles = count => Array.from({ length: count }, (_, i) => ({ filename: `apps/web/f${i}.ts`, additions: 1, deletions: 0 }));

  const exact100 = mock({ fresh: { ...pr, changed_files: 100 }, files: makeFiles(100) });
  await inspectPR(exact100, 1);
  assert.equal(exact100.calls.filter(c => c.name === 'pulls.listFiles').length, 1);
  assert.ok(exact100.calls.find(c => c.name === 'issues.addLabels').args.labels.includes('size:S'));

  const multiPage = mock({ fresh: { ...pr, changed_files: 120 }, files: makeFiles(120) });
  await inspectPR(multiPage, 1);
  assert.equal(multiPage.calls.filter(c => c.name === 'pulls.listFiles').length, 2);
  assert.ok(multiPage.calls.find(c => c.name === 'issues.addLabels').args.labels.includes('size:S'));

  const hugeCount = MAX_FILE_PAGES * 100 + 1;
  const oversized = mock({
    fresh: { ...pr, changed_files: hugeCount, labels: [{ name: 'size:L' }, { name: 'change:server' }] },
    files: makeFiles(hugeCount),
  });
  await inspectPR(oversized, 1);
  assert.equal(oversized.calls.filter(c => c.name === 'pulls.listFiles').length, 0);
  assert.ok(!oversized.calls.some(c => c.name === 'issues.removeLabel'));
  assert.deepEqual(oversized.calls.find(c => c.name === 'issues.addLabels').args.labels, ['bug']);

  const truncated = mock({ files: [], fresh: { ...pr, changed_files: 2, labels: [{ name: 'size:L' }] } });
  await assert.rejects(inspectPR(truncated, 1), /Incomplete PR file list/);
  assert.ok(!truncated.calls.some(c => c.name === 'issues.removeLabel' && c.args.name === 'size:L'));
  assert.ok(!truncated.calls.some(c => c.name === 'issues.addLabels' && c.args.labels.includes('needs:format')));
});

test('issues use current API body, closed items do nothing, and dispatch or unknown events never scan all PRs', async () => {
  const issueMock = mock({ issue: { number: 7, state: 'open', body: '', labels: [], user: { login: 'person' } } });
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

test('governBotPRs inspects only open github-actions[bot] PRs in a single bounded query', async () => {
  const m = mock({
    openPRs: [
      { number: 10, user: { login: 'external-contributor' } },
      { number: 11, user: { login: 'github-actions[bot]' } },
    ],
    fresh: number => ({ ...pr, number, user: { login: 'github-actions[bot]' } }),
  });
  await governBotPRs(m);
  assert.equal(m.calls.filter(c => c.name === 'pulls.list').length, 1);
  assert.deepEqual(m.calls.filter(c => c.name === 'pulls.get').map(c => c.args.pull_number), [11]);
});

test('legacy gate requires no API status, body validation, or controller wait', async () => {
  let message;
  await gate({ core: { info(value) { message = value; } } });
  assert.ok(message.includes('does not wait'));
});
