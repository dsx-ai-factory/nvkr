// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

'use strict';

// This source is copied into generated node24 actions. It has no npm dependency,
// does not execute received bytes, and never reads ambient registry credentials.
// Transport digests bind exact bytes; they confer no build or release authority.
const fs = require('node:fs');
const fsp = fs.promises;
const path = require('node:path');
const https = require('node:https');
const crypto = require('node:crypto');
const { once } = require('node:events');

const LIMIT = Object.freeze({
  chunk: 1024 * 1024 * 1024,
  total: 256 * 1024 * 1024 * 1024,
  metadata: 2 * 1024 * 1024,
  files: 4096,
  layers: 8192,
  requestMS: 9 * 60 * 1000,
  idleMS: 30 * 1000,
  operationMS: 55 * 60 * 1000,
});
const MANIFEST_TYPE = 'application/vnd.oci.image.manifest.v1+json';
const CONFIG_TYPE = 'application/vnd.nvkr.transfer.v1+json';
const LAYER_TYPE = 'application/octet-stream';
const DIGEST = /^sha256:[0-9a-f]{64}$/;
const NAME = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,199}$/;
const SEGMENT = /^[a-z0-9]+(?:[._-][a-z0-9]+)*$/;

function fail(message, code = 'NVKR-E-VERIFY-FAILED') {
  const err = new Error(`[${code}] ${message}`);
  err.code = code;
  throw err;
}

function digest(bytes) {
  return 'sha256:' + crypto.createHash('sha256').update(bytes).digest('hex');
}

function checkAbort(signal) {
  if (signal?.aborted) fail('private carrier transfer was canceled', 'NVKR-E-TIMEOUT');
}

function object(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function fields(value, expected) {
  return object(value) && Object.keys(value).sort().join('\0') === [...expected].sort().join('\0');
}

// Reject duplicate keys, excessive nesting and malformed JSON before parsing.
// JSON.parse alone silently accepts a duplicate producer, digest or scope field.
function parseJSON(input) {
  const raw = Buffer.isBuffer(input) ? input.toString('utf8') : input;
  if (typeof raw !== 'string' || Buffer.byteLength(raw) > LIMIT.metadata || raw.includes('\ufffd')) fail('invalid transfer metadata');
  let pos = 0, tokens = 0;
  const space = () => { while (pos < raw.length && /[ \r\n\t]/.test(raw[pos])) pos++; };
  const string = () => {
    const start = pos++;
    while (pos < raw.length) {
      if (raw[pos] === '\\') { pos += 2; continue; }
      if (raw[pos++] === '"') {
        try { return JSON.parse(raw.slice(start, pos)); } catch { fail('invalid transfer metadata string'); }
      }
    }
    fail('unterminated transfer metadata string');
  };
  const value = (depth) => {
    if (depth > 32 || ++tokens > 131072) fail('transfer metadata exceeds its complexity bound');
    space();
    if (raw[pos] === '"') { string(); return; }
    if (raw[pos] === '{' || raw[pos] === '[') {
      const map = raw[pos++] === '{', end = map ? '}' : ']', seen = new Set();
      space();
      if (raw[pos] === end) { pos++; return; }
      while (pos < raw.length) {
        if (map) {
          space();
          if (raw[pos] !== '"') fail('invalid transfer metadata key');
          const key = string();
          if (seen.has(key) || ['__proto__', 'prototype', 'constructor'].includes(key)) fail('duplicate or unsafe transfer metadata key');
          seen.add(key);
          space();
          if (raw[pos++] !== ':') fail('invalid transfer metadata object');
        }
        value(depth + 1);
        space();
        if (raw[pos] === end) { pos++; return; }
        if (raw[pos++] !== ',') fail('invalid transfer metadata collection');
      }
      fail('unterminated transfer metadata collection');
    }
    const match = /^(?:true|false|null|-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)/.exec(raw.slice(pos));
    if (!match) fail('invalid transfer metadata value');
    pos += match[0].length;
  };
  value(0); space();
  if (pos !== raw.length) fail('trailing transfer metadata');
  try { return JSON.parse(raw); } catch { fail('invalid transfer metadata'); }
}

function scopeFrom(env) {
  const repository = env.GITHUB_REPOSITORY;
  const run = env.GITHUB_RUN_ID;
  const attempt = env.GITHUB_RUN_ATTEMPT;
  const workflow = env.GITHUB_WORKFLOW_REF;
  const ref = env.GITHUB_REF;
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository || '') ||
      !/^[1-9][0-9]{0,19}$/.test(run || '') || !/^[1-9][0-9]{0,9}$/.test(attempt || '') ||
      typeof workflow !== 'string' || workflow.length > 1024 || /[\x00-\x20\x7f]/.test(workflow) ||
      !workflow.startsWith(repository + '/.github/workflows/') || typeof ref !== 'string' || !ref.startsWith('refs/heads/') ||
      !workflow.endsWith('@' + ref) || env.GITHUB_ACTIONS !== 'true' || !['workflow_dispatch', 'push'].includes(env.GITHUB_EVENT_NAME)) {
    fail('private transfer requires an exact trusted branch workflow attempt', 'NVKR-E-ENGINE-CAPABILITY');
  }
  return { repository, run, attempt, workflow };
}

