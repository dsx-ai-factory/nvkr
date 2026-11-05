// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

const assert = require('node:assert/strict');
const { test } = require('node:test');
const triage = require('./triage.cjs');

const repo = { owner: 'synthetic-owner', repo: 'synthetic-repo' };
const ALL_LABELS = [
  'needs-triage', 'documentation', 'bug', 'do-not-merge',
  'size/XS', 'size/S', 'size/M', 'size/L', 'size/XL',
  'area/catalog', 'area/definitions', 'area/run', 'area/pipeline', 'area/release', 'area/security',
  'area/cli', 'area/sdk', 'area/ci', 'area/docs', 'area/tests', 'area/toolchains',
];
const names = list => (list || []).map(name => ({ name }));

// Records writes and failures; reads return the given fresh state.
function harness({ issue, pr, files = [], available = ALL_LABELS, readError, removeStatus, addError }) {
  const calls = { added: [], removed: [], failed: [], info: [] };
  const github = {
    rest: {
      issues: {
        get: async () => ({ data: issue }),
        listLabelsForRepo: 'labels-endpoint',
        addLabels: async params => {
          if (addError) throw new Error('synthetic add failure');
          calls.added.push(params.labels);
        },
        removeLabel: async params => {
          if (removeStatus) throw Object.assign(new Error('synthetic'), { status: removeStatus });
          calls.removed.push(params.name);
        },
      },
      pulls: { get: async () => ({ data: pr }), listFiles: 'files-endpoint' },
    },
    paginate: async (endpoint, params) => {
      if (endpoint === 'files-endpoint') return files;
      assert.equal(endpoint, 'labels-endpoint');
      assert.equal(params.per_page, 100);
      if (readError) throw new Error('synthetic read failure');
      return names(available);
    },
  };
  const context = {
    repo,
    issue: { number: 1 },
    // The event snapshot is stale on purpose; entry points must read current state.
    payload: { pull_request: { number: 2, title: 'stale title', labels: [] } },
  };
  const core = { setFailed: m => calls.failed.push(m), info: m => calls.info.push(m) };
  return { args: { github, context, core }, calls };
}

const issue = fields => ({ state: 'open', title: '[Bug]: defect', body: '', labels: [], ...fields });

