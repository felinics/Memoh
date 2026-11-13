import { classify, typeLabels, validate } from './contribution-policy.mjs';

const marker = '<!-- memoh-contribution-format:v1 -->';

// Reserve 20% of the token's hourly core REST pool (up to 1,000 requests) so non-blocking governance never starves code CI.
export const MIN_REMAINING_QUOTA = 1000;
export const MAX_API_CALLS = 50;
export const MAX_FILE_PAGES = 30;
export const MAX_COMMENT_PAGES = 5;

export function createBudgetedGithub(github, { maxCalls = MAX_API_CALLS } = {}) {
  let calls = 0;
  const rest = new Proxy(github.rest, {
    get(target, section) {
      const group = target[section];
      if (!group || typeof group !== 'object' || section === 'rateLimit') return group;
      return new Proxy(group, {
        get(sectionTarget, method) {
          const fn = sectionTarget[method];
          if (typeof fn !== 'function') return fn;
          return async (...args) => {
            if (calls >= maxCalls) {
              throw new Error(`API call budget exceeded (${maxCalls}) while calling ${String(section)}.${String(method)}`);
            }
            calls += 1;
            return fn.apply(sectionTarget, args);
          };
        },
      });
    },
  });

  return {
    _budgeted: true,
    rest,
    getCallCount: () => calls,
    paginate() {
      throw new Error('Unbounded github.paginate is forbidden in contribution governance; use bounded page queries.');
    },
  };
}

export async function hasSufficientQuota(github, core, { manual = false } = {}) {
  if (typeof github.rest?.rateLimit?.get !== 'function') return true;
  const { data } = await github.rest.rateLimit.get();
  const coreRate = data?.resources?.core ?? data?.rate;
  const remaining = coreRate?.remaining;
  const limit = coreRate?.limit;
  const autoFloor = typeof limit === 'number' && limit > 0
    ? Math.min(MIN_REMAINING_QUOTA, Math.floor(limit * 0.2))
    : MIN_REMAINING_QUOTA;
  const floor = manual ? Math.min(MAX_API_CALLS, autoFloor) : autoFloor;
  if (typeof remaining === 'number' && remaining < floor) {
    const warn = core.warning ?? core.info;
    warn?.call(core, `Skipping contribution governance: GitHub API rate limit remaining (${remaining}) is below safety floor (${floor}).`);
    return false;
  }
  return true;
}

export async function syncLabels(github, repo, issue, wanted, managed) {
  const current = issue.labels.map(label => typeof label === 'string' ? label : label.name);
  const missing = wanted.filter(label => !current.includes(label));
  if (missing.length) await github.rest.issues.addLabels({ ...repo, issue_number: issue.number, labels: missing });
  for (const name of current.filter(label => managed(label) && !wanted.includes(label))) {
    try { await github.rest.issues.removeLabel({ ...repo, issue_number: issue.number, name }); }
    catch (error) { if (error.status !== 404) throw error; }
  }
}

async function listPRFilesBounded(client, repo, number, expectedCount) {
  if (expectedCount === 0) return [];
  const files = [];
  for (let page = 1; page <= MAX_FILE_PAGES; page++) {
    const { data: batch } = await client.rest.pulls.listFiles({ ...repo, pull_number: number, per_page: 100, page });
    files.push(...batch);
    if (batch.length < 100 || files.length >= expectedCount) break;
  }
  return files;
}

const isMarkerComment = comment => comment.user?.login === 'github-actions[bot]' && comment.body?.startsWith(marker);

async function findBotCommentBounded(client, repo, issue) {
  const { data: firstPage } = await client.rest.issues.listComments({ ...repo, issue_number: issue.number, per_page: 100, page: 1 });
  const inFirst = firstPage.find(isMarkerComment);
  if (inFirst || firstPage.length < 100) return inFirst;

  const totalComments = typeof issue.comments === 'number' ? issue.comments : 0;
  const estimatedLastPage = totalComments >= 100 ? Math.ceil((totalComments + 1) / 100) : MAX_COMMENT_PAGES;
  const remainingBudget = MAX_COMMENT_PAGES - 1;
  const startPage = Math.max(2, estimatedLastPage - remainingBudget + 1);
  for (let page = startPage; page < startPage + remainingBudget; page++) {
    const { data: batch } = await client.rest.issues.listComments({ ...repo, issue_number: issue.number, per_page: 100, page });
    const match = batch.find(isMarkerComment);
    if (match || batch.length < 100) return match;
  }
  return undefined;
}

async function publishComment(client, repo, issue, errors) {
  const previous = await findBotCommentBounded(client, repo, issue);
  if (!errors.length && !previous) return;
  const body = errors.length
    ? `${marker}\n@${issue.user.login} Please complete the following information using the template:\n\n${errors.map(error => `- ${error}`).join('\n')}\n\nEditing the description triggers another check; format feedback does not block or cancel code CI.`
    : `${marker}\nFormat check passed; removed \`needs:format\`.`;
  if (previous?.body === body) return;
  if (previous) await client.rest.issues.updateComment({ ...repo, comment_id: previous.id, body });
  else await client.rest.issues.createComment({ ...repo, issue_number: issue.number, body });
}