function sameScope(actual, expected) {
  return fields(actual, ['repository', 'run', 'attempt', 'workflow']) &&
    Object.keys(expected).every(key => actual[key] === expected[key]);
}

function namesFrom(raw) {
  if (typeof raw !== 'string' || raw.length > 32768) fail('invalid carrier selection', 'NVKR-E-USAGE');
  const names = raw.startsWith('{') && raw.endsWith('}') ? raw.slice(1, -1).split(',') : [raw];
  if (!names.length || names.length > LIMIT.files || names.some(name => !NAME.test(name)) || new Set(names).size !== names.length) {
    fail('carrier selection must contain unique exact names', 'NVKR-E-USAGE');
  }
  return names.sort();
}

function registryFrom(raw) {
  if (typeof raw !== 'string' || raw.length > 512 || !raw.startsWith('ghcr.io/')) fail('private transfer requires a canonical GHCR package', 'NVKR-E-USAGE');
  const pieces = raw.slice(8).split('/');
  if (pieces.length < 2 || pieces.some(piece => !SEGMENT.test(piece))) fail('invalid GHCR package path', 'NVKR-E-USAGE');
  return { registry: raw, owner: pieces[0], package: pieces.slice(1).join('/'), repository: pieces.join('/') };
}

function safeMember(member) {
  return typeof member === 'string' && Buffer.byteLength(member) <= 1024 && member !== '' &&
    !member.startsWith('/') && !member.includes('\\') && !/[\x00-\x1f\x7f]/.test(member) &&
    member.split('/').every(part => part !== '' && part !== '.' && part !== '..');
}

function transferTag(scope, name) {
  return 'nvkr-' + digest(Buffer.from(JSON.stringify({ scope, name }))).slice(7);
}

function annotations(scope, name) {
  return {
    'dev.nvkr.transfer.repository': scope.repository,
    'dev.nvkr.transfer.run': scope.run,
    'dev.nvkr.transfer.attempt': scope.attempt,
    'dev.nvkr.transfer.workflow': scope.workflow,
    'dev.nvkr.transfer.name': name,
  };
}

function selectIndexes(raw, names, scope, recovery = false) {
  const needs = parseJSON(raw);
  if (!object(needs)) fail('carrier indices require the GitHub needs object');
  const selected = new Map();
  for (const job of Object.values(needs)) {
    if (!object(job) || !object(job.outputs)) fail('invalid producer result');
    const rawIndex = job.outputs['nvkr-transfer-index'];
    if (rawIndex === undefined || rawIndex === '') continue;
    if (typeof rawIndex !== 'string') fail('invalid producer transfer index');
    const index = parseJSON(rawIndex);
    if (!fields(index, ['version', 'scope', 'entries']) || index.version !== 1 || !sameScope(index.scope, scope) || !object(index.entries)) {
      fail('producer transfer index is outside this exact workflow attempt');
    }
    for (const [name, ref] of Object.entries(index.entries)) {
      if (!NAME.test(name) || typeof ref !== 'string' || !DIGEST.test(ref)) fail('invalid immutable carrier index');
      if (!names.includes(name)) continue;
      if (job.result !== 'success') {
        if (recovery && ['failure', 'cancelled', 'skipped'].includes(job.result)) continue;
        fail('required carrier producer did not succeed');
      }
      if (selected.has(name)) fail('carrier has ambiguous producer indices');
      selected.set(name, ref);
    }
  }
  if (!recovery && selected.size !== names.length) fail('required immutable carrier index is missing');
  return selected;
}

// Each request owns both its upload and response stream through completion. No
// response body, signed redirect URL or remote exception text reaches CI logs.
async function request(url, options = {}) {
  const { method = 'GET', headers = {}, body, sink, maxBytes = LIMIT.metadata, signal } = options;
  if (url.protocol !== 'https:' || url.username || url.password || url.hash) fail('unsafe transfer URL');
  return new Promise((resolve, reject) => {
    let settled = false, writer, timedOut = false;
    const req = https.request(url, { method, headers, signal });
    const timer = setTimeout(() => { timedOut = true; req.destroy(new Error('deadline')); }, LIMIT.requestMS);
    req.setTimeout(LIMIT.idleMS, () => { timedOut = true; req.destroy(new Error('idle timeout')); });
    const finish = async (error, result) => {
      if (settled) return;
      settled = true;
      if (error) req.destroy();
      try { await writer; } catch { error ||= new Error('request body failed'); }
      clearTimeout(timer);
      if (error) reject(new Error(timedOut || signal?.aborted ? '[NVKR-E-TIMEOUT] private registry request timed out or was canceled' : '[NVKR-E-UNAVAILABLE] bounded private registry request failed'));
      else resolve(result);
    };
    req.on('error', error => {
      if (body && !Buffer.isBuffer(body) && typeof body.destroy === 'function') body.destroy(error);
      void finish(error);
    });
    req.on('response', response => {
      // A final response before the request body completed is an early rejection.
      // Stop and join the writer; do not clear its deadline while it can block.
      if (!req.writableFinished) {
        response.destroy();
        req.destroy(new Error('early registry response'));
        return;
      }
      void (async () => {
        let size = 0;
        const chunks = [];
        // Error/redirect responses are metadata, never payload written to disk.
        const acceptSink = response.statusCode === 200 && sink;
        const bound = acceptSink ? maxBytes : LIMIT.metadata;
        for await (const chunk of response) {
          size += chunk.length;
          if (size > bound) { response.destroy(); throw new Error('response bound'); }
          if (acceptSink) await sink(chunk);
          else chunks.push(chunk);
        }
        await finish(null, { status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks), size });
      })().catch(error => { void finish(error); });
    });
    writer = (async () => {
      if (body !== undefined) {
        if (Buffer.isBuffer(body)) req.end(body);
        else {
          for await (const chunk of body) {
            if (!req.write(chunk)) await once(req, 'drain');
          }
          req.end();
        }
      } else req.end();
    })();
    writer.catch(error => { req.destroy(error); });
  });
}

