// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const https = require('node:https');
const { EventEmitter } = require('node:events');
const { Readable } = require('node:stream');
const { spawnSync } = require('node:child_process');
const transfer = require('./github_transfer_action.js');

const scope = { repository: 'example/project', run: '12', attempt: '3', workflow: 'example/project/.github/workflows/build.yml@refs/heads/main' };
const registry = transfer.registryFrom('ghcr.io/example/private-transfer');
const packageMetadata = { id: 7, name: 'private-transfer', package_type: 'container', owner: { login: 'example' }, visibility: 'private' };
const digest = transfer.digest(Buffer.from('fixture'));
const env = { GITHUB_ACTIONS: 'true', GITHUB_EVENT_NAME: 'workflow_dispatch', GITHUB_REPOSITORY: scope.repository,
  GITHUB_RUN_ID: scope.run, GITHUB_RUN_ATTEMPT: scope.attempt, GITHUB_WORKFLOW_REF: scope.workflow, GITHUB_REF: 'refs/heads/main', GITHUB_JOB: 'producer' };

async function temporary(t) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'nvkr-transfer-test-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  return fs.realpath(dir);
}

function needs(entries, extra = {}) {
  return JSON.stringify({ producer: { result: 'success', outputs: { 'nvkr-transfer-index': JSON.stringify({ version: 1, scope, entries }) } }, ...extra });
}

function metadataFixture(files) {
  const config = { version: 1, scope, name: 'test', files };
  const raw = Buffer.from(JSON.stringify(config));
  const manifest = { schemaVersion: 2, mediaType: transfer.MANIFEST_TYPE, artifactType: transfer.CONFIG_TYPE,
    config: { mediaType: transfer.CONFIG_TYPE, digest: transfer.digest(raw), size: raw.length },
    layers: files.flatMap(file => file.chunks.map(chunk => ({ mediaType: transfer.LAYER_TYPE, ...chunk }))),
    annotations: transfer.annotations(scope, 'test') };
  return { config, manifest };
}

class MemoryRegistry {
  constructor() {
    this.blobs = new Map(); this.manifests = new Map(); this.requests = []; this.package = { ...packageMetadata };
    this.corruptBlob = false; this.corruptDigests = new Set(); this.externalUpload = false;
  }

  async send(url, options = {}) {
    this.requests.push({ url: url.toString(), method: options.method || 'GET', headers: options.headers || {} });
    const response = (status, body = Buffer.alloc(0), headers = {}) => ({ status, body: Buffer.isBuffer(body) ? body : Buffer.from(JSON.stringify(body)), headers });
    if (url.hostname === 'api.github.com') {
      if (url.pathname === '/users/example') return response(200, { login: 'example', type: 'Organization' });
      if (url.pathname === '/orgs/example/packages/container/private-transfer') return response(200, this.package);
      assert.fail('unexpected GitHub API endpoint');
    }
    assert.equal(url.hostname, 'ghcr.io');
    if (url.pathname === '/token') {
      assert.match(options.headers.authorization, /^Basic /);
      return response(200, { token: 'fixture-registry-bearer' });
    }
    assert.equal(options.headers.authorization, 'Bearer fixture-registry-bearer');
    const suffix = url.pathname.replace('/v2/example/private-transfer/', '');
    const method = options.method || 'GET';
    if (suffix === 'blobs/uploads/' && method === 'POST') return response(202, undefined,
      { location: this.externalUpload ? 'https://evil.invalid/upload' : '/v2/example/private-transfer/blobs/uploads/fixture' });
    if (suffix === 'blobs/uploads/fixture' && method === 'PUT') {
      const chunks = [];
      if (Buffer.isBuffer(options.body)) chunks.push(options.body);
      else for await (const bytes of options.body) chunks.push(bytes);
      const bytes = Buffer.concat(chunks), ref = url.searchParams.get('digest');
      assert.equal(transfer.digest(bytes), ref);
      assert.equal(String(bytes.length), options.headers['content-length']);
      this.blobs.set(ref, bytes);
      return response(201, undefined, { 'docker-content-digest': ref });
    }
    if (suffix.startsWith('blobs/')) {
      const ref = suffix.slice(6), bytes = this.blobs.get(ref);
      if (!bytes) return response(404);
      if (method === 'HEAD') return response(200, undefined, { 'docker-content-digest': ref, 'content-length': String(bytes.length) });
      if (options.sink) await options.sink(this.corruptBlob || this.corruptDigests.has(ref) ? Buffer.from('bad') : bytes);
      return response(200, options.sink ? undefined : bytes);
    }
    if (suffix.startsWith('manifests/')) {
      const ref = suffix.slice(10);
      if (method === 'PUT') {
        const bytes = options.body;
        this.manifests.set(ref, bytes);
        return response(201, undefined, { 'docker-content-digest': transfer.digest(bytes) });
      }
      const bytes = this.manifests.get(ref);
      return bytes ? response(200, method === 'HEAD' ? undefined : bytes, { 'docker-content-digest': transfer.digest(bytes) }) : response(404);
    }
    assert.fail('unexpected registry endpoint');
  }

