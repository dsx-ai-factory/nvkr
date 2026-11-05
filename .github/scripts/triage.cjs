// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Issue and pull request metadata decisions for the triage and PR workflows.
// Each entry point takes actions/github-script's
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
// `factory` spans Run execution and pipeline generation; leave that scope for
// maintainer review unless the form or another explicit scope names an area.
const SCOPE_AREAS = new Map([
  ['catalog', 'area/catalog'], ['pins', 'area/catalog'],
  ['definition', 'area/definitions'], ['definitions', 'area/definitions'],
  ['environment', 'area/definitions'], ['environments', 'area/definitions'],
  ['run', 'area/run'], ['lifecycle', 'area/run'], ['buildgraph', 'area/run'],
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
// form's Component, the title scope, then the documentation type fallback.
function inferArea(issue, existing) {
  if ([...existing].some(name => name.startsWith('area/'))) return undefined;
  return COMPONENT_AREAS.get(formComponent(issue.body)) ||
    scopeArea(issue.title) ||
    (existing.has('documentation') ? 'area/docs' : undefined);
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
  await applySize({ github, context, core }, number, pr, size);
}

async function applySize({ github, context, core }, number, pr, size) {
  const names = SIZE_LABELS.map(([name]) => name);
  const current = pr.labels.map(label => label.name).filter(name => names.includes(name));
  // Stale sizes are removed before the new one is added. GitHub writes are not
  // atomic: if the addition fails the job fails, the PR may briefly have no size
  // label, and the next synchronize run repairs it.
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

// The labeler file is the sole path mapping. Support its current rule shape;
// new labeler operators must be implemented and tested before they are used.
function labelRules(source) {
  const { parse } = require('yaml');
  const config = parse(source, { uniqueKeys: true });
  if (!config || typeof config !== 'object' || Array.isArray(config)) {
    throw new Error('Invalid labeler configuration');
  }
  return Object.entries(config).map(([label, rules]) => {
    if (!/^area\/[a-z][a-z-]*$/.test(label) || !Array.isArray(rules) || rules.length !== 1 ||
        Object.keys(rules[0] || {}).join() !== 'changed-files') {
      throw new Error('Unsupported labeler rule');
    }
    const changed = rules[0]['changed-files'];
    if (!Array.isArray(changed) || changed.length !== 1 ||
        Object.keys(changed[0] || {}).join() !== 'any-glob-to-any-file') {
      throw new Error('Unsupported changed-files rule');
    }
    const value = changed[0]['any-glob-to-any-file'];
    const globs = typeof value === 'string' ? [value] : value;
    if (!Array.isArray(globs) || !globs.length ||
        globs.some(glob => typeof glob !== 'string' || !glob.length || glob.startsWith('!'))) {
      throw new Error('Invalid labeler globs');
    }
    return { label, globs };
  });
}

function labelsForPR(pr, files, rules) {
  const { minimatch } = require('minimatch');
  if (![pr.additions, pr.deletions, pr.changed_files].every(n => Number.isSafeInteger(n) && n >= 0) ||
      files.length !== pr.changed_files || !Number.isSafeInteger(pr.additions + pr.deletions)) {
    throw new Error('Incomplete PR changes; refusing partial labels');
  }
  const labels = rules.filter(({ globs }) => files.some(file =>
    globs.some(glob => minimatch(file.filename, glob, { dot: true }))
  )).map(({ label }) => label);
  return [...labels, sizeLabel(pr.additions + pr.deletions)].sort();
}

// The producer can be changed by a PR. No artifact field grants authority:
// reject unknown fields/labels, then bind the request to GitHub's run and PR.
function validateLabelRequest(request, rules) {
  const allowed = new Set([...rules.map(rule => rule.label), ...SIZE_LABELS.map(([name]) => name)]);
  if (!request || typeof request !== 'object' || Array.isArray(request) ||
      Object.keys(request).sort().join() !== 'headSHA,labels,number,version' || request.version !== 1 ||
      !Number.isSafeInteger(request.number) || request.number <= 0 ||
      typeof request.headSHA !== 'string' || !/^[a-f0-9]{40}$/.test(request.headSHA) ||
      !Array.isArray(request.labels) || request.labels.length > allowed.size ||
      request.labels.some(label => typeof label !== 'string' || !allowed.has(label)) ||
      new Set(request.labels).size !== request.labels.length ||
      request.labels.filter(label => label.startsWith('size/')).length !== 1) {
    throw new Error('Invalid PR label request');
  }
  return { ...request, labels: [...request.labels].sort() };
}

function validateLabelOrigin(request, run, pr, repository) {
  if (run.event !== 'pull_request' || run.conclusion !== 'success' ||
      run.path !== '.github/workflows/pr-labels.yaml' ||
      !Number.isSafeInteger(repository.id) || !Number.isSafeInteger(run.head_repository?.id) ||
      pr.number !== request.number || pr.base?.repo?.id !== repository.id ||
      request.headSHA !== run.head_sha || pr.head?.sha !== run.head_sha ||
      pr.head?.repo?.id !== run.head_repository.id || pr.head?.ref !== run.head_branch ||
      // Fork runs can have no pull_requests entries. Use the independently
      // fetched PR's head repository, branch and SHA as the binding in that case.
      !Array.isArray(run.pull_requests) || (run.pull_requests.length > 0 &&
        !run.pull_requests.some(item => item.number === request.number && item.base?.repo?.id === repository.id))) {
    throw new Error('PR label request does not match the triggering run');
  }
}

async function readRules() {
  const fs = require('node:fs/promises');
  const path = require('node:path');
  return labelRules(await fs.readFile(path.join(__dirname, '../labeler.yml'), 'utf8'));
}

async function currentLabels(github, context, pr, rules) {
  const files = await github.paginate(github.rest.pulls.listFiles, {
    ...context.repo, pull_number: pr.number, per_page: 100,
  });
  return labelsForPR(pr, files, rules);
}

async function computePRLabels({ github, context, core }) {
  const fs = require('node:fs/promises');
  const path = require('node:path');
  const rules = await readRules();
  const { data: pr } = await github.rest.pulls.get({
    ...context.repo, pull_number: context.payload.pull_request.number,
  });
  if (pr.state !== 'open' || pr.base.ref !== 'main' ||
      pr.head.sha !== context.payload.pull_request.head.sha) {
    throw new Error('PR changed since the triggering event; rerun on its current head');
  }
  const request = validateLabelRequest({
    version: 1, number: pr.number, headSHA: pr.head.sha,
    labels: await currentLabels(github, context, pr, rules),
  }, rules);
  const directory = path.join(process.env.RUNNER_TEMP, 'pr-labels');
  await fs.mkdir(directory, { recursive: true });
  await fs.writeFile(path.join(directory, 'labels.json'), JSON.stringify(request) + '\n');
  core.info('Computed PR label request without label writes.');
}

async function readLabelRequest(directory, rules) {
  const fs = require('node:fs/promises');
  const path = require('node:path');
  const files = await fs.readdir(directory);
  if (files.length !== 1 || files[0] !== 'labels.json') throw new Error('Unexpected label artifact files');
  const filename = path.join(directory, 'labels.json');
  const stat = await fs.lstat(filename);
  if (!stat.isFile() || stat.size > 16384) throw new Error('Invalid label artifact file');
  return validateLabelRequest(JSON.parse(await fs.readFile(filename, 'utf8')), rules);
}

async function applyLabelRequest({ github, context, core }, request, rules) {
  request = validateLabelRequest(request, rules);
  const { data: pr } = await github.rest.pulls.get({ ...context.repo, pull_number: request.number });
  validateLabelOrigin(request, context.payload.workflow_run, pr, context.payload.repository);
  if (pr.state !== 'open' || pr.base.ref !== 'main') {
    core.info('PR is closed or targets another branch; skipping labels.');
    return;
  }
  // An allow-listed label still must match the current API data and trusted
  // config. Never let a modified producer invent areas or a misleading size.
  const expected = await currentLabels(github, context, pr, rules);
  if (JSON.stringify(request.labels) !== JSON.stringify(expected)) {
    throw new Error('Label request differs from current PR changes or default-branch configuration');
  }
  const available = new Set((await github.paginate(github.rest.issues.listLabelsForRepo, {
    ...context.repo, per_page: 100,
  })).map(label => label.name));
  if (expected.some(label => !available.has(label))) throw new Error('Missing repository labels');

  // Read the head/base again after pagination, before the first write.
  const { data: latest } = await github.rest.pulls.get({ ...context.repo, pull_number: request.number });
  validateLabelOrigin(request, context.payload.workflow_run, latest, context.payload.repository);
  if (latest.state !== 'open' || latest.base.ref !== 'main' || latest.base.sha !== pr.base.sha) {
    throw new Error('PR changed while computing labels');
  }
  const additions = expected.filter(label => label.startsWith('area/') &&
    !latest.labels.some(current => current.name === label));
  if (additions.length) {
    await github.rest.issues.addLabels({ ...context.repo, issue_number: request.number, labels: additions });
  }
  await applySize({ github, context, core }, request.number, latest,
    expected.find(label => label.startsWith('size/')));
}

async function applyPRLabels(args) {
  const path = require('node:path');
  const rules = await readRules();
  const request = await readLabelRequest(path.join(process.env.RUNNER_TEMP, 'pr-labels'), rules);
  await applyLabelRequest(args, request, rules);
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
  applyLabelRequest,
  applyPRLabels,
  computePRLabels,
  doNotMergeGuard,
  formComponent,
  intakeLabels,
  issueIntake,
  labelRules,
  labelsForPR,
  prSize,
  sizeLabel,
  readLabelRequest,
  titleIsValid,
  titleLint,
  validateLabelOrigin,
  validateLabelRequest,
};
