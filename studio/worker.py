import json
import math
import threading
from pydantic import ValidationError
from .store import Store, Invalid, Conflict, uid, now, encode, digest
from .skills import SkillRegistry
from .models import OUTPUTS
from .checks import check_candidate, raw_ledger, issue, validate_lore
from .provider import UnknownOutcome, Rejected

DEFAULT_BUDGET = {'enabled': False, 'total_cap': 10.0, 'task_cap': 10.0}
# Conservative estimate, includes a 20% exchange/price margin. No cached-token discount.
PRICE = {'model': 'gpt-6.1-sol', 'input_usd_per_million': 2, 'output_usd_per_million': 10, 'usd_cny': 7.5, 'margin': 1.2, 'version': '2026-10-24-standard-short'}


def price(input_tokens, output_tokens):
    return math.ceil((input_tokens * 2 + output_tokens * 10) / 1e6 * 7.5 * 1.2 * 1e6) / 1e6


class Paused(Exception):
    pass


class Worker:
    def __init__(self, store: Store, provider):
        self.store, self.provider = store, provider
        self.skills = SkillRegistry(store)
        self.stop = threading.Event()
        self.wake = threading.Event()
        self.run_lock = threading.Lock()
        self.thread = None

    def start(self):
        self.recover()
        self.thread = threading.Thread(target=self._loop, daemon=True, name='novel-worker')
        self.thread.start()

    def close(self):
        self.stop.set()
        self.wake.set()
        if self.thread:
            self.thread.join(timeout=2)

    def _loop(self):
        while not self.stop.is_set():
            queued = [t for t in reversed(self.store.tasks()) if t['status'] == 'queued']
            if queued:
                self.run(queued[0]['id'])
            else:
                self.wake.wait(1)
                self.wake.clear()

    def recover(self):
        with self.store.tx() as db:
            db.execute("UPDATE calls SET state='unknown' WHERE state='running'")
            for row in db.execute('SELECT id,data FROM tasks').fetchall():
                t = json.loads(row['data'])
                if t['status'] in ('running', 'queued'):
                    unknown = db.execute("SELECT 1 FROM calls WHERE task_id=? AND state='unknown'", (t['id'],)).fetchone()
                    t.update(status='outcome_unknown' if unknown else 'paused', message='应用曾中断，已保存阶段结果。请核查后继续；未知调用不自动重复。')
                    db.execute('UPDATE tasks SET data=? WHERE id=?', (encode(t), t['id']))

    def budget(self):
        config = self.store.setting('budget', DEFAULT_BUDGET)
        with self.store.tx() as db:
            rows = db.execute('SELECT state,reserved,actual,data FROM calls').fetchall()
        return {**config, 'spent': round(sum(r['actual'] or 0 for r in rows if r['state'] == 'done'), 6), 'reserved': round(sum(r['reserved'] for r in rows if r['state'] in ('running', 'unknown')), 6), 'price': PRICE, 'key_ready': self.provider.ready,
                'calls': [{'state': r['state'], 'reserved': r['reserved'], 'actual': r['actual'], **{k: v for k, v in json.loads(r['data']).items() if k in ('role', 'stage', 'created_at', 'response_id', 'input_tokens', 'output_tokens')}} for r in rows]}

    def enqueue(self, pid, *, chapter=None, request='', candidate_id=None):
        cfg = self.store.setting('budget', DEFAULT_BUDGET)
        if not cfg['enabled']:
            raise Invalid('付费调用尚未启用。请先在费用设置中确认额度。')
        if not self.provider.ready:
            raise Invalid('请先在本机设置 API 密钥。密钥不会进入项目或备份。')
        p = self.store.project(pid)
        c = self.store.candidate(candidate_id) if candidate_id else None
        if c and c['project_id'] != pid:
            raise Invalid('稿件不属于当前项目')
        chapter = c['chapter'] if c else (chapter or len(p['chapters']) + 1)
        if not 1 <= chapter <= len(p['chapters']) + 1:
            raise Invalid('请按章节顺序写作')
        context = self.store.context(pid, request or p['brief'], chapter)
        if context['stale_chapters']:
            raise Invalid('前章受到设定或正文变更影响，请先重新审查前章')
        skill_set, skills = self.skills.snapshot(pid)
        t = dict(id=uid(), project_id=pid, generation_token=uid(), base_revision=p['revision'], chapter=chapter, status='queued', stage='等待', created_at=now(), skill_set_id=skill_set, skills=skills, context=context, request=request or p['brief'], brief=p['brief'], candidate_input=c, steps={}, candidate_id=None, message='', task_cap=cfg['task_cap'])
        with self.store.tx() as db:
            if any(json.loads(r[0])['status'] in ('queued', 'running', 'outcome_unknown') for r in db.execute('SELECT data FROM tasks WHERE project_id=?', (pid,))):
                raise Invalid('当前项目已有进行中或结果未知的任务，请先处理它')
            db.execute('INSERT INTO tasks VALUES(?,?,?)', (t['id'], pid, encode(t)))
        self.wake.set()
        return t

    def cancel(self, tid):
        with self.store.tx() as db:
            t = self.store._task(db, tid)
            if t['status'] in ('queued', 'running', 'paused', 'outcome_unknown'):
                t.update(status='cancelled', message='已取消后续步骤；已发送请求的费用仍按实际或预留计算。')
                db.execute('UPDATE tasks SET data=? WHERE id=?', (encode(t), tid))
        return self.store.task(tid)

    def resume(self, tid):
        enabled = self.store.setting('budget', DEFAULT_BUDGET)['enabled']
        with self.store.tx() as db:
            t = self.store._task(db, tid)
            if t['status'] != 'paused':
                raise Invalid('仅可继续已安全暂停的任务；未知调用请先核对账单')
            if self.store._project(db, t['project_id'])['revision'] != t['base_revision']:
                raise Conflict('项目已改变，请创建基于新版本的审查任务')
            if not enabled:
                raise Invalid('付费调用未启用')
            t.update(status='queued', message='')
            db.execute('UPDATE tasks SET data=? WHERE id=?', (encode(t), tid))
        self.wake.set()
        return t

    def _update(self, tid, **changes):
        with self.store.tx() as db:
            t = self.store._task(db, tid)
            if t['status'] != 'running':
                raise Paused('任务已停止')
            t.update(changes)
            db.execute('UPDATE tasks SET data=? WHERE id=?', (encode(t), tid))
        return t

    def _stage(self, tid, stage, role, payload):
        t = self.store.task(tid)
        if t['status'] != 'running' or self.stop.is_set():
            raise Paused('任务已停止')
        self._update(tid, stage=stage)
        skill = t['skills'][role]
        instructions = SkillRegistry.instructions(skill)
        schema = OUTPUTS[role].model_json_schema()
        max_tokens = 10000 if role == 'writer' else 6000
        input_hash = digest(encode([instructions, payload, schema, max_tokens]))
        # UTF-8 bytes bound tokens conservatively; reject oversized input instead of silently omitting canon.
        input_bound = len(encode([instructions, payload, schema]).encode('utf-8')) + 2048
        if input_bound > 120000:
            raise Paused('上下文超过本版安全输入上限，请缩小资料或拆分项目后继续')
        reserve = price(input_bound, max_tokens)
        cached = None
        call_id = uid()
        with self.store.tx() as db:
            for r in db.execute('SELECT * FROM calls WHERE task_id=?', (tid,)):
                d = json.loads(r['data'])
                if d['stage'] == stage and d['input_hash'] == input_hash:
                    if r['state'] == 'done':
                        cached = d['result']
                    elif r['state'] in ('unknown', 'running'):
                        raise UnknownOutcome('这个阶段的既有请求结果未知，不能自动重发')
            if cached is None:
                cfgrow = db.execute("SELECT data FROM settings WHERE key='budget'").fetchone()
                cfg = json.loads(cfgrow[0]) if cfgrow else DEFAULT_BUDGET
                if not cfg['enabled']:
                    raise Paused('付费调用已关闭')
                total = db.execute("SELECT COALESCE(SUM(CASE WHEN state='done' THEN actual WHEN state IN ('running','unknown') THEN reserved ELSE 0 END),0) FROM calls").fetchone()[0]
                task_total = db.execute("SELECT COALESCE(SUM(CASE WHEN state='done' THEN actual WHEN state IN ('running','unknown') THEN reserved ELSE 0 END),0) FROM calls WHERE task_id=?", (tid,)).fetchone()[0]
                if total + reserve > cfg['total_cap'] or task_total + reserve > min(cfg['task_cap'], t['task_cap']):
                    raise Paused(f'下一步预留约 ¥{reserve:.2f} 将超过预算，已暂停；不会自动扩大额度')
                if self.store._task(db, tid)['status'] != 'running':
                    raise Paused('任务已停止')
                db.execute('INSERT INTO calls VALUES(?,?,?,?,?,?)', (call_id, tid, 'running', reserve, None, encode(dict(stage=stage, role=role, input_hash=input_hash, created_at=now(), price=PRICE))))
        if cached is None:
            try:
                cached = self.provider.generate(role, instructions, payload, schema, max_tokens)
            except Rejected:
                with self.store.tx() as db:
                    db.execute("UPDATE calls SET state='rejected',actual=0 WHERE id=?", (call_id,))
                raise
            except Exception as exc:
                with self.store.tx() as db:
                    db.execute("UPDATE calls SET state='unknown' WHERE id=?", (call_id,))
                raise UnknownOutcome('请求结果未知，预算保持占用，请先核对服务商账单。') from exc
            with self.store.tx() as db:
                d = json.loads(db.execute('SELECT data FROM calls WHERE id=?', (call_id,)).fetchone()[0])
                d.update(result=cached, response_id=cached.get('response_id'), input_tokens=cached['input_tokens'], output_tokens=cached['output_tokens'])
                db.execute("UPDATE calls SET state='done',actual=?,data=? WHERE id=?", (price(cached['input_tokens'], cached['output_tokens']), encode(d), call_id))
        result = OUTPUTS[role].model_validate(cached['data']).model_dump()
        current = self.store.task(tid)
        self._update(tid, steps={**current['steps'], stage: {'role': role, 'data': result, 'input_hash': input_hash, 'finished_at': now()}})
        return result

    def run(self, tid):
        with self.run_lock:
            with self.store.tx() as db:
                t = self.store._task(db, tid)
                if t['status'] != 'queued':
                    return
                t['status'] = 'running'
                db.execute('UPDATE tasks SET data=? WHERE id=?', (encode(t), tid))
            try:
                self._pipeline(t)
            except UnknownOutcome as exc:
                self._finish_error(tid, 'outcome_unknown', str(exc))
            except Paused as exc:
                self._finish_error(tid, 'paused', str(exc))
            except (Invalid, Conflict, ValidationError, Rejected) as exc:
                self._finish_error(tid, 'failed', '输入、输出或版本检查未通过：' + str(exc)[:800])
            except Exception:
                self._finish_error(tid, 'failed', '本地处理失败，已保留阶段结果。请查看任务与备份，不会自动重试付费请求。')

    def _finish_error(self, tid, status, message):
        with self.store.tx() as db:
            t = self.store._task(db, tid)
            if t['status'] == 'running':
                t.update(status=status, message=message)
                db.execute('UPDATE tasks SET data=? WHERE id=?', (encode(t), tid))

    def _pipeline(self, t):
        tid = t['id']
        context = t['context']
        proposed = None
        initial = t['candidate_input']
        if initial and initial.get('proposed_lore') and initial['base_revision'] == t['base_revision'] and initial['status'] != 'confirmed':
            proposed = validate_lore(initial['proposed_lore'])
            context = {**context, 'lore': proposed}
        elif not context['lore']['entities']:
            proposed = validate_lore(self._stage(tid, '建立设定', 'architect', {'brief': t['brief'], 'author_request': t['request'], 'context': context}))
            context = {**context, 'lore': proposed}
        if initial:
            plan = initial['plan'] or {'title': initial['title'], 'pov': context['lore']['entities'][0]['id'], 'story_time': '当前章', 'goal': '保留作者手稿并检查', 'conflict': '依据正文', 'beats': ['审查已有正文'], 'ending_hook': '依据正文', 'target_chars': len(initial['body'])}
            draft = {'title': initial['title'], 'body': initial['body']}
        else:
            plan = self._stage(tid, '规划章卡', 'planner', {'chapter': t['chapter'], 'author_request': t['request'], 'context': context})
            draft = self._stage(tid, '撰写正文', 'writer', {'plan': plan, 'author_request': t['request'], 'context': context})
        for attempt in range(2):
            suffix = '' if attempt == 0 else '（修订后）'
            ledger = self._stage(tid, '独立记账' + suffix, 'bookkeeper', {'body': draft['body'], 'plan': plan, 'context': context})
            deterministic, ledger = check_candidate(draft['body'], ledger, context['lore'], True, plan=plan)
            review = self._stage(tid, '独立审稿' + suffix, 'critic', {'body': draft['body'], 'plan': plan, 'context': context, 'ledger': ledger, 'deterministic_issues': deterministic})
            problems = review['issues'] + deterministic
            if not any(i['severity'] == 'blocker' for i in problems) or attempt == 1 or initial:
                break
            draft = self._stage(tid, '针对性修订', 'writer', {'plan': plan, 'context': context, 'previous_draft': draft, 'issues': problems, 'author_request': t['request']})
        self._update(tid, stage='质量检查与候选保存')
        c = self.store.create_candidate(t['project_id'], t['chapter'], draft['title'], draft['body'], t['base_revision'], ledger=raw_ledger(ledger), issues=review['issues'], reviewed=True, plan=plan, task_id=tid, skill_set_id=t['skill_set_id'], context=context, proposed_lore=proposed)
        self._update(tid, status='ready', stage='待作者确认', candidate_id=c['id'], message='有阻断问题，请修改并重新审查。' if any(i['severity'] == 'blocker' for i in c['issues']) else '审查完成。请阅读正文、设定和账本后确认。')
