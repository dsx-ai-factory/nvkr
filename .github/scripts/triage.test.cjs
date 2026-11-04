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
function harness({ issue, pr, available = ALL_LABELS, readError, removeStatus, addError }) {
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
      pulls: { get: async () => ({ data: pr }) },
    },
    paginate: async (endpoint, params) => {
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