  client() { return new transfer.Registry(registry, 'transfer-user', 'dedicated-test-token', undefined, this.send.bind(this)); }
}

test('JSON rejects duplicate keys, unsafe keys, malformed values and unbounded nesting', () => {
  const invalid = ['{"a":1,"a":2}', '{"a":{"x":1,"x":2}}', '{"__proto__":{}}', '{"a":01}', '[true,]', '{"a":1}null', '[',
    '['.repeat(34) + '0' + ']'.repeat(34), '"unterminated', '"bad\u0000string"'];
  for (const raw of invalid) assert.throws(() => transfer.parseJSON(raw), /NVKR-E-VERIFY-FAILED/, raw);
  for (const value of [{ a: 'escaped\\"value', b: [null, true, -2.5e12] }, [], {}, 123]) {
    assert.deepEqual(transfer.parseJSON(JSON.stringify(value)), value);
  }
});

test('only exact canonical packages, names and dispatch scopes are admitted', () => {
  for (const raw of ['http://ghcr.io/org/name', 'ghcr.io/Org/name', 'ghcr.io/org/../name', 'ghcr.io/org/name:tag', 'ghcr.io/org/name@' + digest]) {
    assert.throws(() => transfer.registryFrom(raw), /NVKR-E-USAGE/, raw);
  }
  assert.deepEqual(transfer.namesFrom('{b,a}'), ['a', 'b']);
  for (const raw of ['*', '{a,a}', '{a,*}', '{a,}', '../a', '', 'a,b']) assert.throws(() => transfer.namesFrom(raw), /NVKR-E-USAGE/, raw);
  assert.deepEqual(transfer.scopeFrom(env), scope);
  assert.deepEqual(transfer.scopeFrom({ ...env, GITHUB_EVENT_NAME: 'push' }), scope);
  for (const patch of [{ GITHUB_EVENT_NAME: 'pull_request' }, { GITHUB_ACTIONS: 'false' }, { GITHUB_RUN_ATTEMPT: '0' },
    { GITHUB_WORKFLOW_REF: 'other/repo/.github/workflows/build.yml@refs/heads/main' }, { GITHUB_WORKFLOW_REF: scope.workflow.replace('refs/heads/main', 'refs/tags/v1') }]) {
    assert.throws(() => transfer.scopeFrom({ ...env, ...patch }), /NVKR-E-ENGINE-CAPABILITY/);
  }
  assert.notEqual(transfer.transferTag(scope, 'a'), transfer.transferTag({ ...scope, attempt: '4' }, 'a'));
  assert.notEqual(transfer.transferTag(scope, 'a'), transfer.transferTag(scope, 'b'));
});

test('package admission rejects public, linked, missing and substituted identities', () => {
  for (const patch of [{ visibility: 'public' }, { visibility: undefined }, { repository: { full_name: scope.repository } },
    { id: 0 }, { name: 'other' }, { owner: { login: 'other' } }, { package_type: 'npm' }]) {
    assert.throws(() => transfer.validatePackage({ ...packageMetadata, ...patch }, registry), /NVKR-E-TRUST-POLICY-MISSING/);
  }
  assert.equal(transfer.validatePackage(packageMetadata, registry), 7);
  assert.equal(transfer.validatePackage({ ...packageMetadata, visibility: 'internal', repository: null }, registry), 7);
});

