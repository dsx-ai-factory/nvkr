// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Issue and pull request metadata decisions for triage.yaml, pr-title-lint.yaml
// and pr-label-guard.yaml. Each entry point takes actions/github-script's
// { github, context, core } and reads current state from the API, so a rerun
// acts on the issue or PR as it is now rather than on the event snapshot.
// Tests: .github/scripts/triage.test.cjs (run by `make test-workflow-scripts`).

// CONTRIBUTING.md owns the supported types. There is no NVKR title-length
// limit or requirement for a lowercase scope.
const TITLE_TYPES = ['feat', 'fix', 'refactor', 'docs', 'test', 'perf', 'build', 'ci', 'chore'];
const TITLE_PATTERN = new RegExp(
  `^(${TITLE_TYPES.join('|')})(\\([a-zA-Z0-9][a-zA-Z0-9._/-]*\\))?!?: +\\S[^\\r\\n]*$`
);

// Issue-form Component values (bug and feature forms) and their area. Other /
// Unknown names no area and is left for a maintainer.
const COMPONENT_AREAS = new Map([
  ['Catalog — pins (pins.lock.yaml)', 'area/catalog'],
  ['Catalog — registry (sources / constraints / build commands)', 'area/catalog'],
  ['Definitions — output selection / catalog substitutions', 'area/definitions'],
  ['Environment — execution or publication configuration', 'area/definitions'],
  ['Run — planning, generation, or execution', 'area/run'],
  ['Release — verification or consumption', 'area/release'],
  ['CLI (nvkr)', 'area/cli'],
  ['Generator / pipeline emission', 'area/pipeline'],
  ['Signing / provenance / evidence', 'area/security'],
  ['Docs / examples', 'area/docs'],
]);

// Conventional Commit title-scope tokens and their area. Free-text keywords
// are not inferred: issue bodies quote logs and paths from unrelated areas.
const SCOPE_AREAS = new Map([
  ['catalog', 'area/catalog'], ['pins', 'area/catalog'],
  ['definition', 'area/definitions'], ['definitions', 'area/definitions'],
  ['environment', 'area/definitions'], ['environments', 'area/definitions'],
  ['run', 'area/run'], ['lifecycle', 'area/run'], ['factory', 'area/run'], ['buildgraph', 'area/run'],
  ['pipeline', 'area/pipeline'], ['generate', 'area/pipeline'],
  ['release', 'area/release'], ['delivery', 'area/release'], ['qualification', 'area/release'],
  ['security', 'area/security'], ['sign', 'area/security'], ['signing', 'area/security'],
  ['provenance', 'area/security'], ['attest', 'area/security'],
  ['cli', 'area/cli'],
  ['sdk', 'area/sdk'], ['client', 'area/sdk'],
  ['ci', 'area/ci'],
  ['docs', 'area/docs'],
  ['test', 'area/tests'], ['tests', 'area/tests'],
  ['toolchain', 'area/toolchains'], ['toolchains', 'area/toolchains'],
]);

// AICR's thresholds on changed lines (additions + deletions).
const SIZE_LABELS = [
  ['size/XS', 10],
  ['size/S', 50],
  ['size/M', 200],
  ['size/L', 500],
  ['size/XL', Infinity],
];

const DO_NOT_MERGE = 'do-not-merge';

function titleIsValid(title) {
  return typeof title === 'string' && !/[\r\n]/.test(title) && TITLE_PATTERN.test(title);
}

