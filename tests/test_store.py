import pytest
from studio.store import Store, Conflict, Invalid


BODY = '林舟把铜钥匙放进口袋。他对守卫说：“门已经锁了。”'


def ledger():
    return {'entries': [
        {'kind': 'fact', 'subject': 'lin', 'predicate': '持有', 'value': '铜钥匙', 'story_time': '开场', 'known_by': ['lin'], 'quote': '林舟把铜钥匙放进口袋', 'start': 0},
        {'kind': 'statement', 'subject': 'lin', 'predicate': '声称', 'value': '门已经锁了', 'story_time': '开场', 'known_by': ['guard'], 'quote': '门已经锁了', 'start': 20},
    ], 'threads': [{'name': '铜钥匙的用途', 'status': 'open', 'quote': '铜钥匙', 'start': 4}], 'summary': '林舟藏起钥匙并向守卫声称门锁了。'}


@pytest.fixture
def setup(tmp_path):
    s = Store(tmp_path)
    p = s.create_project('钥匙', '悬疑。限制第三人称，从林舟视角写。')
    lore = {'entities': [{'id': 'lin', 'name': '林舟', 'aliases': [], 'description': '年轻修锁匠'}, {'id': 'guard', 'name': '守卫', 'aliases': [], 'description': '城门守卫'}], 'rules': [], 'brief': '林舟必须在黎明前进城。'}
    p = s.save_lore(p['id'], lore, p['revision'])
    return s, p


def candidate(s, p, **kw):
    return s.create_candidate(p['id'], 1, '门前', BODY, p['revision'], ledger=ledger(), issues=[], reviewed=True, **kw)


def test_commit_is_atomic_persistent_and_does_not_promote_dialogue(setup, tmp_path):
    s, p = setup
    c = candidate(s, p)
    s.commit_candidate(c['id'], p['revision'])
    reopened = Store(tmp_path)
    ctx = reopened.context(p['id'], '钥匙', 2)
    assert ctx['entries'][0]['kind'] == 'fact'
    assert ctx['entries'][1]['kind'] == 'statement'
    assert len([x for x in ctx['entries'] if x['kind'] == 'fact']) == 1
    assert ctx['sources'][0]['body'] == BODY
    assert '铜钥匙' in reopened.export(p['id'], 'md')
    assert reopened.project(p['id'])['revision'] == p['revision'] + 1


def test_stale_candidate_never_overwrites_new_lore(setup):
    s, p = setup
    c = candidate(s, p)
    s.save_lore(p['id'], p['lore'], p['revision'])
    with pytest.raises(Conflict):
        s.commit_candidate(c['id'], p['revision'])
    assert s.project(p['id'])['chapters'] == []


def test_bad_evidence_and_unknown_entity_block_canon(setup):
    s, p = setup
    data = ledger()
    data['entries'][0]['quote'] = '他杀死了守卫'
    data['entries'][0]['subject'] = 'nonexistent'
    c = s.create_candidate(p['id'], 1, '错误', BODY, p['revision'], ledger=data, issues=[], reviewed=True)
    with pytest.raises(Invalid):
        s.commit_candidate(c['id'], p['revision'])
    assert s.project(p['id'])['chapters'] == []


def test_manual_edit_preserves_prior_version_and_requires_review(setup):
    s, p = setup
    c = candidate(s, p)
    s.commit_candidate(c['id'], p['revision'])
    current = s.project(p['id'])
    edit = s.create_candidate(p['id'], 1, '门前', BODY + '天亮了。', current['revision'])
    with pytest.raises(Invalid):
        s.commit_candidate(edit['id'], current['revision'])
    assert s.candidate(c['id'])['body'] == BODY
    assert s.project(p['id'])['chapters'][0]['id'] == c['id']


def test_context_excludes_other_projects_and_future_chapters(setup):
    s, p = setup
    c = candidate(s, p)
    s.commit_candidate(c['id'], p['revision'])
    other = s.create_project('别的小说', '无关联')
    assert s.context(other['id'], '钥匙', 2)['sources'] == []
    assert s.context(p['id'], '钥匙', 1)['sources'] == []


def test_lore_change_marks_existing_chapters_stale(setup):
    s, p = setup
    c = candidate(s, p)
    s.commit_candidate(c['id'], p['revision'])
    p = s.project(p['id'])
    s.save_lore(p['id'], p['lore'], p['revision'])
    assert s.project(p['id'])['chapters'][0]['needs_review'] is True


def test_quoted_lie_with_speech_prefix_cannot_be_promoted_to_fact(setup):
    s, p = setup
    body = '林舟撒谎说：“钥匙在屋里。”'
    data = ledger()
    data['entries'] = [{**data['entries'][0], 'predicate': '钥匙位置', 'value': '屋里', 'quote': body, 'start': 0}]
    data['threads'] = []
    c = s.create_candidate(p['id'], 1, '谎言', body, p['revision'], ledger=data, issues=[], reviewed=True)
    with pytest.raises(Invalid):
        s.commit_candidate(c['id'], p['revision'])


def test_edited_creative_brief_becomes_authoritative_request(setup):
    s, p = setup
    changed = {**p['lore'], 'brief': '改成轻喜剧，第一人称。'}
    updated = s.save_lore(p['id'], changed, p['revision'])
    assert updated['brief'] == '改成轻喜剧，第一人称。'