test('consumer index binds successful producer, exact scope and immutable digest', () => {
  assert.equal(transfer.selectIndexes(needs({ first: digest }), ['first'], scope).get('first'), digest);
  for (const raw of [needs({}), needs({ first: 'latest' }), needs({ first: digest }).replace('success', 'failure'),
    needs({ first: digest }).replace('\\"attempt\\":\\"3\\"', '\\"attempt\\":\\"4\\"'),
    needs({ first: digest }, { other: { result: 'success', outputs: { 'nvkr-transfer-index': JSON.stringify({ version: 1, scope, entries: { first: digest } }) } } })]) {
    assert.throws(() => transfer.selectIndexes(raw, ['first'], scope), /NVKR-E-VERIFY-FAILED/);
  }
});

test('manifests and file membership reject substituted subjects and traversal', () => {
  const fixture = metadataFixture([{ path: 'data.bin', size: 7, digest, chunks: [{ digest, size: 7 }] }]);
  const raw = Buffer.from(JSON.stringify(fixture.manifest));
  assert.deepEqual(transfer.validateManifest(raw, transfer.digest(raw), scope, 'test'), fixture.manifest);
  assert.throws(() => transfer.validateManifest(raw, digest, scope, 'test'), /NVKR-E-VERIFY-FAILED/);
  assert.throws(() => transfer.validateManifest(raw, transfer.digest(raw), { ...scope, run: '13' }, 'test'), /NVKR-E-VERIFY-FAILED/);
  assert.deepEqual(transfer.validateConfig(fixture.config, fixture.manifest, scope, 'test'), fixture.config.files);
  for (const member of ['/absolute', '../escape', 'a/../escape', 'a//b', 'a\\b', 'a\u0000b']) {
    const bad = structuredClone(fixture); bad.config.files[0].path = member;
    assert.throws(() => transfer.validateConfig(bad.config, bad.manifest, scope, 'test'), /NVKR-E-VERIFY-FAILED/, member);
  }
  for (const mutate of [f => { f.config.files.push(f.config.files[0]); }, f => { f.manifest.layers.push(f.manifest.layers[0]); },
    f => { f.config.files[0].size++; }, f => { f.config.files[0].chunks[0].size++; }]) {
    const bad = structuredClone(fixture); mutate(bad);
    assert.throws(() => transfer.validateConfig(bad.config, bad.manifest, scope, 'test'), /NVKR-E-VERIFY-FAILED/);
  }
});

test('filesystem selection preserves names and rejects symlinks, overlap and globbing', async t => {
  const root = await temporary(t);
  await fs.mkdir(path.join(root, 'input'));
  await fs.writeFile(path.join(root, 'input', 'one'), '1');
  await fs.writeFile(path.join(root, 'input', 'two'), '2');
  assert.deepEqual((await transfer.collectFiles('input', root)).map(([name]) => name), ['one', 'two']);
  assert.deepEqual((await transfer.collectFiles('input/one\ninput/two', root)).map(([name]) => name), ['one', 'two']);
  for (const selected of ['.', '../outside', 'input/*', 'input\ninput/one']) await assert.rejects(transfer.collectFiles(selected, root), /NVKR-E-/);
  await fs.symlink(path.join(root, 'input', 'one'), path.join(root, 'input', 'link'));
  await assert.rejects(transfer.collectFiles('input', root), /NVKR-E-VERIFY-FAILED/);
});

test('private GHCR round trip restores exact files, carries only digests in outputs, and never uses mutable reads', async t => {
  const root = await temporary(t), storage = new MemoryRegistry(), client = storage.client();
  await fs.mkdir(path.join(root, 'input')); await fs.mkdir(path.join(root, 'input', 'nested'));
  await fs.writeFile(path.join(root, 'input', 'one.bin'), Buffer.from([0, 1, 2, 255]));
  await fs.writeFile(path.join(root, 'input', 'nested', 'empty'), '');
  await client.admit(); await client.authenticate(true);
  const ref = await transfer.upload(client, scope, 'test', 'input', root);
  await transfer.download(client, scope, ['test'], new Map([['test', ref]]), 'result', root);
  assert.deepEqual(await fs.readFile(path.join(root, 'result', 'one.bin')), Buffer.from([0, 1, 2, 255]));
  assert.equal((await fs.stat(path.join(root, 'result', 'one.bin'))).mode & 0o777, 0o600);
  assert.equal((await fs.stat(path.join(root, 'result', 'nested', 'empty'))).size, 0);
  assert.ok(storage.requests.filter(call => call.method === 'GET' && call.url.includes('/manifests/')).every(call => call.url.includes('/manifests/sha256:')));
  await assert.rejects(transfer.upload(client, scope, 'test', 'input', root), /NVKR-E-IMMUTABLE-VIOLATION/);
  await assert.rejects(transfer.download(client, scope, ['test'], new Map([['test', ref]]), 'result', root), /NVKR-E-IMMUTABLE-VIOLATION/);
});