test('issue intake', async t => {
  const cases = [
    { name: 'plain issue stays pending', issue: issue(), added: [['needs-triage']] },
    { name: 'closed issue is skipped', issue: issue({ state: 'closed', title: 'fix(ci): x' }), added: [] },
    { name: 'form type label does not complete triage', issue: issue({ labels: names(['bug']) }), added: [['needs-triage']] },
    { name: 'catalog pins component', issue: issue({ body: '### Component\n\nCatalog — pins (pins.lock.yaml)\n\n### Other\nCLI' }), added: [['area/catalog', 'needs-triage']] },
    { name: 'environment component', issue: issue({ body: '### Component\n\nEnvironment — execution or publication configuration' }), added: [['area/definitions', 'needs-triage']] },
    { name: 'signing component', issue: issue({ body: '### Component\n\nSigning / provenance / evidence' }), added: [['area/security', 'needs-triage']] },
    { name: 'CRLF component', issue: issue({ body: '### Component\r\n\r\nDocs / examples\r\n' }), added: [['area/docs', 'needs-triage']] },
    { name: 'component wins over title scope', issue: issue({ title: 'fix(ci): x', body: '### Component\n\nCLI (nvkr)' }), added: [['area/cli', 'needs-triage']] },
    { name: 'repeated Component heading is ambiguous', issue: issue({ body: '### Steps\n\n### Component\n\nDocs / examples\n\n### Component\n\nCLI (nvkr)' }), added: [['needs-triage']] },
    { name: 'unknown component and keywords ignored', issue: issue({ body: '### Component\n\nOther / Unknown\n\ncatalog pipeline release docs ci' }), added: [['needs-triage']] },
    { name: 'documentation form label', issue: issue({ labels: names(['documentation']) }), added: [['area/docs', 'needs-triage']] },
    { name: 'release scope overrides documentation label', issue: issue({ title: 'docs(release): record consumer acceptance', labels: names(['documentation']) }), added: [['area/release', 'needs-triage']] },
    { name: 'definitions scope overrides documentation label', issue: issue({ title: 'docs(definitions): explain input resolution', labels: names(['documentation']) }), added: [['area/definitions', 'needs-triage']] },
    { name: 'component wins over scope and documentation label', issue: issue({ title: 'docs(release): explain', body: '### Component\n\nDefinitions — output selection / catalog substitutions', labels: names(['documentation']) }), added: [['area/definitions', 'needs-triage']] },
    { name: 'bare factory scope is ambiguous despite title keywords', issue: issue({ title: 'feat(factory): grow pipeline result collectors' }), added: [['needs-triage']] },
    { name: 'factory with pipeline component', issue: issue({ title: 'feat(factory): grow result collectors', body: '### Component\n\nGenerator / pipeline emission' }), added: [['area/pipeline', 'needs-triage']] },
    { name: 'factory with Run component', issue: issue({ title: 'feat(factory): reuse results', body: '### Component\n\nRun — planning, generation, or execution' }), added: [['area/run', 'needs-triage']] },
    { name: 'factory with explicit pipeline scope', issue: issue({ title: 'fix(factory/pipeline): collector capacity' }), added: [['area/pipeline', 'needs-triage']] },
    { name: 'focused regression keeps behavior scope', issue: issue({ title: 'test(release): verify closure' }), added: [['area/release', 'needs-triage']] },
    { name: 'shared test harness scope', issue: issue({ title: 'fix(tests): share fixture setup' }), added: [['area/tests', 'needs-triage']] },
    { name: 'explicit ci scope', issue: issue({ title: 'fix(ci): refresh action' }), added: [['area/ci', 'needs-triage']] },
    { name: 'breaking multi-token scope', issue: issue({ title: 'feat(pkg/nvkr/release)!: verify' }), added: [['area/release', 'needs-triage']] },
    { name: 'unmapped scope', issue: issue({ title: 'fix(go): bump' }), added: [['needs-triage']] },
    { name: 'existing area is kept', issue: issue({ title: 'fix(ci): x', labels: names(['area/run']) }), added: [['needs-triage']] },
    { name: 'idempotent', issue: issue({ title: 'docs(docs): clarify', labels: names(['bug', 'area/docs', 'needs-triage']) }), added: [] },
    { name: 'missing label fails with no partial write', issue: issue({ title: 'fix(ci): x' }), available: ['area/ci'], added: [], failed: 1 },
    { name: 'hostile text is data', issue: issue({ title: '`$(id)` ${{ secrets.X }}', body: '### Component\n\n"); process.exit(1); //' }), added: [['needs-triage']] },
  ];
  for (const c of cases) {
    await t.test(c.name, async () => {
      const { args, calls } = harness(c);
      await triage.issueIntake(args);
      assert.deepEqual(calls.added, c.added);
      assert.equal(calls.failed.length, c.failed || 0);
    });
  }
  await t.test('label add failure propagates', async () => {
    const { args, calls } = harness({ issue: issue(), addError: true });
    await assert.rejects(triage.issueIntake(args), /synthetic add failure/);
    assert.deepEqual(calls.failed, []);
  });
  await t.test('label read failure propagates with no write', async () => {
    const { args, calls } = harness({ issue: issue(), readError: true });
    await assert.rejects(triage.issueIntake(args), /synthetic read failure/);
    assert.deepEqual(calls.added, []);
  });
});