// Use the form's exact component, not keywords in logs or examples. Free text
// before or after the dropdown can repeat the heading; infer nothing unless
// exactly one Component section exists.
function formComponent(body) {
  const sections = [...(body || '').matchAll(/^### Component(?:\r?\n)+([^\r\n]+)/gm)];
  return sections.length === 1 ? sections[0][1].trim() : undefined;
}

// The first scope token with an area, so `fix(cli,docs): x` names area/cli.
function scopeArea(title) {
  const scope = (title || '').toLowerCase().match(/^[a-z]+\(([^)]+)\)!?: /)?.[1] || '';
  return scope.split(/[^a-z0-9]+/).map(token => SCOPE_AREAS.get(token)).find(Boolean);
}

// One inferred area, in order of reporter intent: an area already set, the
// form's Component, the documentation form's type label, then the title scope.
function inferArea(issue, existing) {
  if ([...existing].some(name => name.startsWith('area/'))) return undefined;
  return COMPONENT_AREAS.get(formComponent(issue.body)) ||
    (existing.has('documentation') ? 'area/docs' : undefined) ||
    scopeArea(issue.title);
}

function intakeLabels(issue) {
  const existing = new Set(issue.labels.map(label => label.name));
  const wanted = new Set(['needs-triage']);
  const area = inferArea(issue, existing);
  if (area) wanted.add(area);
  return [...wanted].filter(label => !existing.has(label)).sort();
}

function sizeLabel(changes) {
  return SIZE_LABELS.find(([, below]) => changes < below)[0];
}

async function issueIntake({ github, context, core }) {
  const { data: issue } = await github.rest.issues.get({
    ...context.repo,
    issue_number: context.issue.number,
  });
  // A delayed or rerun opened-event job must not reopen triage on closed work.
  if (issue.state !== 'open') {
    core.info(`Issue is ${issue.state}; skipping intake.`);
    return;
  }
  const labels = intakeLabels(issue);
  if (!labels.length) return;

  // A maintainer provisions labels. Check the entire set before writing;
  // this workflow never creates repository labels.
  const available = new Set((await github.paginate(
    github.rest.issues.listLabelsForRepo,
    { ...context.repo, per_page: 100 }
  )).map(label => label.name));
  const missing = labels.filter(label => !available.has(label));
  if (missing.length) {
    core.setFailed(`Missing repository labels: ${missing.join(', ')}`);
    return;
  }
  // Maintainers remove needs-triage after review. Type labels from issue
  // forms and inferred labels do not establish completed triage.
  await github.rest.issues.addLabels({
    ...context.repo,
    issue_number: context.issue.number,
    labels,
  });
}

async function prSize({ github, context, core }) {
  const number = context.payload.pull_request.number;
  const { data: pr } = await github.rest.pulls.get({ ...context.repo, pull_number: number });
  const size = sizeLabel(pr.additions + pr.deletions);
  const names = SIZE_LABELS.map(([name]) => name);
  const current = pr.labels.map(label => label.name).filter(name => names.includes(name));
  for (const name of current.filter(name => name !== size)) {
    try {
      await github.rest.issues.removeLabel({ ...context.repo, issue_number: number, name });
    } catch (error) {
      if (error.status !== 404) throw error; // already removed by a concurrent edit
    }
  }
  if (!current.includes(size)) {
    await github.rest.issues.addLabels({ ...context.repo, issue_number: number, labels: [size] });
  }
  core.info(`PR size: ${size}`);
}

async function titleLint({ github, context, core }) {
  // A rerun must validate the current title, not the event snapshot.
  const { data: pr } = await github.rest.pulls.get({
    ...context.repo,
    pull_number: context.payload.pull_request.number,
  });
  if (!titleIsValid(pr.title)) {
    core.setFailed(
      'Use type(optional-scope)!: description; scope and ! are optional. ' +
      `Types: ${TITLE_TYPES.join(', ')}. See CONTRIBUTING.md. ` +
      'Editing the title reruns this check.'
    );
    return;
  }
  core.info('PR title follows the NVKR Conventional Commit format.');
}

async function doNotMergeGuard({ github, context, core }) {
  const { data: pr } = await github.rest.pulls.get({
    ...context.repo,
    pull_number: context.payload.pull_request.number,
  });
  if (pr.labels.some(label => label.name === DO_NOT_MERGE)) {
    core.setFailed(`Remove the ${DO_NOT_MERGE} label when this PR is ready to merge.`);
    return;
  }
  core.info(`No ${DO_NOT_MERGE} label.`);
}

module.exports = {
  doNotMergeGuard,
  formComponent,
  intakeLabels,
  issueIntake,
  prSize,
  sizeLabel,
  titleIsValid,
  titleLint,
};
