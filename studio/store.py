import hashlib
import json
import os
import re
import sqlite3
import threading
import uuid
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
import jieba
from .checks import check_candidate, raw_ledger, validate_lore


class Conflict(ValueError):
    pass


class Invalid(ValueError):
    pass


def uid():
    return uuid.uuid4().hex


def now():
    return datetime.now(timezone.utc).isoformat()


def encode(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':'))


def digest(value):
    return hashlib.sha256(value.encode('utf-8')).hexdigest()


class Store:
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.blobs = self.root / 'blobs'
        self.blobs.mkdir(exist_ok=True)
        self.path = self.root / 'studio.sqlite3'
        self.lock = threading.RLock()
        with self.tx() as db:
            db.executescript('''
            CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS candidates(id TEXT PRIMARY KEY, project_id TEXT NOT NULL, chapter INTEGER NOT NULL, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS canon(project_id TEXT NOT NULL, chapter INTEGER NOT NULL, candidate_id TEXT NOT NULL, needs_review INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(project_id,chapter));
            CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY, project_id TEXT NOT NULL, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS calls(id TEXT PRIMARY KEY, task_id TEXT NOT NULL, state TEXT NOT NULL, reserved REAL NOT NULL, actual REAL, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS skills(id TEXT PRIMARY KEY, project_id TEXT NOT NULL, role TEXT NOT NULL, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS revisions(id TEXT PRIMARY KEY, project_id TEXT NOT NULL, data TEXT NOT NULL);
            CREATE VIRTUAL TABLE IF NOT EXISTS search USING fts5(project_id UNINDEXED, candidate_id UNINDEXED, terms);
            ''')

    @contextmanager
    def tx(self):
        with self.lock:
            db = sqlite3.connect(self.path, timeout=15)
            db.row_factory = sqlite3.Row
            try:
                db.execute('PRAGMA foreign_keys=ON')
                db.execute('BEGIN IMMEDIATE')
                yield db
                db.commit()
            except BaseException:
                db.rollback()
                raise
            finally:
                db.close()

    def put_blob(self, body):
        sha = digest(body)
        path = self.blobs / (sha + '.txt')
        if not path.exists():
            temp = self.blobs / (uid() + '.tmp')
            with temp.open('wb') as f:
                f.write(body.encode('utf-8'))
                f.flush()
                os.fsync(f.fileno())
            os.replace(temp, path)
        return sha

    def read_blob(self, sha):
        if not re.fullmatch('[a-f0-9]{64}', sha):
            raise Invalid('正文引用无效')
        data = (self.blobs / (sha + '.txt')).read_text(encoding='utf-8')
        if digest(data) != sha:
            raise Invalid('正文文件校验失败，请恢复备份')
        return data

    def _project(self, db, pid):
        row = db.execute('SELECT data FROM projects WHERE id=?', (pid,)).fetchone()
        if not row:
            raise Invalid('项目不存在')
        return json.loads(row['data'])

    def _save_project(self, db, p):
        p['updated_at'] = now()
        db.execute('INSERT OR REPLACE INTO projects VALUES(?,?)', (p['id'], encode(p)))

    def create_project(self, title, brief):
        if not title.strip() or not brief.strip() or len(brief) > 30000 or len(title) > 150:
            raise Invalid('请填写书名和创作要求（要求最多 30000 字）')
        p = dict(id=uid(), title=title.strip(), brief=brief, revision=0, branch_id=uid(), created_at=now(), lore={'brief': brief, 'entities': [], 'rules': []})
        with self.tx() as db:
            self._save_project(db, p)
        return self.project(p['id'])

    def list_projects(self):
        with self.tx() as db:
            return [json.loads(r[0]) for r in db.execute('SELECT data FROM projects ORDER BY rowid DESC')]

    def project(self, pid):
        with self.tx() as db:
            p = self._project(db, pid)
            p['chapters'] = []
            for r in db.execute('SELECT c.data, n.needs_review FROM canon n JOIN candidates c ON c.id=n.candidate_id WHERE n.project_id=? ORDER BY n.chapter', (pid,)):
                c = json.loads(r['data'])
                c['needs_review'] = bool(r['needs_review'])
                p['chapters'].append(c)
            return p

    def save_lore(self, pid, lore, expected_revision):
        try:
            lore = validate_lore(lore)
        except Exception as exc:
            raise Invalid(str(exc)) from exc
        with self.tx() as db:
            p = self._project(db, pid)
            if p['revision'] != expected_revision:
                raise Conflict('项目已变化，请刷新后再保存设定')
            db.execute('INSERT INTO revisions VALUES(?,?,?)', (uid(), pid, encode(p)))
            p['lore'] = lore
            p['brief'] = lore['brief']
            p['revision'] += 1
            self._save_project(db, p)
            db.execute('UPDATE canon SET needs_review=1 WHERE project_id=?', (pid,))
        return self.project(pid)

    def create_candidate(self, pid, chapter, title, body, base_revision, *, ledger=None, issues=None, reviewed=False, plan=None, task_id=None, skill_set_id=None, context=None, proposed_lore=None, source_candidate_id=None):
        if not 1 <= chapter <= 10000 or not body.strip() or len(body) > 100000:
            raise Invalid('章节序号或正文长度无效')
        p = self.project(pid)
        if p['revision'] != base_revision:
            raise Conflict('项目已经变化，请基于最新设定重新审查')
        if source_candidate_id:
            source = self.candidate(source_candidate_id)
            if source['project_id'] != pid or source['chapter'] != chapter:
                raise Invalid('原稿不属于当前项目和章节')
            plan = source['plan']
            if source['base_revision'] == base_revision and source['status'] != 'confirmed':
                proposed_lore = source.get('proposed_lore')
        sha = self.put_blob(body)
        effective_lore = validate_lore(proposed_lore) if proposed_lore else p['lore']
        problems, checked = check_candidate(body, raw_ledger(ledger or {}), effective_lore, reviewed, issues or [], plan)
        c = dict(id=uid(), artifact_id=uid(), revision_id=uid(), project_id=pid, branch_id=p['branch_id'], chapter=chapter, title=title, blob=sha, base_revision=base_revision, ledger=checked, issues=problems, reviewed=reviewed, plan=plan, task_id=task_id, skill_set_id=skill_set_id, context=context, status='candidate', created_at=now())
        c['proposed_lore'] = proposed_lore
        c['source_candidate_id'] = source_candidate_id
        with self.tx() as db:
            if self._project(db, pid)['revision'] != base_revision:
                raise Conflict('项目已变化')
            if task_id:
                task = self._task(db, task_id)
                if task['status'] != 'running':
                    raise Conflict('任务已取消或失效，结果未进入候选')
            db.execute('INSERT INTO candidates VALUES(?,?,?,?)', (c['id'], pid, chapter, encode(c)))
        return {**c, 'body': body}

    def candidate(self, cid):
        with self.tx() as db:
            row = db.execute('SELECT data FROM candidates WHERE id=?', (cid,)).fetchone()
            if not row:
                raise Invalid('稿件不存在')
            c = json.loads(row[0])
        return {**c, 'body': self.read_blob(c['blob'])}

    def candidates(self, pid):
        with self.tx() as db:
            self._project(db, pid)
            return [json.loads(r[0]) for r in db.execute('SELECT data FROM candidates WHERE project_id=? ORDER BY rowid DESC', (pid,))]

    def commit_candidate(self, cid, expected_revision):
        c = self.candidate(cid)
        with self.tx() as db:
            p = self._project(db, c['project_id'])
            if p['revision'] != expected_revision or c['base_revision'] != expected_revision:
                raise Conflict('候选基于旧版本，不能覆盖当前正史；请重新审查')
            if c['status'] == 'confirmed':
                raise Conflict('该版本已确认')
            effective_lore = validate_lore(c['proposed_lore']) if c.get('proposed_lore') else p['lore']
            problems, ledger = check_candidate(c['body'], raw_ledger(c['ledger']), effective_lore, c['reviewed'], c['issues'], c['plan'])
            if any(i['severity'] == 'blocker' for i in problems):
                raise Invalid('仍有阻断问题，不能确认正史')
            prior = db.execute('SELECT COUNT(*) FROM canon WHERE project_id=? AND chapter<?', (p['id'], c['chapter'])).fetchone()[0]
            if prior != c['chapter'] - 1:
                raise Invalid('请按顺序确认章节，不能跳过前章')
            if db.execute('SELECT 1 FROM canon WHERE project_id=? AND chapter<? AND needs_review=1', (p['id'], c['chapter'])).fetchone():
                raise Invalid('前章受到变更影响，请先重新审查')
            c.pop('body')
            c.update(status='confirmed', ledger=ledger)
            db.execute('UPDATE candidates SET data=? WHERE id=?', (encode(c), cid))
            db.execute('INSERT OR REPLACE INTO canon VALUES(?,?,?,0)', (p['id'], c['chapter'], cid))
            db.execute('UPDATE canon SET needs_review=1 WHERE project_id=? AND chapter>?', (p['id'], c['chapter']))
            p['revision'] += 1
            p['lore'] = effective_lore
            p['brief'] = effective_lore['brief']
            self._save_project(db, p)
            db.execute('INSERT INTO revisions VALUES(?,?,?)', (c['revision_id'], p['id'], encode({'chapter': c['chapter'], 'candidate': cid, 'revision': p['revision']})))
            db.execute('DELETE FROM search WHERE project_id=? AND candidate_id IN (SELECT id FROM candidates WHERE project_id=? AND chapter=?)', (p['id'], p['id'], c['chapter']))
            terms = ' '.join(jieba.cut_for_search(self.read_blob(c['blob'])))
            db.execute('INSERT INTO search(project_id,candidate_id,terms) VALUES(?,?,?)', (p['id'], cid, terms))
        return self.project(p['id'])

    def context(self, pid, query, before_chapter):
        p = self.project(pid)
        chapters = [c for c in p['chapters'] if c['chapter'] < before_chapter and not c['needs_review']]
        tokens = [t for t in jieba.cut_for_search(query) if t.strip() and len(t) > 1][:30]
        ranked = []
        if tokens:
            with self.tx() as db:
                expr = ' OR '.join('"' + t.replace('"', '""') + '"' for t in tokens)
                ranked = [r[0] for r in db.execute('SELECT candidate_id FROM search WHERE search MATCH ? AND project_id=? ORDER BY rank LIMIT 5', (expr, pid))]
        selected = {c['id'] for c in chapters[-2:]} | set(ranked)
        sources, entries, threads = [], [], {}
        for c in chapters:
            entries.extend({**e, 'revision_id': c['revision_id'], 'chapter': c['chapter']} for e in c['ledger']['entries'])
            for thread in c['ledger']['threads']:
                threads[thread['name']] = {**thread, 'chapter': c['chapter']}
            if c['id'] in selected:
                sources.append({'candidate_id': c['id'], 'revision_id': c['revision_id'], 'chapter': c['chapter'], 'body': self.read_blob(c['blob']), 'summary': c['ledger']['summary']})
        return dict(project_id=pid, branch_id=p['branch_id'], revision=p['revision'], before_chapter=before_chapter, lore=p['lore'], entries=entries, threads=list(threads.values()), sources=sources, omitted=[{'chapter': c['chapter'], 'candidate_id': c['id'], 'summary': c['ledger']['summary']} for c in chapters if c['id'] not in selected], stale_chapters=[c['chapter'] for c in p['chapters'] if c['chapter'] < before_chapter and c['needs_review']])

    def export(self, pid, format='md'):
        if format not in ('md', 'txt'):
            raise Invalid('仅支持 Markdown 和 TXT')
        p = self.project(pid)
        pieces = [('# ' if format == 'md' else '') + p['title']]
        for c in p['chapters']:
            status = '（变更后待复核）' if c['needs_review'] else ''
            pieces.extend([('## ' if format == 'md' else '') + f"第{c['chapter']}章 {c['title']}{status}", self.read_blob(c['blob'])])
        return '\n\n'.join(pieces) + '\n'

    def setting(self, key, default=None):
        with self.tx() as db:
            r = db.execute('SELECT data FROM settings WHERE key=?', (key,)).fetchone()
            return json.loads(r[0]) if r else default

    def set_setting(self, key, data):
        with self.tx() as db:
            db.execute('INSERT OR REPLACE INTO settings VALUES(?,?)', (key, encode(data)))

    def _task(self, db, tid):
        row = db.execute('SELECT data FROM tasks WHERE id=?', (tid,)).fetchone()
        if not row:
            raise Invalid('任务不存在')
        return json.loads(row[0])

    def task(self, tid):
        with self.tx() as db:
            return self._task(db, tid)

    def tasks(self, pid=None):
        with self.tx() as db:
            rows = db.execute('SELECT data FROM tasks' + (' WHERE project_id=?' if pid else '') + ' ORDER BY rowid DESC', (pid,) if pid else ())
            return [json.loads(r[0]) for r in rows]

    def save_task(self, task):
        with self.tx() as db:
            db.execute('INSERT OR REPLACE INTO tasks VALUES(?,?,?)', (task['id'], task['project_id'], encode(task)))