test('PR size', async t => {
  const cases = [
    { name: 'XS', changes: [3, 6], labels: [], added: [['size/XS']], removed: [] },
    { name: 'boundary 10 is S', changes: [10, 0], labels: [], added: [['size/S']], removed: [] },
    { name: 'M replaces stale S', changes: [150, 49], labels: ['size/S', 'bug'], added: [['size/M']], removed: ['size/S'] },
    { name: 'L', changes: [499, 0], labels: [], added: [['size/L']], removed: [] },
    { name: 'XL', changes: [400, 100], labels: [], added: [['size/XL']], removed: [] },
    { name: 'unchanged size is idempotent', changes: [1, 1], labels: ['size/XS'], added: [], removed: [] },
    { name: 'duplicate sizes collapse', changes: [60, 0], labels: ['size/XS', 'size/M', 'size/XL'], added: [], removed: ['size/XS', 'size/XL'] },
  ];
  for (const c of cases) {
    await t.test(c.name, async () => {
      const pr = { additions: c.changes[0], deletions: c.changes[1], labels: names(c.labels) };
      const { args, calls } = harness({ pr });
      await triage.prSize(args);
      assert.deepEqual(calls.added, c.added);
      assert.deepEqual(calls.removed, c.removed);
    });
  }
  await t.test('add failure propagates after the stale size was removed', async () => {
    const pr = { additions: 1, deletions: 0, labels: names(['size/XL']) };
    const { args, calls } = harness({ pr, addError: true });
    await assert.rejects(triage.prSize(args), /synthetic add failure/);
    assert.deepEqual(calls.removed, ['size/XL']);
  });
  await t.test('concurrently removed label is tolerated', async () => {
    const pr = { additions: 1, deletions: 0, labels: names(['size/XL']) };
    const { args, calls } = harness({ pr, removeStatus: 404 });
    await triage.prSize(args);
    assert.deepEqual(calls.added, [['size/XS']]);
  });
  await t.test('other remove errors propagate', async () => {
    const pr = { additions: 1, deletions: 0, labels: names(['size/XL']) };
    const { args } = harness({ pr, removeStatus: 403 });
    await assert.rejects(triage.prSize(args), /synthetic/);
  });
});

test('PR title', async t => {
  const cases = [
    ['feat: add triage', true],
    ['fix(ci): pin action', true],
    ['refactor(pkg/nvkr.factory)!: split', true],
    ['chore(Catalog): refresh', true],
    ['docs:  two spaces ok', true],
    ['Feat: capitalized type', false],
    ['feature: unknown type', false],
    ['fix(): empty scope', false],
    ['fix(ci) missing colon', false],
    ['fix: ', false],
    ['fix: line\nbreak', false],
    ['fix: line\r\nbreak', false],
    ['', false],
  ];
  for (const [title, ok] of cases) {
    await t.test(JSON.stringify(title), async () => {
      const { args, calls } = harness({ pr: { title } });
      await triage.titleLint(args);
      assert.equal(calls.failed.length, ok ? 0 : 1);
    });
  }
  await t.test('validates the current title, not the event snapshot', async () => {
    const { args, calls } = harness({ pr: { title: 'fix: current title' } });
    assert.equal(args.context.payload.pull_request.title, 'stale title');
    await triage.titleLint(args);
    assert.deepEqual(calls.failed, []);
  });
});

test('do-not-merge guard', async t => {
  const cases = [
    { name: 'no label passes', labels: ['bug', 'size/S'], failed: 0 },
    { name: 'label fails', labels: ['do-not-merge'], failed: 1 },
  ];
  for (const c of cases) {
    await t.test(c.name, async () => {
      const { args, calls } = harness({ pr: { labels: names(c.labels) } });
      await triage.doNotMergeGuard(args);
      assert.equal(calls.failed.length, c.failed);
    });
  }
});

const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const rulesSource = require('node:fs').readFileSync(path.join(__dirname, '../labeler.yml'), 'utf8');
const rules = triage.labelRules(rulesSource);
const sha = 'a'.repeat(40);
const request = () => ({ version: 1, number: 2, headSHA: sha, labels: ['area/docs', 'size/XS'] });
const labelPR = () => ({
  number: 2, state: 'open', additions: 3, deletions: 2, changed_files: 1, labels: [],
  head: { sha, ref: 'contributor-branch', repo: { id: 20 } },
  base: { sha: 'b'.repeat(40), ref: 'main', repo: { id: 10 } },
});
const labelRun = () => ({
  event: 'pull_request', conclusion: 'success', path: '.github/workflows/pr-labels.yaml',
  head_sha: sha, head_branch: 'contributor-branch', head_repository: { id: 20 }, pull_requests: [],
});

