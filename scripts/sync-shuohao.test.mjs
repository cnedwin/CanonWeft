import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { materialize } from './sync-shuohao.mjs';

const sha = 'a'.repeat(40);
function fixture() {
  return new Map([
    ['LICENSE', Buffer.from('Apache-2.0')], ['NOTICE', Buffer.from('Copyright original author')],
    ['README.md', Buffer.from('readme')],
    ['skills/novel-outline/SKILL.md', Buffer.from('---\nname: novel-outline\ndescription: adaptation\n---\nmethod')],
    ['skills/novel-outline/scripts/tool.mjs', Buffer.from('throw new Error("must never execute")')],
    ['skills/novel-outline/examples/渡口.json', Buffer.from('{}')],
  ]);
}
test('immutable complete snapshot: retry no-op, update retains old bytes and reports difference', () => {
  const root = mkdtempSync(join(tmpdir(), 'shuohao-sync-'));
  const data = fixture();
  const first = materialize(data, sha, root);
  assert.equal(first.changed, true);
  assert.deepEqual(first.skills, ['novel-outline']);
  assert.equal(materialize(data, sha, root).changed, false);
  data.set('README.md', Buffer.from('new readme'));
  const next = materialize(data, 'b'.repeat(40), root);
  assert.deepEqual(next.diff.modified, ['README.md']);
  assert.equal(readFileSync(join(root, sha, 'upstream/README.md'), 'utf8'), 'readme');
  assert.equal(readFileSync(join(root, sha, 'upstream/skills/novel-outline/examples/渡口.json'), 'utf8'), '{}');
  writeFileSync(join(root, sha, 'upstream/README.md'), 'tampered');
  assert.throws(() => materialize(fixture(), sha, root), /integrity/);
});
test('reject traversal, aliases, missing license and oversized packages before writing', () => {
  for (const bad of ['../escape', 'skills/a/../escape', 'C:/bad', 'skills/a\\b', 'AUX', 'foo.']) {
    const data = fixture(); data.set(bad, Buffer.from('bad'));
    assert.throws(() => materialize(data, sha, mkdtempSync(join(tmpdir(), 'shuohao-bad-'))));
  }
  const data = fixture(); data.delete('LICENSE');
  assert.throws(() => materialize(data, sha, mkdtempSync(join(tmpdir(), 'shuohao-bad-'))));
  const duplicate = fixture(); duplicate.set('readme.md', Buffer.from('alias'));
  assert.throws(() => materialize(duplicate, sha, mkdtempSync(join(tmpdir(), 'shuohao-bad-'))));
  const oversized = fixture(); oversized.set('huge.dat', Buffer.alloc(2 * 1024 * 1024 + 1));
  assert.throws(() => materialize(oversized, sha, mkdtempSync(join(tmpdir(), 'shuohao-bad-'))));
  const tooMany = fixture();
  for (let i = 0; i < 128; i++) tooMany.set(`skills/novel-outline/references/item-${i}.txt`, Buffer.from('x'));
  assert.throws(() => materialize(tooMany, sha, mkdtempSync(join(tmpdir(), 'shuohao-bad-'))));
});