function expect(response, allowed, context) {
  if (!allowed.includes(response.status)) fail(`${context} rejected by private registry (HTTP ${response.status})`, 'NVKR-E-UNAVAILABLE');
  return response;
}

function validatePackage(pkg, registry) {
  if (!object(pkg) || !Number.isSafeInteger(pkg.id) || pkg.id <= 0 || pkg.name !== registry.package ||
      pkg.package_type !== 'container' || !object(pkg.owner) || typeof pkg.owner.login !== 'string' || pkg.owner.login.toLowerCase() !== registry.owner ||
      !['private', 'internal'].includes(pkg.visibility) || (pkg.repository !== undefined && pkg.repository !== null)) {
    fail('transfer package must pre-exist, be private/internal, and have no linked repository', 'NVKR-E-TRUST-POLICY-MISSING');
  }
  return pkg.id;
}

class Registry {
  constructor(registry, username, token, signal, send = request) {
    if (typeof username !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9-]{0,99}$/.test(username) ||
        typeof token !== 'string' || token.length < 8 || token.length > 4096 || /[\x00-\x20\x7f]/.test(token)) {
      fail('private transfer requires dedicated explicit package credentials', 'NVKR-E-TRUST-POLICY-MISSING');
    }
    this.registry = registry;
    this.username = username;
    this.token = token;
    this.signal = signal;
    this.send = send;
  }

  async admit() {
    const headers = { authorization: `Bearer ${this.token}`, accept: 'application/vnd.github+json',
      'user-agent': 'nvkr-private-transfer', 'x-github-api-version': '2026-03-10' };
    const suffix = `${this.registry.owner}/packages/container/${encodeURIComponent(this.registry.package)}`;
    let response = await this.send(new URL(`https://api.github.com/orgs/${suffix}`), { headers, signal: this.signal });
    // Organization packages require one REST admission request per operation.
    // A user-owned package is tried only on definitive absence, never on 403,
    // throttling, timeout or another inconclusive organization response.
    if (response.status === 404) response = await this.send(new URL(`https://api.github.com/users/${suffix}`), { headers, signal: this.signal });
    const pkg = parseJSON(expect(response, [200], 'private package admission').body);
    const id = validatePackage(pkg, this.registry);
    if (this.packageID !== undefined && id !== this.packageID) fail('private package identity changed during transfer');
    this.packageID = id;
    // GitHub's documented API does not expose inheritance or Manage Actions
    // access. Provisioning must separately remove the public repository's Actions
    // access and grant only the dedicated transfer principals. Visibility and the
    // absence of a linked repository are checked here, never inferred from a URL.
  }

  async authenticate(write) {
    checkAbort(this.signal);
    const url = new URL('https://ghcr.io/token');
    url.searchParams.set('service', 'ghcr.io');
    url.searchParams.set('scope', `repository:${this.registry.repository}:${write ? 'pull,push' : 'pull'}`);
    const response = expect(await this.send(url, { headers: {
      authorization: 'Basic ' + Buffer.from(`${this.username}:${this.token}`).toString('base64'),
      'user-agent': 'nvkr-private-transfer',
    }, signal: this.signal }), [200], 'registry authentication');
    const result = parseJSON(response.body);
    if (!object(result) || typeof result.token !== 'string' || result.token.length === 0 || result.token.length > 32768 || /[\x00-\x20\x7f]/.test(result.token)) fail('invalid registry token');
    this.bearer = result.token;
    this.write = write;
    // OCI token responses may omit expires_in (the protocol default is 60s).
    // Refresh before the next request, without retrying any ambiguous mutation.
    const lifetime = Number.isSafeInteger(result.expires_in) && result.expires_in > 0 ? Math.min(result.expires_in, 300) : 60;
    this.refreshAt = Date.now() + Math.max(1, lifetime - 15) * 1000;
  }

  async call(suffix, options = {}) {
    checkAbort(this.signal);
    if (!this.bearer || Date.now() >= this.refreshAt) await this.authenticate(this.write === true);
    return this.send(new URL(`https://ghcr.io/v2/${this.registry.repository}/${suffix}`), {
      ...options, signal: this.signal,
      headers: { 'user-agent': 'nvkr-private-transfer', authorization: `Bearer ${this.bearer}`, ...options.headers },
    });
  }

  async uploadBlob(ref, size, body) {
    const found = await this.call(`blobs/${ref}`, { method: 'HEAD' });
    if (found.status === 200) {
      if (found.headers['docker-content-digest'] !== ref || Number(found.headers['content-length']) !== size) fail('existing registry blob differs from exact bytes');
      return;
    }
    expect(found, [404], 'blob existence check');
    const start = expect(await this.call('blobs/uploads/', { method: 'POST', headers: { 'content-length': '0' } }), [202], 'blob upload admission');
    let location;
    try { location = new URL(start.headers.location, 'https://ghcr.io'); } catch { fail('invalid registry upload location'); }
    if (location.origin !== 'https://ghcr.io' || location.username || location.password || location.hash ||
        !location.pathname.startsWith(`/v2/${this.registry.repository}/blobs/uploads/`)) fail('registry upload redirects outside the admitted package');
    location.searchParams.set('digest', ref);
    const response = expect(await this.send(location, { method: 'PUT', body: typeof body === 'function' ? body() : body, signal: this.signal,
      headers: { authorization: `Bearer ${this.bearer}`, 'user-agent': 'nvkr-private-transfer', 'content-type': LAYER_TYPE, 'content-length': String(size) } }), [201], 'blob publication');
    if (response.headers['docker-content-digest'] !== ref) fail('registry acknowledged another blob digest');
  }

  async blob(ref, size, sink) {
    if (!DIGEST.test(ref) || !Number.isSafeInteger(size) || size < 0 || size > LIMIT.chunk) fail('invalid carrier blob descriptor');
    const hash = crypto.createHash('sha256');
    let received = 0;
    const consume = async bytes => { received += bytes.length; hash.update(bytes); await sink(bytes); };
    let response = await this.call(`blobs/${ref}`, { maxBytes: size, sink: consume });
    if ([302, 307].includes(response.status)) {
      let url;
      try { url = new URL(response.headers.location); } catch { fail('invalid registry blob redirect'); }
      if (url.protocol !== 'https:' || url.hostname !== 'pkg-containers.githubusercontent.com' || url.port || url.username || url.password || url.hash) fail('registry blob redirect is outside its admitted CDN');
      // A signed CDN URL receives no PAT, registry bearer or ambient credentials.
      response = await this.send(url, { maxBytes: size, sink: consume, signal: this.signal });
    }
    expect(response, [200], 'immutable blob download');
    if (received !== size || 'sha256:' + hash.digest('hex') !== ref) fail('received carrier blob does not match its immutable descriptor');
  }

  async metadataBlob(descriptor) {
    if (descriptor.size > LIMIT.metadata) fail('transfer configuration exceeds its size bound');
    const chunks = [];
    await this.blob(descriptor.digest, descriptor.size, async bytes => { chunks.push(bytes); });
    return parseJSON(Buffer.concat(chunks));
  }
}