export async function inspectPR({ github, context, core }, number) {
  const client = github._budgeted ? github : createBudgetedGithub(github);
  const repo = context.repo;
  const { data: pr } = await client.rest.pulls.get({ ...repo, pull_number: number });
  if (pr.state !== 'open') return;
  const { errors, type } = validate(pr.body, true);
  const wanted = [...(type ? [type] : []), ...(errors.length ? ['needs:format'] : [])];

  let classification;
  let classificationError;
  const maxFiles = MAX_FILE_PAGES * 100;
  const skippedLargeDiff = pr.changed_files > maxFiles;
  if (!skippedLargeDiff) {
    try {
      const files = await listPRFilesBounded(client, repo, number, pr.changed_files);
      if (files.length !== pr.changed_files) throw new Error(`Incomplete PR file list: ${files.length}/${pr.changed_files}`);
      classification = classify(files);
      wanted.push(classification.size, ...classification.changes);
    } catch (error) {
      classificationError = error;
    }
  }

  await syncLabels(
    client,
    repo,
    pr,
    wanted,
    name => typeLabels.includes(name) || name === 'needs:format'
      || Boolean(classification && (name.startsWith('size:') || name.startsWith('change:'))),
  );
  await publishComment(client, repo, pr, errors);

  const scopeNote = classification
    ? `Filtered additions/deletions: ${classification.additions}/${classification.deletions}; Excluded ${classification.ignored} files; ${classification.size}; ${classification.changes.join(', ')}`
    : skippedLargeDiff
      ? `Skipped file classification: PR changes ${pr.changed_files} files (exceeds GitHub REST ${maxFiles}-file ceiling); existing size/change labels were preserved.`
      : 'Classification failed; existing size/change labels were preserved.';
  await core.summary
    .addHeading(`PR #${number}`)
    .addRaw(`Format: ${errors.length ? errors.join('; ') : 'Passed'}\n\nOriginal additions/deletions: ${pr.additions}/${pr.deletions}\n\n${scopeNote}\n\nCounted API calls: ${client.getCallCount()}\n`)
    .write();

  if (classificationError) throw classificationError;
}

export async function run({ github, context, core }) {
  const payload = context.payload ?? {};
  const isManual = context.eventName === 'workflow_dispatch';
  let dispatchPRNumber;
  if (isManual) {
    const raw = String(payload.inputs?.pr_number ?? '').trim();
    const parsed = Number(raw);
    if (!/^\d+$/.test(raw) || !Number.isSafeInteger(parsed) || parsed <= 0) {
      throw new Error('workflow_dispatch requires a positive integer pr_number');
    }
    dispatchPRNumber = parsed;
  } else if (context.eventName !== 'issues' && !payload.pull_request) {
    throw new Error(`Unsupported event for contribution governance: ${context.eventName}`);
  }

  if (!await hasSufficientQuota(github, core, { manual: isManual })) return;
  const client = createBudgetedGithub(github);
  const args = { github: client, context, core };

  if (context.eventName === 'issues') {
    const { data: issue } = await client.rest.issues.get({ ...context.repo, issue_number: payload.issue.number });
    if (issue.state !== 'open') return;
    const { errors, type } = validate(issue.body, false);
    await syncLabels(
      client,
      context.repo,
      issue,
      [...(type ? [type] : []), ...(errors.length ? ['needs:format'] : [])],
      name => typeLabels.includes(name) || name === 'needs:format',
    );
    await publishComment(client, context.repo, issue, errors);
    return;
  }

  return inspectPR(args, dispatchPRNumber ?? payload.pull_request.number);
}

// Reconcile open PRs created by GITHUB_TOKEN right after an internal automation workflow finishes, without scheduled polling.
export async function governBotPRs({ github, context, core }) {
  if (!await hasSufficientQuota(github, core)) return;
  const listClient = createBudgetedGithub(github);
  const { data: openPRs } = await listClient.rest.pulls.list({
    ...context.repo,
    state: 'open',
    sort: 'updated',
    direction: 'desc',
    per_page: 20,
    page: 1,
  });
  const botPRs = openPRs.filter(item => item.user?.login === 'github-actions[bot]').slice(0, 2);
  for (const item of botPRs) {
    try {
      await inspectPR({ github: createBudgetedGithub(github), context, core }, item.number);
    } catch (error) {
      core.setFailed?.(`PR #${item.number}: ${error.message}`);
    }
  }
}

// Older PR branches still call this entry point through a reusable workflow; keep it as a no-op.
export async function gate({ core }) {
  core.info('Format checks now provide independent feedback; code CI does not wait for description validation.');
}