test('upload refuses public packages before registry traffic, even with a valid secret', async t => {
  const root = await temporary(t), storage = new MemoryRegistry();
  await fs.writeFile(path.join(root, 'payload'), 'must stay private');
  storage.package.visibility = 'public';
  await assert.rejects(transfer.run({ ...env, GITHUB_WORKSPACE: root, RUNNER_TEMP: root, INPUT_MODE: 'upload',
    INPUT_REGISTRY: registry.registry, INPUT_USERNAME: 'transfer-user', INPUT_TOKEN: 'dedicated-test-token', INPUT_NAME: 'test', INPUT_PATH: 'payload' }, storage.send.bind(storage)), /NVKR-E-TRUST-POLICY-MISSING/);
  assert.ok(storage.requests.every(call => new URL(call.url).hostname === 'api.github.com'));
});

test('upload rejects cross-host credential redirects and consumer rejects corrupt payloads without committed files', async t => {
  const root = await temporary(t), storage = new MemoryRegistry(), client = storage.client();
  await fs.writeFile(path.join(root, 'payload'), 'private bytes');
  await client.admit(); await client.authenticate(true);
  storage.externalUpload = true;
  await assert.rejects(transfer.upload(client, scope, 'test', 'payload', root), /NVKR-E-VERIFY-FAILED/);
  assert.ok(storage.requests.every(call => !call.url.includes('evil.invalid')));
  storage.externalUpload = false;
  const ref = await transfer.upload(client, scope, 'test', 'payload', root);
  storage.corruptBlob = true;
  await assert.rejects(transfer.download(client, scope, ['test'], new Map([['test', ref]]), 'output', root), /NVKR-E-VERIFY-FAILED/);
  await assert.rejects(fs.stat(path.join(root, 'output', 'payload')), { code: 'ENOENT' });
});

test('per-job index outputs integrate upload and digest-selected download', async t => {
  const root = await temporary(t), storage = new MemoryRegistry(), output = path.join(root, 'github-output');
  await fs.writeFile(path.join(root, 'payload'), 'source helper'); await fs.writeFile(output, '');
  const common = { ...env, GITHUB_WORKSPACE: root, RUNNER_TEMP: root, GITHUB_OUTPUT: output,
    INPUT_REGISTRY: registry.registry, INPUT_USERNAME: 'transfer-user', INPUT_TOKEN: 'dedicated-test-token' };
  await transfer.run({ ...common, INPUT_MODE: 'upload', INPUT_NAME: 'helper', INPUT_PATH: 'payload' }, storage.send.bind(storage));
  await transfer.run({ ...common, INPUT_MODE: 'index' }, storage.send.bind(storage));
  const raw = (await fs.readFile(output, 'utf8')).trim().slice('index='.length);
  assert.equal(transfer.parseJSON(raw).scope.attempt, scope.attempt);
  assert.ok(!raw.includes('source helper'));
  const expected = JSON.stringify({ producer: { result: 'success', outputs: { 'nvkr-transfer-index': raw } } });
  await transfer.run({ ...common, GITHUB_JOB: 'consumer', INPUT_MODE: 'download', INPUT_PATTERN: 'helper', INPUT_PATH: 'restored', 'INPUT_EXPECTED-INDEX': expected }, storage.send.bind(storage));
  assert.equal(await fs.readFile(path.join(root, 'restored', 'payload'), 'utf8'), 'source helper');
});

test('early final HTTP response stops and joins a blocked upload writer', async t => {
  const original = https.request;
  t.after(() => { https.request = original; });
  let request;
  https.request = () => {
    request = new EventEmitter();
    request.writableFinished = false;
    request.setTimeout = () => {};
    request.write = () => {
      const response = Readable.from([]);
      response.statusCode = 403; response.headers = {};
      process.nextTick(() => request.emit('response', response));
      return false;
    };
    request.end = () => { request.writableFinished = true; };
    request.destroy = error => {
      if (request.destroyed) return;
      request.destroyed = true;
      process.nextTick(() => request.emit('error', error || new Error('destroyed')));
    };
    return request;
  };
  const body = Readable.from([Buffer.from('secret payload'), Buffer.from('more')]);
  await assert.rejects(transfer.request(new URL('https://ghcr.io/fixture'), { method: 'PUT', body }), /NVKR-E-UNAVAILABLE/);
  assert.equal(request.destroyed, true);
  assert.equal(body.destroyed, true);
});