test('path labels use the maintained config, including nested and hidden Markdown', async t => {
  for (const [filename, labels] of [
    ['README.md', ['area/docs']],
    ['pkg/nvkr/factory/README.md', ['area/docs', 'area/run']],
    ['.github/scripts/README.md', ['area/ci', 'area/docs']],
    ['.github/workflows/triage.yaml', ['area/ci']],
    ['.github/workflows/nvkr-run.yml', ['area/pipeline']],
    ['pkg/nvkr/sign/testdata/fixture.json', ['area/security', 'area/tests']],
    ['unmapped/file.txt', []],
  ]) {
    await t.test(filename, () => {
      assert.deepEqual(triage.labelsForPR(labelPR(), [{ filename }], rules), [...labels, 'size/XS']);
    });
  }
  assert.throws(() => triage.labelsForPR(labelPR(), [], rules), /Incomplete/);
  assert.throws(() => triage.labelsForPR({ ...labelPR(), additions: -1 }, [{}], rules), /Incomplete/);
});

test('label config rejects unsupported operators and duplicate names', async t => {
  for (const source of [
    'null', '[]', 'do-not-merge: []',
    'area/docs:\n  - head-branch: docs',
    'area/docs:\n  - changed-files:\n    - all-globs-to-any-file: "**/*.md"',
    'area/docs:\n  - changed-files:\n    - any-glob-to-any-file: "!**/*.md"',
    rulesSource + '\narea/docs: []\n',
  ]) {
    await t.test(source.slice(0, 60), () => assert.throws(() => triage.labelRules(source)));
  }
});

test('artifact schema and config-derived allow-list fail closed', async t => {
  const cases = [
    ['null', null], ['array', []], ['missing fields', {}],
    ['string number', { ...request(), number: '2' }],
    ['fractional number', { ...request(), number: 2.5 }],
    ['zero number', { ...request(), number: 0 }],
    ['negative number', { ...request(), number: -1 }],
    ['unsafe number', { ...request(), number: Number.MAX_SAFE_INTEGER + 1 }],
    ['extra field', { ...request(), command: 'id' }],
    ['unknown version', { ...request(), version: 2 }],
    ['invalid SHA', { ...request(), headSHA: 'main' }],
    ['hold label', { ...request(), labels: ['do-not-merge', 'size/XS'] }],
    ['unconfigured area', { ...request(), labels: ['area/unknown', 'size/XS'] }],
    ['non-string label', { ...request(), labels: [{ name: 'area/docs' }, 'size/XS'] }],
    ['labels string', { ...request(), labels: 'size/XS' }],
    ['duplicate label', { ...request(), labels: ['area/docs', 'area/docs', 'size/XS'] }],
    ['multiple sizes', { ...request(), labels: ['size/S', 'size/XS'] }],
    ['missing size', { ...request(), labels: ['area/docs'] }],
  ];
  for (const [name, value] of cases) {
    await t.test(name, () => assert.throws(() => triage.validateLabelRequest(value, rules), /Invalid/));
  }
  assert.deepEqual(triage.validateLabelRequest(request(), rules), request());
  assert.throws(() => triage.validateLabelRequest(request(), rules.filter(rule => rule.label !== 'area/docs')));
});

test('artifact reader accepts only one bounded JSON regular file', async t => {
  for (const variant of ['valid', 'invalid JSON', 'oversized', 'symlink', 'extra file', 'directory', 'missing']) {
    await t.test(variant, async () => {
      const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'nvkr-label-test-'));
      t.after(() => fs.rm(directory, { recursive: true, force: true }));
      const filename = path.join(directory, 'labels.json');
      if (variant === 'symlink') await fs.symlink(__filename, filename);
      else if (variant === 'directory') await fs.mkdir(filename);
      else if (variant !== 'missing') await fs.writeFile(filename,
        variant === 'invalid JSON' ? '{' : variant === 'oversized' ? ' '.repeat(16385) : JSON.stringify(request()));
      if (variant === 'extra file') await fs.writeFile(path.join(directory, 'payload.cjs'), 'throw new Error()');
      if (variant === 'valid') assert.deepEqual(await triage.readLabelRequest(directory, rules), request());
      else await assert.rejects(triage.readLabelRequest(directory, rules));
    });
  }
});