function descriptorValid(value, mediaType, max) {
  return fields(value, ['mediaType', 'digest', 'size']) && value.mediaType === mediaType && DIGEST.test(value.digest) &&
    Number.isSafeInteger(value.size) && value.size >= 0 && value.size <= max;
}

function validateManifest(raw, expectedDigest, scope, name) {
  if (digest(raw) !== expectedDigest) fail('carrier manifest differs from producer index');
  const manifest = parseJSON(raw);
  const expected = annotations(scope, name);
  if (!fields(manifest, ['schemaVersion', 'mediaType', 'artifactType', 'config', 'layers', 'annotations']) ||
      manifest.schemaVersion !== 2 || manifest.mediaType !== MANIFEST_TYPE || manifest.artifactType !== CONFIG_TYPE ||
      !fields(manifest.annotations, Object.keys(expected)) || Object.entries(expected).some(([key, value]) => manifest.annotations[key] !== value) ||
      !descriptorValid(manifest.config, CONFIG_TYPE, LIMIT.metadata) || !Array.isArray(manifest.layers) ||
      manifest.layers.length > LIMIT.layers || manifest.layers.some(layer => !descriptorValid(layer, LAYER_TYPE, LIMIT.chunk))) {
    fail('carrier manifest does not bind this exact attempt and artifact');
  }
  return manifest;
}