test('signed CDN download strips credentials and authenticates exact streamed bytes', async () => {
  const payload = Buffer.from('private payload'), ref = transfer.digest(payload), calls = [];
  const client = new transfer.Registry(registry, 'transfer-user', 'dedicated-test-token', undefined, async (url, options) => {
    calls.push({ url, options });
    if (url.hostname === 'ghcr.io') return { status: 307, headers: { location: 'https://pkg-containers.githubusercontent.com/opaque?signature=fixture' }, body: Buffer.alloc(0) };
    assert.equal(url.hostname, 'pkg-containers.githubusercontent.com');
    assert.equal(options.headers, undefined);
    await options.sink(payload);
    return { status: 200, headers: {}, body: Buffer.alloc(0) };
  });
  client.bearer = 'registry-secret'; client.refreshAt = Date.now() + 60000;
  const chunks = [];
  await client.blob(ref, payload.length, async bytes => { chunks.push(bytes); });
  assert.deepEqual(Buffer.concat(chunks), payload);
  assert.equal(calls.length, 2);
});

test('cancellation stops filesystem enumeration before any network upload', async t => {
  const root = await temporary(t), controller = new AbortController();
  await fs.writeFile(path.join(root, 'payload'), 'bytes');
  controller.abort();
  await assert.rejects(transfer.collectFiles('payload', root, controller.signal), /NVKR-E-TIMEOUT/);
});

test('finalizer recovery retains every successful valid carrier and still fails for missing or corrupt carriers', async t => {
  const root = await temporary(t), storage = new MemoryRegistry(), client = storage.client();
  await fs.writeFile(path.join(root, 'a.bin'), 'first bytes');
  await fs.writeFile(path.join(root, 'b.bin'), 'second bytes');
  await client.admit(); await client.authenticate(true);
  const first = await transfer.upload(client, scope, 'first', 'a.bin', root);
  const second = await transfer.upload(client, scope, 'second', 'b.bin', root);
  const selected = transfer.selectIndexes(needs({ first, second }), ['first', 'missing', 'second'], scope, true);
  storage.corruptDigests.add(transfer.digest(Buffer.from('first bytes')));
  await assert.rejects(transfer.recover(client, scope, ['first', 'missing', 'second'], selected, 'recovered', root), /NVKR-E-VERIFY-FAILED/);
  await assert.rejects(fs.stat(path.join(root, 'recovered', 'a.bin')), { code: 'ENOENT' });
  assert.equal(await fs.readFile(path.join(root, 'recovered', 'b.bin'), 'utf8'), 'second bytes');
  assert.equal(transfer.selectIndexes(needs({ first }).replace('success', 'failure'), ['first'], scope, true).size, 0);
});

test('ordinary jobs cannot enable partial recovery', async t => {
  const root = await temporary(t);
  await assert.rejects(transfer.run({ ...env, RUNNER_TEMP: root, INPUT_MODE: 'download', INPUT_RECOVERY: 'true' }), /NVKR-E-ENGINE-CAPABILITY/);
});

test('process boundary never logs credentials, signed paths or raw filesystem errors', async t => {
  const root = await temporary(t), secret = 'fixture-sensitive-secret';
  for (const input of [
    { INPUT_NAME: '', INPUT_PATTERN: '' },
    { INPUT_NAME: '{repeat,repeat}' },
    { INPUT_NAME: 'payload', GITHUB_WORKSPACE: path.join(root, 'signed-path?signature=' + secret) },
  ]) {
    const result = spawnSync(process.execPath, [path.join(__dirname, 'github_transfer_action.js')], {
      env: { ...env, RUNNER_TEMP: root, GITHUB_WORKSPACE: root, INPUT_MODE: 'upload',
        INPUT_REGISTRY: registry.registry, INPUT_USERNAME: secret, INPUT_TOKEN: secret, ...input },
      encoding: 'utf8', timeout: 5000,
    });
    assert.equal(result.error, undefined);
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /^\[NVKR-E-(USAGE|UNAVAILABLE)\] [^\n]+\n$/);
    assert.ok(!result.stderr.includes(secret));
    assert.ok(!result.stderr.includes(root));
    assert.ok(!result.stderr.includes('signature='));
  }
});