test('apply binds untrusted requests to the run and current PR before any write', async t => {
  const cases = [
    { name: 'fork with empty run associations', ok: true },
    { name: 'same repository', ok: true, change: ({ pr, run }) => { pr.head.repo.id = 10; run.head_repository.id = 10; } },
    { name: 'associated PR', ok: true, change: ({ run }) => { run.pull_requests = [{ number: 2, base: { repo: { id: 10 } } }]; } },
    { name: 'preserve manual labels and replace stale size', ok: true, change: ({ pr }) => { pr.labels = names(['area/cli', 'bug', 'do-not-merge', 'size/XL']); }, removed: ['size/XL'] },
    { name: 'idempotent', ok: true, change: ({ pr }) => { pr.labels = names(['area/docs', 'size/XS']); }, added: [] },
    { name: 'different PR number', change: ({ value }) => { value.number = 3; } },
    { name: 'different head', change: ({ pr }) => { pr.head.sha = 'c'.repeat(40); } },
    { name: 'different fork with same head and branch', change: ({ pr }) => { pr.head.repo.id = 30; } },
    { name: 'different branch with same head', change: ({ pr }) => { pr.head.ref = 'other'; } },
    { name: 'different base repository', change: ({ pr }) => { pr.base.repo.id = 30; } },
    { name: 'wrong association', change: ({ run }) => { run.pull_requests = [{ number: 3, base: { repo: { id: 10 } } }]; } },
    { name: 'wrong artifact SHA', change: ({ value }) => { value.headSHA = 'd'.repeat(40); } },
    { name: 'wrong producer event', change: ({ run }) => { run.event = 'push'; } },
    { name: 'failed producer', change: ({ run }) => { run.conclusion = 'failure'; } },
    { name: 'wrong producer path', change: ({ run }) => { run.path = '.github/workflows/attacker.yaml'; } },
    { name: 'fabricated allowed area', change: ({ value }) => { value.labels = ['area/cli', 'size/XS']; } },
    { name: 'fabricated allowed size', change: ({ value }) => { value.labels = ['area/docs', 'size/XL']; } },
    { name: 'unknown label', change: ({ value }) => { value.labels = ['bug', 'size/XS']; } },
    { name: 'missing label', available: ['size/XS'] },
    { name: 'incomplete files', files: [] },
    { name: 'closed PR', ok: true, change: ({ pr }) => { pr.state = 'closed'; }, added: [] },
    { name: 'retargeted PR', ok: true, change: ({ pr }) => { pr.base.ref = 'release'; }, added: [] },
  ];
  for (const c of cases) {
    await t.test(c.name, async () => {
      const pr = labelPR(), run = labelRun(), value = request();
      c.change?.({ pr, run, value });
      const { args, calls } = harness({ pr, files: [{ filename: 'nested/README.md' }], ...c });
      args.context.payload = { workflow_run: run, repository: { id: 10 } };
      if (c.ok) await triage.applyLabelRequest(args, value, rules);
      else await assert.rejects(triage.applyLabelRequest(args, value, rules));
      assert.deepEqual(calls.added, c.ok ? c.added || [['area/docs'], ['size/XS']] : []);
      assert.deepEqual(calls.removed, c.removed || []);
    });
  }
  await t.test('head changes during file pagination', async () => {
    const pr = labelPR();
    const { args, calls } = harness({ pr, files: [{ filename: 'README.md' }] });
    args.context.payload = { workflow_run: labelRun(), repository: { id: 10 } };
    let reads = 0;
    args.github.rest.pulls.get = async () => ({ data: ++reads === 1 ? pr : { ...pr, head: { ...pr.head, sha: 'e'.repeat(40) } } });
    await assert.rejects(triage.applyLabelRequest(args, request(), rules), /does not match/);
    assert.deepEqual(calls.added, []);
    assert.deepEqual(calls.removed, []);
  });
  await t.test('only base SHA changes during file pagination', async () => {
    const pr = labelPR();
    // A stale size would be removed if the final guard were bypassed.
    pr.labels = names(['size/XL']);
    const { args, calls } = harness({ pr, files: [{ filename: 'README.md' }] });
    args.context.payload = { workflow_run: labelRun(), repository: { id: 10 } };
    let reads = 0;
    args.github.rest.pulls.get = async () => ({ data: ++reads === 1 ? pr : {
      ...pr, base: { ...pr.base, sha: 'e'.repeat(40) },
    } });
    await assert.rejects(triage.applyLabelRequest(args, request(), rules),
      { message: 'PR changed while computing labels' });
    assert.equal(reads, 2);
    assert.deepEqual(calls.added, []);
    assert.deepEqual(calls.removed, []);
  });
});