function validateConfig(config, manifest, scope, name) {
  if (!fields(config, ['version', 'scope', 'name', 'files']) || config.version !== 1 || !sameScope(config.scope, scope) || config.name !== name ||
      !Array.isArray(config.files) || config.files.length === 0 || config.files.length > LIMIT.files) fail('invalid exact carrier configuration');
  const paths = new Set(), layers = [];
  let total = 0, previous = '';
  for (const file of config.files) {
    if (!fields(file, ['path', 'size', 'digest', 'chunks']) || !safeMember(file.path) || paths.has(file.path) || file.path <= previous ||
        !Number.isSafeInteger(file.size) || file.size < 0 || file.size > LIMIT.total || !DIGEST.test(file.digest) ||
        !Array.isArray(file.chunks) || file.chunks.length !== Math.ceil(file.size / LIMIT.chunk)) fail('invalid carrier file membership');
    previous = file.path;
    for (const seen of paths) if (seen.startsWith(file.path + '/') || file.path.startsWith(seen + '/')) fail('conflicting carrier file membership');
    paths.add(file.path);
    let size = 0;
    for (let i = 0; i < file.chunks.length; i++) {
      const chunk = file.chunks[i];
      if (!fields(chunk, ['digest', 'size']) || !DIGEST.test(chunk.digest) || !Number.isSafeInteger(chunk.size) ||
          chunk.size <= 0 || chunk.size > LIMIT.chunk || (i + 1 < file.chunks.length && chunk.size !== LIMIT.chunk)) fail('invalid carrier chunk');
      size += chunk.size;
      layers.push({ mediaType: LAYER_TYPE, digest: chunk.digest, size: chunk.size });
    }
    if (size !== file.size) fail('carrier chunks do not cover exact file bytes');
    total += size;
    if (total > LIMIT.total || layers.length > LIMIT.layers) fail('carrier exceeds its total size bound');
  }
  if (JSON.stringify(layers) !== JSON.stringify(manifest.layers)) fail('carrier contains missing, extra or reordered blobs');
  return config.files;
}

async function checkedPath(root, target, allowMissing = false) {
  const absolute = path.resolve(root, target);
  const relative = path.relative(root, absolute);
  if (relative === '..' || relative.startsWith('..' + path.sep) || path.isAbsolute(relative)) fail('transfer path escapes its admitted root');
  let current = root;
  for (const part of relative.split(path.sep).filter(Boolean)) {
    current = path.join(current, part);
    try {
      const stat = await fsp.lstat(current);
      if (stat.isSymbolicLink()) fail('transfer paths cannot contain symbolic links');
    } catch (error) {
      if (allowMissing && error.code === 'ENOENT') continue;
      throw error;
    }
  }
  return absolute;
}

async function collectFiles(raw, workspace, signal) {
  if (typeof raw !== 'string' || Buffer.byteLength(raw) > 32768) fail('invalid transfer path selection', 'NVKR-E-USAGE');
  const selected = raw.split(/\r?\n/).map(item => item.trim()).filter(Boolean);
  if (!selected.length || selected.length > LIMIT.files || selected.some(item => /[*?{}\[\]\x00]/.test(item))) fail('upload requires exact file or directory paths', 'NVKR-E-USAGE');
  const roots = [];
  for (const item of selected) {
    checkAbort(signal);
    const absolute = await checkedPath(workspace, item);
    if (absolute === workspace) fail('uploading the complete workspace is not admitted', 'NVKR-E-USAGE');
    const stat = await fsp.lstat(absolute);
    if (!stat.isDirectory() && !stat.isFile()) fail('carrier includes a non-regular filesystem object');
    roots.push({ absolute, directory: stat.isDirectory() });
  }
  let base = roots.length === 1 && roots[0].directory ? roots[0].absolute : path.dirname(roots[0].absolute);
  for (const item of roots) while (path.relative(base, item.absolute).startsWith('..' + path.sep) || path.relative(base, item.absolute) === '..') base = path.dirname(base);
  const files = new Map();
  let total = 0, visited = 0;
  async function walk(absolute) {
    checkAbort(signal);
    if (++visited > LIMIT.files * 2 || path.relative(base, absolute).split(path.sep).length > 64) fail('carrier tree exceeds its complexity bound');
    const stat = await fsp.lstat(absolute);
    if (stat.isSymbolicLink()) fail('carrier cannot contain symbolic links');
    if (stat.isDirectory()) {
      const names = (await fsp.readdir(absolute)).sort();
      if (names.length > LIMIT.files) fail('carrier directory exceeds its entry bound');
      for (const name of names) await walk(path.join(absolute, name));
      return;
    }
    if (!stat.isFile() || !Number.isSafeInteger(stat.size)) fail('carrier requires regular files');
    const member = path.relative(base, absolute).split(path.sep).join('/');
    if (!safeMember(member) || files.has(member)) fail('carrier path selection overlaps or is unsafe');
    files.set(member, absolute);
    total += stat.size;
    if (files.size > LIMIT.files || total > LIMIT.total) fail('carrier exceeds its filesystem bound');
  }
  for (const root of roots) await walk(root.absolute);
  if (!files.size) fail('carrier has no regular files');
  return [...files.entries()].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0);
}

function unchanged(before, after) {
  return ['dev', 'ino', 'size', 'mtimeNs', 'ctimeNs'].every(key => before[key] === after[key]);
}

