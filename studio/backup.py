import hashlib
import io
import json
import os
import re
import sqlite3
import tempfile
import zipfile
from contextlib import closing
from pathlib import Path
from .store import Invalid, uid, now, encode, digest
from .checks import validate_lore


class BackupManager:
    def __init__(self, store, management):
        self.store = store
        self.management = Path(management)
        self.management.mkdir(parents=True, exist_ok=True)
        self.journal = self.management / 'revocations.jsonl'
        install = self.management / 'installation-id'
        if not install.exists():
            install.write_text(uid(), encoding='ascii')
        self.installation = install.read_text(encoding='ascii')
        for pid in self.revoked():
            self._purge(pid)

    def revoked(self):
        if not self.journal.exists():
            return set()
        try:
            return {json.loads(line)['project_id'] for line in self.journal.read_text(encoding='utf-8').splitlines() if line.strip()}
        except Exception as exc:
            raise Invalid('撤销记录损坏，已停止恢复操作；请保留管理目录并修复') from exc

    def _revoke(self, ids):
        existing = self.revoked()
        with self.journal.open('a', encoding='utf-8') as f:
            for pid in ids - existing:
                f.write(encode({'project_id': pid, 'revoked_at': now()}) + '\n')
            f.flush()
            os.fsync(f.fileno())

    def _purge(self, pid):
        with self.store.tx() as db:
            rows = db.execute('SELECT id FROM tasks WHERE project_id=?', (pid,)).fetchall()
            for row in rows:
                for call in db.execute('SELECT id,data FROM calls WHERE task_id=?', (row[0],)).fetchall():
                    data = json.loads(call['data'])
                    data.pop('result', None)
                    db.execute('UPDATE calls SET data=? WHERE id=?', (encode(data), call['id']))
            for table in ['candidates', 'canon', 'tasks', 'skills', 'revisions', 'search']:
                db.execute(f'DELETE FROM {table} WHERE project_id=?', (pid,))
            db.execute('DELETE FROM projects WHERE id=?', (pid,))

    def delete_project(self, pid):
        p = self.store.project(pid)
        if any(t['status'] in ('running', 'queued', 'outcome_unknown') for t in self.store.tasks(pid)):
            raise Invalid('请先取消运行中或结果未知的任务，再删除项目')
        identities = {pid}
        self._revoke(identities)
        self._purge(pid)

    def export(self, pid):
        p = self.store.project(pid)
        if pid in self.revoked():
            raise Invalid('项目已撤销')
        with tempfile.TemporaryDirectory(dir=self.store.root, prefix='backup-') as temp:
            path = Path(temp) / 'project.sqlite3'
            # SQLite's backup API provides a consistent snapshot, including committed WAL data.
            with self.store.lock, closing(sqlite3.connect(self.store.path)) as source, closing(sqlite3.connect(path)) as dest:
                source.backup(dest)
            with closing(sqlite3.connect(path)) as db:
                db.execute('DELETE FROM projects WHERE id<>?', (pid,))
                for table in ['candidates', 'canon', 'tasks', 'skills', 'revisions', 'search']:
                    db.execute(f'DELETE FROM {table} WHERE project_id<>?', (pid,))
                db.execute('DELETE FROM settings')
                db.execute('DELETE FROM calls')
                db.commit()
                db.execute('VACUUM')
                blobs = {json.loads(r[0])['blob'] for r in db.execute('SELECT data FROM candidates')}
            files = {'project.sqlite3': path.read_bytes()}
            for sha in blobs:
                files[f'blobs/{sha}.txt'] = self.store.read_blob(sha).encode('utf-8')
            manifest = {'version': 1, 'project_id': pid, 'origin_project_id': p.get('origin_project_id', pid), 'installation': self.installation, 'revoked': sorted(self.revoked()), 'created_at': now(), 'files': {name: hashlib.sha256(data).hexdigest() for name, data in files.items()}}
            out = io.BytesIO()
            with zipfile.ZipFile(out, 'w', zipfile.ZIP_DEFLATED) as z:
                z.writestr('manifest.json', encode(manifest))
                for name, data in files.items():
                    z.writestr(name, data)
            return out.getvalue()

    def restore(self, archive, allow_migration=False):
        try:
            return self._restore(archive, allow_migration)
        except Invalid:
            raise
        except Exception as exc:
            raise Invalid('备份损坏或格式不支持，未恢复到项目列表') from exc

    def _restore(self, archive, allow_migration):
        if len(archive) > 50 * 1024 * 1024:
            raise Invalid('备份超过 50 MB 限制')
        with zipfile.ZipFile(io.BytesIO(archive)) as z:
            names = z.namelist()
            if len(names) > 2000 or len(set(names)) != len(names) or sum(i.file_size for i in z.infolist()) > 100 * 1024 * 1024:
                raise Invalid('备份文件数量、大小或重复路径异常')
            if any(n not in ('manifest.json', 'project.sqlite3') and not re.fullmatch('blobs/[a-f0-9]{64}\\.txt', n) for n in names):
                raise Invalid('备份包含非法路径')
            manifest = json.loads(z.read('manifest.json'))
            if manifest['version'] != 1 or set(manifest['files']) != set(names) - {'manifest.json'}:
                raise Invalid('备份清单不匹配')
            for name, sha in manifest['files'].items():
                if hashlib.sha256(z.read(name)).hexdigest() != sha:
                    raise Invalid('备份校验失败')
            revoked = self.revoked() | set(manifest['revoked'])
            if {manifest['project_id'], manifest['origin_project_id']} & revoked:
                raise Invalid('该项目已撤销，旧备份不能恢复它')
            if manifest['installation'] != self.installation and not allow_migration:
                raise Invalid('这是跨安装迁移，缺少来源机当前撤销记录。请核对后明确确认迁移。')
            with tempfile.TemporaryDirectory(dir=self.store.root, prefix='restore-') as temp:
                path = Path(temp) / 'project.sqlite3'
                path.write_bytes(z.read('project.sqlite3'))
                db = sqlite3.connect(path.as_uri() + '?mode=ro&immutable=1', uri=True)
                try:
                    db.execute('PRAGMA trusted_schema=OFF')
                    if db.execute("SELECT 1 FROM sqlite_master WHERE type IN ('view','trigger')").fetchone():
                        raise Invalid('备份含不支持的数据库对象')
                    for table in ['projects', 'candidates', 'canon', 'tasks', 'skills', 'revisions']:
                        row = db.execute('SELECT type FROM sqlite_master WHERE name=?', (table,)).fetchone()
                        if not row or row[0] != 'table':
                            raise Invalid('备份数据库结构不完整')
                    ps = db.execute('SELECT id,data FROM projects').fetchall()
                    if len(ps) != 1 or ps[0][0] != manifest['project_id']:
                        raise Invalid('备份项目身份不匹配')
                    p = json.loads(ps[0][1])
                    validate_lore(p['lore'])
                    candidates = [json.loads(r[0]) for r in db.execute('SELECT data FROM candidates')]
                    canon = db.execute('SELECT chapter,candidate_id,needs_review FROM canon WHERE project_id=?', (p['id'],)).fetchall()
                    skills = db.execute('SELECT role,data FROM skills WHERE project_id=?', (p['id'],)).fetchall()
                finally:
                    db.close()
            old_id, new_id = p['id'], uid()
            mapping = {c['id']: uid() for c in candidates}
            for c in candidates:
                if c['project_id'] != old_id or not 1 <= c['chapter'] <= 10000:
                    raise Invalid('备份包含跨项目或无效章节')
                body = z.read('blobs/' + c['blob'] + '.txt').decode('utf-8')
                if digest(body) != c['blob']:
                    raise Invalid('正文哈希不符')
                self.store.put_blob(body)
            if any(cid not in mapping for _, cid, _ in canon):
                raise Invalid('正史引用缺失')
            p.update(id=new_id, origin_project_id=manifest['origin_project_id'], title=p['title'] + '（恢复副本）', restored_at=now())
            p.pop('chapters', None)
            self._revoke(set(manifest['revoked']))
            with self.store.tx() as target:
                self.store._save_project(target, p)
                for c in candidates:
                    c.update(id=mapping[c['id']], project_id=new_id, task_id=None)
                    if c.get('context'):
                        c['context']['project_id'] = new_id
                    target.execute('INSERT INTO candidates VALUES(?,?,?,?)', (c['id'], new_id, c['chapter'], encode(c)))
                for chapter, cid, stale in canon:
                    target.execute('INSERT INTO canon VALUES(?,?,?,?)', (new_id, chapter, mapping[cid], stale))
                for role, data in skills:
                    target.execute('INSERT INTO skills VALUES(?,?,?,?)', (uid(), new_id, role, data))
            # Rebuild derived retrieval index after all authoritative data is present.
            import jieba
            with self.store.tx() as target:
                for c in candidates:
                    if any(mapping[cid] == c['id'] for _, cid, _ in canon):
                        target.execute('INSERT INTO search VALUES(?,?,?)', (new_id, c['id'], ' '.join(jieba.cut_for_search(self.store.read_blob(c['blob'])))))
            return self.store.project(new_id)