test('600-producer dependency file delivers exact carrier and bounded recovery outcomes without large environment strings', async t => {
  const root = await temporary(t), storage = new MemoryRegistry(), client = storage.client();
  await fs.writeFile(path.join(root, 'payload'), 'large join helper');
  await client.admit(); await client.authenticate(true);
  const ref = await transfer.upload(client, scope, 'helper', 'payload', root);
  const dependencies = {};
  for (let i = 0; i < 600; i++) dependencies['producer-' + i] = { result: 'success', outputs: {
    'nvkr-transfer-index': JSON.stringify({ version: 1, scope, entries: { [i === 0 ? 'helper' : 'carrier-' + i]: i === 0 ? ref : digest } }),
  } };
  const serialized = JSON.stringify(dependencies, null, 2);
  assert.ok(Buffer.byteLength(serialized) > 128 * 1024);
  const file = path.join(root, 'dependencies.json'), output = path.join(root, 'github-output');
  await fs.writeFile(file, serialized, { mode: 0o600 }); await fs.writeFile(output, '');
  const common = { ...env, GITHUB_JOB: 'nvkr-lifecycle-run', GITHUB_WORKSPACE: root, RUNNER_TEMP: root, GITHUB_OUTPUT: output,
    'INPUT_EXPECTED-INDEX-FILE': file, INPUT_REGISTRY: registry.registry, INPUT_USERNAME: 'transfer-user', INPUT_TOKEN: 'dedicated-test-token' };
  for (const [name, value] of Object.entries(common)) assert.ok(Buffer.byteLength(name + '=' + value) < 128 * 1024);
  await transfer.run({ ...common, INPUT_MODE: 'download', INPUT_NAME: 'helper', INPUT_PATH: 'restored' }, storage.send.bind(storage));
  assert.equal(await fs.readFile(path.join(root, 'restored', 'payload'), 'utf8'), 'large join helper');
  // Exercise the actual child-process boundary, with only a small path in INPUT.
  const result = spawnSync(process.execPath, [path.join(__dirname, 'github_transfer_action.js')], {
    env: { ...common, INPUT_MODE: 'results' }, encoding: 'utf8', timeout: 5000,
  });
  assert.equal(result.error, undefined); assert.equal(result.status, 0, result.stderr);
  const projected = (await fs.readFile(output, 'utf8')).trim().slice('results='.length);
  assert.ok(Buffer.byteLength(projected) < 120 * 1024);
  const outcomes = transfer.parseJSON(projected);
  assert.equal(Object.keys(outcomes).length, 600);
  for (const outcome of Object.values(outcomes)) assert.deepEqual(outcome, { result: 'success' });
});

test('dependency file admission refuses symlinks, escape, conflicting sources and excessive bytes', async t => {
  const root = await temporary(t), outside = await temporary(t), file = path.join(root, 'needs.json');
  await fs.writeFile(file, needs({ helper: digest }));
  await fs.symlink(file, path.join(root, 'link'));
  await fs.writeFile(path.join(outside, 'needs.json'), needs({ helper: digest }));
  const read = (file, raw = '') => transfer.readExpectedIndex({ RUNNER_TEMP: root }, name => ({ 'expected-index-file': file, 'expected-index': raw }[name] || ''));
  await assert.rejects(read(path.join(root, 'link')), /NVKR-E-VERIFY-FAILED/);
  await assert.rejects(read(path.join(outside, 'needs.json')), /NVKR-E-VERIFY-FAILED/);
  await assert.rejects(read(file, '{}'), /NVKR-E-USAGE/);
  await assert.rejects(read(''), /NVKR-E-USAGE/);
  await fs.writeFile(file, Buffer.alloc(transfer.LIMIT.metadata + 1));
  await assert.rejects(read(file), /NVKR-E-VERIFY-FAILED/);
  for (const result of ['unknown', 'success\nattack']) assert.throws(() => transfer.projectResults(JSON.stringify({ job: { result, outputs: {} } })), /NVKR-E-VERIFY-FAILED/);
  await assert.rejects(transfer.run({ ...env, RUNNER_TEMP: root, INPUT_MODE: 'results' }), /NVKR-E-ENGINE-CAPABILITY/);
});