test('producer refuses changed PRs before writing a request', async t => {
  const cases = [
    { name: 'closed PR', change: pr => { pr.state = 'closed'; } },
    { name: 'retargeted base', change: pr => { pr.base.ref = 'release'; } },
    { name: 'head differs from triggering event', change: pr => { pr.head.sha = 'c'.repeat(40); } },
  ];
  for (const c of cases) {
    await t.test(c.name, async t => {
      const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'nvkr-compute-refusal-'));
      const previous = process.env.RUNNER_TEMP;
      process.env.RUNNER_TEMP = directory;
      t.after(async () => {
        if (previous === undefined) delete process.env.RUNNER_TEMP;
        else process.env.RUNNER_TEMP = previous;
        await fs.rm(directory, { recursive: true, force: true });
      });
      const pr = labelPR();
      c.change(pr);
      const { args, calls } = harness({ pr, files: [{ filename: 'README.md' }] });
      args.context.payload.pull_request = labelPR();
      await assert.rejects(triage.computePRLabels(args),
        { message: 'PR changed since the triggering event; rerun on its current head' });
      assert.deepEqual(await fs.readdir(directory), []);
      assert.deepEqual(calls.added, []);
      assert.deepEqual(calls.removed, []);
    });
  }
});

test('producer writes validated JSON and never labels a PR', async t => {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), 'nvkr-compute-test-'));
  const previous = process.env.RUNNER_TEMP;
  process.env.RUNNER_TEMP = directory;
  t.after(async () => {
    if (previous === undefined) delete process.env.RUNNER_TEMP;
    else process.env.RUNNER_TEMP = previous;
    await fs.rm(directory, { recursive: true, force: true });
  });
  const pr = labelPR();
  const { args, calls } = harness({ pr, files: [{ filename: 'nested/README.md' }] });
  args.context.payload.pull_request = pr;
  await triage.computePRLabels(args);
  assert.deepEqual(await triage.readLabelRequest(path.join(directory, 'pr-labels'), rules), request());
  assert.deepEqual(calls.added, []);
  assert.deepEqual(calls.removed, []);
});

test('workflow permission and checkout boundaries', async t => {
  const { parse } = require('yaml');
  const workflow = async name => parse(await fs.readFile(path.join(__dirname, `../workflows/${name}.yaml`), 'utf8'));
  for (const name of ['pr-labels', 'pr-label-guard', 'pr-title-lint']) {
    await t.test(name, async () => {
      const config = await workflow(name);
      assert.ok(config.on.pull_request.types.includes('edited'));
      assert.equal(config.on.pull_request_target, undefined);
      for (const permissions of [config.permissions, ...Object.values(config.jobs).map(job => job.permissions)]) {
        assert.ok(Object.values(permissions).every(access => access === 'read'));
      }
    });
  }
  await t.test('writer runs trusted code and downloads only the triggering artifact outside it', async () => {
    const config = await workflow('pr-labels-apply');
    assert.deepEqual(config.on, { workflow_run: { workflows: ['PR Labels'], types: ['completed'] } });
    const job = config.jobs.apply;
    assert.equal(job.permissions['pull-requests'], 'write');
    assert.equal(job.permissions.actions, 'read');
    const checkout = job.steps.filter(step => step.uses?.startsWith('actions/checkout@'));
    assert.equal(checkout.length, 1);
    assert.equal(checkout[0].with.ref, '${{ github.event.repository.default_branch }}');
    assert.equal(checkout[0].with['persist-credentials'], false);
    const download = job.steps.find(step => step.uses?.startsWith('actions/download-artifact@'));
    assert.equal(download.with['run-id'], '${{ github.event.workflow_run.id }}');
    assert.equal(download.with.name, 'pr-labels');
    assert.equal(download.with.path, '${{ runner.temp }}/pr-labels');
  });
});