async function uploadFile(registry, member, absolute) {
  const handle = await fsp.open(absolute, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
  try {
    const stat = await handle.stat({ bigint: true });
    if (!stat.isFile() || stat.size > BigInt(LIMIT.total)) fail('invalid held carrier file');
    const size = Number(stat.size), whole = crypto.createHash('sha256'), chunks = [];
    for (let offset = 0; offset < size; offset += LIMIT.chunk) {
      checkAbort(registry.signal);
      const length = Math.min(LIMIT.chunk, size - offset), hash = crypto.createHash('sha256');
      for await (const bytes of handle.createReadStream({ start: offset, end: offset + length - 1, autoClose: false })) {
        checkAbort(registry.signal);
        whole.update(bytes); hash.update(bytes);
      }
      const ref = 'sha256:' + hash.digest('hex');
      await registry.uploadBlob(ref, length, () => handle.createReadStream({ start: offset, end: offset + length - 1, autoClose: false }));
      chunks.push({ digest: ref, size: length });
    }
    if (!unchanged(stat, await handle.stat({ bigint: true }))) fail('held carrier file changed during upload');
    return { path: member, size, digest: 'sha256:' + whole.digest('hex'), chunks };
  } finally { await handle.close(); }
}

async function upload(registry, scope, name, rawPath, workspace) {
  const files = await collectFiles(rawPath, workspace, registry.signal);
  const tag = transferTag(scope, name);
  const exists = await registry.call(`manifests/${tag}`, { method: 'HEAD', headers: { accept: MANIFEST_TYPE } });
  if (exists.status === 200) fail('carrier name already exists in this exact attempt', 'NVKR-E-IMMUTABLE-VIOLATION');
  expect(exists, [404], 'carrier name reservation');
  const contents = [];
  for (const [member, absolute] of files) contents.push(await uploadFile(registry, member, absolute));
  const config = Buffer.from(JSON.stringify({ version: 1, scope, name, files: contents }));
  if (config.length > LIMIT.metadata) fail('carrier configuration exceeds its bound');
  const configDigest = digest(config);
  await registry.uploadBlob(configDigest, config.length, config);
  const layers = contents.flatMap(file => file.chunks.map(chunk => ({ mediaType: LAYER_TYPE, ...chunk })));
  if (layers.length > LIMIT.layers) fail('carrier has too many chunks');
  const manifest = Buffer.from(JSON.stringify({ schemaVersion: 2, mediaType: MANIFEST_TYPE, artifactType: CONFIG_TYPE,
    config: { mediaType: CONFIG_TYPE, digest: configDigest, size: config.length }, layers, annotations: annotations(scope, name) }));
  if (manifest.length > LIMIT.metadata) fail('carrier manifest exceeds its bound');
  const ref = digest(manifest);
  for (const destination of [ref, tag]) {
    const response = expect(await registry.call(`manifests/${destination}`, { method: 'PUT', body: manifest,
      headers: { 'content-type': MANIFEST_TYPE, 'content-length': String(manifest.length) } }), [201], 'carrier manifest publication');
    if (response.headers['docker-content-digest'] !== ref) fail('registry acknowledged another carrier manifest');
  }
  const readback = expect(await registry.call(`manifests/${ref}`, { headers: { accept: MANIFEST_TYPE } }), [200], 'carrier publication readback');
  validateManifest(readback.body, ref, scope, name);
  return ref;
}

async function mkdirChecked(workspace, destination) {
  const absolute = await checkedPath(workspace, destination, true);
  const relative = path.relative(workspace, absolute);
  let current = workspace;
  for (const part of relative.split(path.sep).filter(Boolean)) {
    current = path.join(current, part);
    try { await fsp.mkdir(current, { mode: 0o700 }); } catch (error) { if (error.code !== 'EEXIST') throw error; }
    const stat = await fsp.lstat(current);
    if (!stat.isDirectory() || stat.isSymbolicLink()) fail('unsafe transfer destination directory');
  }
  return absolute;
}

async function writeAll(handle, bytes) {
  let position = 0;
  while (position < bytes.length) {
    const { bytesWritten } = await handle.write(bytes, position, bytes.length - position);
    if (!bytesWritten) fail('carrier destination stopped accepting bytes');
    position += bytesWritten;
  }
}

async function download(registry, scope, names, index, destination, workspace) {
  const selected = [], members = new Set();
  let total = 0;
  // Admit every exact manifest and destination before creating output files.
  for (const name of names) {
    checkAbort(registry.signal);
    const ref = index.get(name);
    const response = expect(await registry.call(`manifests/${ref}`, { headers: { accept: MANIFEST_TYPE } }), [200], 'immutable carrier manifest download');
    const manifest = validateManifest(response.body, ref, scope, name);
    const config = await registry.metadataBlob(manifest.config);
    for (const file of validateConfig(config, manifest, scope, name)) {
      if (members.has(file.path) || [...members].some(member => member.startsWith(file.path + '/') || file.path.startsWith(member + '/'))) fail('selected carriers have colliding file paths');
      members.add(file.path);
      total += file.size;
      if (members.size > LIMIT.files || total > LIMIT.total) fail('selected carriers exceed the aggregate transfer bound');
      selected.push(file);
    }
  }
  const root = await mkdirChecked(workspace, destination);
  for (const file of selected) {
    await checkedPath(root, file.path, true);
    try { await fsp.lstat(path.join(root, file.path)); fail('carrier destination already exists', 'NVKR-E-IMMUTABLE-VIOLATION'); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  const stage = await fsp.mkdtemp(path.join(root, '.nvkr-transfer-'));
  const committed = [];
  try {
    for (let i = 0; i < selected.length; i++) {
      checkAbort(registry.signal);
      const file = selected[i], target = path.join(stage, String(i));
      const handle = await fsp.open(target, fs.constants.O_WRONLY | fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_NOFOLLOW, 0o600);
      try {
        const whole = crypto.createHash('sha256');
        for (const chunk of file.chunks) await registry.blob(chunk.digest, chunk.size, async bytes => { whole.update(bytes); await writeAll(handle, bytes); });
        if ('sha256:' + whole.digest('hex') !== file.digest || (await handle.stat()).size !== file.size) fail('restored file differs from exact carrier bytes');
        await handle.sync();
      } finally { await handle.close(); }
    }
    for (let i = 0; i < selected.length; i++) {
      checkAbort(registry.signal);
      const target = await checkedPath(root, selected[i].path, true);
      await mkdirChecked(root, path.dirname(target));
      // link is atomic no-replace; a concurrent existing destination is an error.
      await fsp.link(path.join(stage, String(i)), target);
      committed.push(target);
    }
  } catch (error) {
    for (const target of committed.reverse()) await fsp.unlink(target).catch(() => {});
    throw error;
  } finally { await fsp.rm(stage, { recursive: true, force: true }); }
}

// The always-run lifecycle finalizer needs all recoverable exact carriers so its
// existing outcome records can report every missing output. Partial recovery is
// diagnostic only and still returns failure; ordinary consumers never use it.
async function recover(registry, scope, names, index, destination, workspace) {
  let failed = 0;
  for (const name of names) {
    checkAbort(registry.signal);
    if (!index.has(name)) { failed++; continue; }
    try { await download(registry, scope, [name], index, destination, workspace); }
    catch (error) {
      checkAbort(registry.signal);
      if (String(error.message).startsWith('[NVKR-E-TIMEOUT]')) throw error;
      failed++;
    }
  }
  if (failed) fail('private finalizer recovery could not restore every required carrier');
}

async function indexPath(env, scope) {
  if (!env.RUNNER_TEMP || !path.isAbsolute(env.RUNNER_TEMP) || !/^[a-zA-Z0-9_-]{1,100}$/.test(env.GITHUB_JOB || '')) fail('transfer index requires provider-owned temporary storage');
  const root = await fsp.realpath(env.RUNNER_TEMP);
  const key = digest(Buffer.from(JSON.stringify({ scope, job: env.GITHUB_JOB }))).slice(7);
  return path.join(root, `nvkr-transfer-${key}.json`);
}

async function readIndex(file, scope) {
  let handle;
  try { handle = await fsp.open(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW); }
  catch (error) { if (error.code === 'ENOENT') return { version: 1, scope, entries: {} }; throw error; }
  try {
    const stat = await handle.stat();
    if (!stat.isFile() || stat.size > LIMIT.metadata) fail('invalid local transfer index');
    const index = parseJSON(await handle.readFile());
    if (!fields(index, ['version', 'scope', 'entries']) || index.version !== 1 || !sameScope(index.scope, scope) || !object(index.entries) ||
        Object.entries(index.entries).some(([name, value]) => !NAME.test(name) || !DIGEST.test(value))) fail('invalid local carrier index');
    return index;
  } finally { await handle.close(); }
}

async function recordIndex(file, scope, name, ref) {
  const index = await readIndex(file, scope);
  if (Object.hasOwn(index.entries, name)) fail('carrier index already contains this name', 'NVKR-E-IMMUTABLE-VIOLATION');
  index.entries[name] = ref;
  index.entries = Object.fromEntries(Object.entries(index.entries).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0));
  const raw = Buffer.from(JSON.stringify(index));
  if (raw.length > LIMIT.metadata) fail('local carrier index exceeds its bound');
  const temporary = file + '.' + crypto.randomBytes(12).toString('hex');
  const handle = await fsp.open(temporary, 'wx', 0o600);
  try { await handle.writeFile(raw); await handle.sync(); } finally { await handle.close(); }
  try { await fsp.rename(temporary, file); } finally { await fsp.unlink(temporary).catch(error => { if (error.code !== 'ENOENT') throw error; }); }
}

async function readExpectedIndex(env, input) {
  const inline = input('expected-index'), file = input('expected-index-file');
  if ((inline !== '') === (file !== '')) fail('provide exactly one dependency index source', 'NVKR-E-USAGE');
  if (inline !== '') return inline;
  if (!env.RUNNER_TEMP || !path.isAbsolute(env.RUNNER_TEMP) || !path.isAbsolute(file)) fail('dependency index requires provider-owned temporary storage');
  const root = await fsp.realpath(env.RUNNER_TEMP);
  const absolute = await checkedPath(root, file);
  const handle = await fsp.open(absolute, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const before = await handle.stat({ bigint: true });
    if (!before.isFile() || before.size > BigInt(LIMIT.metadata)) fail('dependency index exceeds its file bound');
    const buffer = Buffer.alloc(LIMIT.metadata + 1);
    let size = 0;
    while (size < buffer.length) {
      const { bytesRead } = await handle.read(buffer, size, buffer.length - size, size);
      if (!bytesRead) break;
      size += bytesRead;
    }
    if (size > LIMIT.metadata || !unchanged(before, await handle.stat({ bigint: true }))) fail('dependency index changed or exceeds its bound');
    return buffer.subarray(0, size);
  } finally { await handle.close(); }
}

function projectResults(raw) {
  const needs = parseJSON(raw);
  if (!object(needs) || Object.keys(needs).length > 2048) fail('invalid bounded dependency results');
  const results = {};
  for (const job of Object.keys(needs).sort()) {
    const value = needs[job];
    if (!/^[a-zA-Z0-9_-]{1,100}$/.test(job) || !object(value) || !object(value.outputs) ||
        !['success', 'failure', 'cancelled', 'skipped'].includes(value.result)) fail('invalid dependency result');
    results[job] = { result: value.result };
  }
  const encoded = JSON.stringify(results);
  // Recovery consumes this small projection through an environment variable.
  // Keep headroom below Linux MAX_ARG_STRLEN for its variable name and terminator.
  if (Buffer.byteLength(encoded) > 120 * 1024) fail('dependency results exceed their environment bound');
  return encoded;
}

async function emitOutput(env, name, raw) {
  if (!env.GITHUB_OUTPUT || !path.isAbsolute(env.GITHUB_OUTPUT)) fail('transfer output requires a GitHub output file');
  await fsp.appendFile(env.GITHUB_OUTPUT, `${name}=${raw}\n`, { encoding: 'utf8', flag: fs.constants.O_WRONLY | fs.constants.O_APPEND | fs.constants.O_NOFOLLOW });
}

async function run(env = process.env, send = request) {
  const input = name => env['INPUT_' + name.toUpperCase().replaceAll('-', '_')] || env['INPUT_' + name.toUpperCase()] || '';
  const scope = scopeFrom(env), mode = input('mode');
  if (!['upload', 'download', 'index', 'results'].includes(mode)) fail('unknown transfer operation', 'NVKR-E-USAGE');
  const recovery = input('recovery');
  if (!['', 'true', 'false'].includes(recovery) || (recovery === 'true' && env.GITHUB_JOB !== 'nvkr-lifecycle-run')) {
    fail('partial carrier recovery is limited to the lifecycle finalizer', 'NVKR-E-ENGINE-CAPABILITY');
  }
  if (mode === 'results') {
    if (env.GITHUB_JOB !== 'nvkr-lifecycle-run') fail('dependency outcome projection is limited to the lifecycle finalizer', 'NVKR-E-ENGINE-CAPABILITY');
    await emitOutput(env, 'results', projectResults(await readExpectedIndex(env, input)));
    return;
  }
  const localIndex = await indexPath(env, scope);
  if (mode === 'index') {
    const raw = JSON.stringify(await readIndex(localIndex, scope));
    // Job outputs are capped by GitHub; fail before emitting an oversized value.
    if (raw.length * 2 > 512 * 1024) fail('carrier index exceeds its GitHub job-output budget');
    await emitOutput(env, 'index', raw);
    return;
  }
  const names = namesFrom(input('name') || input('pattern'));
  if (mode === 'upload' && names.length !== 1) fail('upload requires one exact carrier name', 'NVKR-E-USAGE');
  const registry = registryFrom(input('registry'));
  const workspace = await fsp.realpath(env.GITHUB_WORKSPACE || fail('transfer requires the checked out workspace'));
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), LIMIT.operationMS);
  const stop = () => controller.abort();
  process.once('SIGTERM', stop); process.once('SIGINT', stop);
  try {
    const client = new Registry(registry, input('username'), input('token'), controller.signal, send);
    await client.admit();
    await client.authenticate(mode === 'upload');
    if (mode === 'upload') {
      const ref = await upload(client, scope, names[0], input('path'), workspace);
      await recordIndex(localIndex, scope, names[0], ref);
    } else {
      const index = selectIndexes(await readExpectedIndex(env, input), names, scope, recovery === 'true');
      await (recovery === 'true' ? recover : download)(client, scope, names, index, input('path'), workspace);
    }
  } finally {
    clearTimeout(timer);
    process.removeListener('SIGTERM', stop); process.removeListener('SIGINT', stop);
  }
}

module.exports = { LIMIT, MANIFEST_TYPE, CONFIG_TYPE, LAYER_TYPE, digest, parseJSON, scopeFrom, sameScope, namesFrom, registryFrom,
  safeMember, transferTag, annotations, selectIndexes, validatePackage, validateManifest, validateConfig, checkedPath, collectFiles,
  Registry, upload, download, recover, readIndex, recordIndex, readExpectedIndex, projectResults, request, run };

if (require.main === module) {
  run().catch(error => {
    // Arbitrary filesystem/HTTP error text may include a sensitive signed URL or
    // payload-controlled path. Only this module's fixed coded messages are shown.
    const message = typeof error.message === 'string' && /^\[NVKR-E-[A-Z-]+\]/.test(error.message) ? error.message : '[NVKR-E-UNAVAILABLE] private carrier transfer failed';
    process.stderr.write(message + '\n');
    process.exitCode = 1;
  });
}
