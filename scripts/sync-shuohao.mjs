// Read upstream Git objects as data. Never check out or execute upstream code.
import { spawnSync } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { existsSync, lstatSync, mkdirSync, readFileSync, readdirSync, renameSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repository = 'eternityspring/shuohao-skills';
const url = `https://github.com/${repository}.git`;
const digest = data => createHash('sha256').update(data).digest('hex');
const validCommit = value => /^[0-9a-f]{40}$/.test(value);
function safePath(value) {
  return typeof value === 'string' && value.length > 0 && !/[\\:\x00-\x1f]/.test(value) &&
    value.split('/').every(part => part && part !== '.' && part !== '..' && part.trim() === part &&
      !part.endsWith('.') && !/^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(part));
}
function regular(path) {
  const st = lstatSync(path);
  if (!st.isFile() || st.isSymbolicLink()) throw new Error('snapshot integrity: non-regular file');
}
function filesUnder(root, prefix = '') {
  if (lstatSync(root).isSymbolicLink()) throw new Error('snapshot integrity: linked directory');
  const result = [];
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    const name = prefix + entry.name;
    if (entry.isSymbolicLink()) throw new Error('snapshot integrity: symbolic link');
    if (entry.isDirectory()) result.push(...filesUnder(join(root, entry.name), name + '/'));
    else if (entry.isFile()) result.push(name);
    else throw new Error('snapshot integrity: special file');
  }
  return result.sort();
}
function verifySnapshot(path, manifest) {
  if (lstatSync(path).isSymbolicLink()) throw new Error('snapshot integrity: linked directory');
  const actual = filesUnder(join(path, 'upstream'));
  if (JSON.stringify(actual) !== JSON.stringify(manifest.files.map(f => f.path).sort())) {
    throw new Error('snapshot integrity: changed inventory');
  }
  for (const file of manifest.files) {
    if (!safePath(file.path)) throw new Error('snapshot integrity: path');
    const target = join(path, 'upstream', file.path);
    regular(target);
    const bytes = readFileSync(target);
    if (digest(bytes) !== file.sha256 || bytes.length !== file.size) throw new Error('snapshot integrity: changed bytes');
  }
}
export function materialize(data, commit, destination) {
  if (!validCommit(commit) || !(data instanceof Map) || data.size > 2000) throw new Error('invalid snapshot');
  const folded = new Set();
  let total = 0;
  const files = [];
  for (const [path, bytes] of data) {
    if (!safePath(path) || folded.has(path.toLowerCase()) || !Buffer.isBuffer(bytes) || bytes.length > 2 * 1024 * 1024) throw new Error('invalid upstream file');
    folded.add(path.toLowerCase());
    total += bytes.length;
    files.push({ path, size: bytes.length, sha256: digest(bytes) });
  }
  if (total > 32 * 1024 * 1024 || !data.has('LICENSE') || !data.has('NOTICE')) throw new Error('incomplete or oversized snapshot');
  files.sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
  const names = files.map(f => /^skills\/([a-z0-9]+(?:-[a-z0-9]+)*)\/SKILL\.md$/.exec(f.path)?.[1]).filter(Boolean).sort();
  if (!names.length) throw new Error('no skills found');
  for (const name of names) {
    const bundle = files.filter(f => f.path.startsWith(`skills/${name}/`));
    if (bundle.length + 2 > 128 || bundle.reduce((n, f) => n + f.size, data.get('LICENSE').length + data.get('NOTICE').length) > 8 * 1024 * 1024) throw new Error('skill exceeds product bundle limit');
    if (bundle.some(f => ['LICENSE', 'NOTICE'].includes(f.path.slice(`skills/${name}/`.length)))) throw new Error('license collision requires review');
  }
  mkdirSync(destination, { recursive: true });
  if (lstatSync(destination).isSymbolicLink()) throw new Error('linked destination');
  const target = join(destination, commit);
  if (existsSync(target)) {
    const saved = JSON.parse(readFileSync(join(target, 'manifest.json'), 'utf8'));
    verifySnapshot(target, saved);
    if (saved.repository !== repository || saved.commit !== commit || JSON.stringify(saved.files) !== JSON.stringify(files) || JSON.stringify(saved.skills) !== JSON.stringify(names)) throw new Error('snapshot integrity: manifest mismatch');
    return { changed: false, commit, skills: names };
  }
  const previous = readdirSync(destination).filter(validCommit).map(id => {
    const location = join(destination, id);
    const m = JSON.parse(readFileSync(join(location, 'manifest.json'), 'utf8'));
    verifySnapshot(location, m);
    return m;
  }).sort((a, b) => a.captured_at.localeCompare(b.captured_at)).at(-1);
  const old = new Map((previous?.files ?? []).map(f => [f.path, f.sha256]));
  const current = new Map(files.map(f => [f.path, f.sha256]));
  const diff = {
    added: files.filter(f => !old.has(f.path)).map(f => f.path),
    modified: files.filter(f => old.has(f.path) && old.get(f.path) !== f.sha256).map(f => f.path),
    removed: [...old.keys()].filter(p => !current.has(p)),
  };
  const manifest = { repository, commit, captured_at: new Date().toISOString(), previous_commit: previous?.commit ?? '', skills: names, files, diff, state: 'CANDIDATE', execution: 'NOT_EXECUTED' };
  // Incomplete writes remain hidden and never enter the embedded catalog.
  const stage = join(destination, `.partial-${randomUUID()}`);
  mkdirSync(stage);
  for (const [path, bytes] of data) {
    const output = join(stage, 'upstream', path);
    mkdirSync(dirname(output), { recursive: true });
    writeFileSync(output, bytes, { flag: 'wx' });
  }
  writeFileSync(join(stage, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n', { flag: 'wx' });
  verifySnapshot(stage, manifest);
  renameSync(stage, target);
  return { changed: true, commit, skills: names, diff };
}
function git(source, args, binary = false) {
  const result = spawnSync('git', ['-c', `safe.directory=${source.replaceAll('\\', '/')}`, '-C', source, ...args], {
    encoding: binary ? undefined : 'utf8', windowsHide: true, maxBuffer: 40 * 1024 * 1024,
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0' },
  });
  if (result.error || result.status !== 0) throw new Error(result.error?.message ?? String(result.stderr));
  return result.stdout;
}
function main() {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
  const source = resolve(root, '../work/shuohao-upstream');
  const args = process.argv.slice(2);
  if (args.some(a => !['--fetch', '--check'].includes(a))) throw new Error('Usage: node scripts/sync-shuohao.mjs [--fetch] [--check]');
  if (args.includes('--fetch')) git(source, ['fetch', '--no-tags', '--depth=1', url, 'main']);
  const commit = git(source, ['rev-parse', args.includes('--fetch') ? 'FETCH_HEAD' : 'HEAD']).trim();
  if (!validCommit(commit)) throw new Error('invalid commit');
  const inventory = git(source, ['ls-tree', '-rz', '--long', commit]).split('\0').filter(Boolean);
  const data = new Map();
  let total = 0;
  for (const row of inventory) {
    const match = /^(100644|100755) blob ([0-9a-f]{40})\s+(\d+)\t([\s\S]+)$/.exec(row);
    if (!match || !safePath(match[4]) || Number(match[3]) > 2 * 1024 * 1024) throw new Error('unsupported Git tree entry');
    total += Number(match[3]);
    if (total > 32 * 1024 * 1024 || inventory.length > 2000) throw new Error('upstream inventory limit');
    const bytes = git(source, ['cat-file', 'blob', match[2]], true);
    if (bytes.length !== Number(match[3])) throw new Error('Git size mismatch');
    data.set(match[4], bytes);
  }
  const destination = join(root, 'backend/presets/external/shuohao');
  if (args.includes('--check') && !existsSync(join(destination, commit))) {
    console.log(JSON.stringify({ changed: true, commit, action: 'sync required' }));
    return;
  }
  console.log(JSON.stringify(materialize(data, commit, destination)));
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
