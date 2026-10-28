import copy
import pytest
from studio.store import Store, Invalid
from studio.worker import Worker
from studio.provider import UnknownOutcome
from studio.skills import SkillRegistry


LORE = {'brief': '林舟取钥匙进城', 'entities': [{'id': 'lin', 'name': '林舟', 'aliases': [], 'description': '修锁匠'}], 'rules': []}
PLAN = {'title': '雨夜的门', 'pov': 'lin', 'story_time': '第一夜', 'goal': '进城', 'conflict': '城门关闭', 'beats': ['取钥匙', '抵达门前'], 'ending_hook': '城内传来警钟', 'target_chars': 40}
BODY = '林舟把铜钥匙放进口袋。城内传来警钟，他抬起头。'
LEDGER = {'entries': [{'kind': 'fact', 'subject': 'lin', 'predicate': '持有', 'value': '铜钥匙', 'story_time': '第一夜', 'known_by': ['lin'], 'quote': '林舟把铜钥匙放进口袋', 'start': 0}], 'threads': [{'name': '城内的警钟', 'status': 'open', 'quote': '城内传来警钟', 'start': 12}], 'summary': '林舟取得钥匙，听见城内警钟。'}


class ScriptedProvider:
    """Only the external model boundary is replaced. No canned provider ships in the app."""
    ready = True

    def __init__(self, bad=False, unknown=False):
        self.roles = []
        self.bad = bad
        self.unknown = unknown

    def generate(self, role, instructions, payload, schema, max_output_tokens):
        self.roles.append(role)
        if self.unknown:
            raise UnknownOutcome('请求结果未知')
        if role == 'bookkeeper':
            assert 'body' in payload and 'critique' not in payload
        result = {'architect': LORE, 'planner': PLAN, 'writer': {'title': PLAN['title'], 'body': BODY}, 'bookkeeper': LEDGER, 'critic': {'issues': [{'severity': 'blocker', 'category': '动机', 'message': '未解释取钥匙的动机', 'quote': '铜钥匙', 'suggestion': '补充动机'}] if self.bad else [], 'assessment': '完成审查'}}[role]
        return {'data': copy.deepcopy(result), 'response_id': 'resp_fixture', 'input_tokens': 100, 'output_tokens': 200}


def prepared(tmp_path, **kwargs):
    store = Store(tmp_path)
    p = store.create_project('雨城', '悬疑小说。第一章1200字，林舟夜里进城。')
    store.set_setting('budget', {'enabled': True, 'total_cap': 10, 'task_cap': 10})
    provider = ScriptedProvider(**kwargs)
    return store, p, provider, Worker(store, provider)


