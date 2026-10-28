from fastapi.testclient import TestClient
from studio.api import create_app


def client(tmp_path):
    app = create_app(tmp_path, start_worker=False)
    return TestClient(app, base_url='http://127.0.0.1:8765'), app


def auth(c):
    return {'X-Studio-Token': c.get('/api/session').json()['token'], 'Origin': 'http://127.0.0.1:8765'}


def test_foreign_origin_and_missing_token_cannot_read_or_spend(tmp_path):
    c, app = client(tmp_path)
    h = auth(c)
    assert c.get('/api/projects').status_code == 403
    assert c.get('/api/projects', headers={**h, 'Origin': 'https://evil.example'}).status_code == 403
    assert c.post('/api/budget', json={'enabled': True, 'total_cap': 10, 'task_cap': 10}, headers={'Origin': 'https://evil.example'}).status_code == 403
    assert c.get('/api/session', headers={'Host': 'evil.example'}).status_code == 403
    assert c.get('/api/session', headers={'Sec-Fetch-Site': 'cross-site'}).status_code == 403
    assert app.state.worker.budget()['enabled'] is False


def test_create_edit_export_and_restore_without_paid_calls(tmp_path):
    c, app = client(tmp_path)
    h = auth(c)
    p = c.post('/api/projects', headers=h, json={'title': '我的小说', 'brief': '青年修锁匠在雨夜进城'}).json()
    draft = c.post(f"/api/projects/{p['id']}/drafts", headers=h, json={'chapter': 1, 'title': '雨夜', 'body': '林舟站在城门前。', 'expected_revision': 0}).json()
    assert draft['reviewed'] is False
    assert c.post(f"/api/candidates/{draft['id']}/confirm", headers=h, json={'expected_revision': 0}).status_code == 422
    exported = c.get(f"/api/candidates/{draft['id']}/export?format=txt", headers=h)
    assert '林舟站在城门前。' in exported.text
    assert '未确认草稿' in exported.text
    backup = c.get(f"/api/projects/{p['id']}/backup", headers=h).content
    restored = c.post('/api/restore', headers=h, files={'file': ('backup.zip', backup, 'application/zip')})
    assert restored.status_code == 200
    assert restored.json()['id'] != p['id']
    denied = c.post(f"/api/projects/{p['id']}/tasks", headers=h, json={})
    assert denied.status_code == 422
    assert app.state.worker.budget()['spent'] == 0


def test_negative_budget_cannot_disable_spending_guard(tmp_path):
    c, _ = client(tmp_path)
    h = auth(c)
    assert c.post('/api/budget', headers=h, json={'enabled': True, 'total_cap': -1, 'task_cap': 10}).status_code == 422