def test_real_pipeline_pins_skills_and_commits_complete_chapter(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    task = w.enqueue(p['id'])
    w.run(task['id'])
    finished = s.task(task['id'])
    assert finished['status'] == 'ready'
    assert provider.roles == ['architect', 'planner', 'writer', 'bookkeeper', 'critic']
    c = s.candidate(finished['candidate_id'])
    assert c['plan']['title'] == '雨夜的门'
    assert c['ledger']['entries'][0]['kind'] == 'fact'
    assert c['skill_set_id'] == task['skill_set_id']
    s.commit_candidate(c['id'], 0)
    assert '林舟' in s.export(p['id'])
    assert w.budget()['spent'] > 0


def test_unresolved_issue_repairs_once_then_blocks_confirmation(tmp_path):
    s, p, provider, w = prepared(tmp_path, bad=True)
    t = w.enqueue(p['id'])
    w.run(t['id'])
    assert provider.roles.count('writer') == 2
    assert provider.roles.count('bookkeeper') == 2
    assert provider.roles.count('critic') == 2
    c = s.candidate(s.task(t['id'])['candidate_id'])
    with pytest.raises(Invalid):
        s.commit_candidate(c['id'], 0)


def test_disabled_budget_never_contacts_model(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    s.set_setting('budget', {'enabled': False, 'total_cap': 10, 'task_cap': 10})
    with pytest.raises(Invalid, match='付费'):
        w.enqueue(p['id'])
    assert provider.roles == []


def test_unknown_charge_survives_restart_and_does_not_retry(tmp_path):
    s, p, provider, w = prepared(tmp_path, unknown=True)
    t = w.enqueue(p['id'])
    w.run(t['id'])
    assert s.task(t['id'])['status'] == 'outcome_unknown'
    before = w.budget()['reserved']
    assert before > 0
    fresh = Worker(Store(tmp_path), provider)
    fresh.recover()
    fresh.run(t['id'])
    assert provider.roles == ['architect']
    assert fresh.budget()['reserved'] == before


def test_task_uses_pinned_skill_after_user_edit(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    t = w.enqueue(p['id'])
    registry = SkillRegistry(s)
    before = t['skills']['writer']['hash']
    skill = registry.get(p['id'], 'writer')
    registry.publish(p['id'], 'writer', skill['instructions'] + '\n多用短句，但保留人物声音。', skill['hash'])
    assert registry.get(p['id'], 'writer')['hash'] != before
    assert s.task(t['id'])['skills']['writer']['hash'] == before


def test_cancelled_task_cannot_generate_or_save_late_results(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    t = w.enqueue(p['id'])
    w.cancel(t['id'])
    w.run(t['id'])
    assert provider.roles == []
    assert s.candidates(p['id']) == []


def test_low_cap_blocks_call_before_transport(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    s.set_setting('budget', {'enabled': True, 'total_cap': 0.001, 'task_cap': 0.001})
    t = w.enqueue(p['id'])
    w.run(t['id'])
    assert provider.roles == []
    assert s.task(t['id'])['status'] == 'paused'


def test_review_first_chapter_reuses_proposed_lore_without_architect_call(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    t = w.enqueue(p['id'])
    w.run(t['id'])
    cid = s.task(t['id'])['candidate_id']
    provider.roles.clear()
    t2 = w.enqueue(p['id'], candidate_id=cid)
    w.run(t2['id'])
    assert provider.roles == ['bookkeeper', 'critic']
    assert s.candidate(s.task(t2['id'])['candidate_id'])['proposed_lore'] == LORE


def test_manual_edit_inherits_candidate_lore_and_plan_but_not_approval(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    t = w.enqueue(p['id'])
    w.run(t['id'])
    cid = s.task(t['id'])['candidate_id']
    edited = s.create_candidate(p['id'],1,'雨城',BODY+'天亮了。',0,source_candidate_id=cid)
    assert edited['proposed_lore'] == LORE
    assert edited['plan'] == PLAN
    assert edited['reviewed'] is False
    provider.roles.clear()
    next_task = w.enqueue(p['id'], candidate_id=edited['id'])
    w.run(next_task['id'])
    assert provider.roles == ['bookkeeper', 'critic']


def test_resume_reuses_completed_paid_stage_after_budget_pause(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    original = provider.generate
    def pause_after_architect(role, *args):
        result = original(role, *args)
        if role == 'architect':
            s.set_setting('budget', {'enabled': False, 'total_cap': 10, 'task_cap': 10})
        return result
    provider.generate = pause_after_architect
    t = w.enqueue(p['id'])
    w.run(t['id'])
    assert s.task(t['id'])['status'] == 'paused'
    assert provider.roles == ['architect']
    s.set_setting('budget', {'enabled': True, 'total_cap': 10, 'task_cap': 10})
    fresh = Worker(Store(tmp_path), provider)
    fresh.recover()
    fresh.resume(t['id'])
    fresh.run(t['id'])
    assert s.task(t['id'])['status'] == 'ready'
    assert provider.roles.count('architect') == 1


def test_inflight_cancel_keeps_charge_but_discards_candidate(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    t = w.enqueue(p['id'])
    original = provider.generate
    def cancel_during_request(role, *args):
        w.cancel(t['id'])
        return original(role, *args)
    provider.generate = cancel_during_request
    w.run(t['id'])
    assert s.task(t['id'])['status'] == 'cancelled'
    assert s.candidates(p['id']) == []
    assert w.budget()['spent'] > 0


def test_truncated_chapter_cannot_be_confirmed_even_if_critic_misses_it(tmp_path):
    s, p, provider, w = prepared(tmp_path)
    original = provider.generate
    def long_plan(role, *args):
        result = original(role, *args)
        if role == 'planner':
            result['data']['target_chars'] = 1800
        return result
    provider.generate = long_plan
    t = w.enqueue(p['id'])
    w.run(t['id'])
    c = s.candidate(s.task(t['id'])['candidate_id'])
    assert any(i['category'] == '完成度' and i['severity'] == 'blocker' for i in c['issues'])
    with pytest.raises(Invalid):
        s.commit_candidate(c['id'], 0)
