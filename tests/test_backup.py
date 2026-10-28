import io
import zipfile
import pytest
from studio.store import Store, Invalid
from studio.backup import BackupManager
from test_store import setup, candidate, BODY


def test_backup_restores_isolated_project_with_history_and_no_credentials(setup, tmp_path):
    s, p = setup
    c = candidate(s, p)
    s.commit_candidate(c['id'], p['revision'])
    s.set_setting('budget', {'enabled': True, 'total_cap': 100, 'task_cap': 10})
    backup = BackupManager(s, tmp_path / 'management')
    data = backup.export(p['id'])
    restored = backup.restore(data)
    assert restored['id'] != p['id']
    assert restored['title'].endswith('（恢复副本）')
    assert BODY in s.export(restored['id'])
    assert len(s.candidates(restored['id'])) == 1
    assert s.project(p['id'])['chapters'][0]['id'] == c['id']
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        assert 'manifest.json' in z.namelist()
        assert b'OPENAI_API_KEY' not in data


def test_archive_cannot_write_outside_restore_root(setup, tmp_path):
    s, p = setup
    b = io.BytesIO()
    with zipfile.ZipFile(b, 'w') as z:
        z.writestr('../escaped.txt', 'bad')
    with pytest.raises(Invalid):
        BackupManager(s, tmp_path / 'management').restore(b.getvalue())
    assert not (tmp_path / 'escaped.txt').exists()


def test_tampered_blob_rejected_before_project_visibility(setup, tmp_path):
    s, p = setup
    c = candidate(s, p)
    s.commit_candidate(c['id'], p['revision'])
    bm = BackupManager(s, tmp_path / 'management')
    original = bm.export(p['id'])
    out = io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(original)) as src, zipfile.ZipFile(out, 'w') as dst:
        for name in src.namelist():
            dst.writestr(name, 'corrupt' if name.startswith('blobs/') else src.read(name))
    with pytest.raises(Invalid):
        bm.restore(out.getvalue())
    assert len(s.list_projects()) == 1


def test_deleted_project_cannot_resurrect_from_old_backup(setup, tmp_path):
    s, p = setup
    bm = BackupManager(s, tmp_path / 'management')
    old = bm.export(p['id'])
    bm.delete_project(p['id'])
    with pytest.raises(Invalid, match='撤销'):
        BackupManager(Store(tmp_path), tmp_path / 'management').restore(old)
    assert s.list_projects() == []


def test_migration_requires_explicit_reconciliation(setup, tmp_path):
    s, p = setup
    data = BackupManager(s, tmp_path / 'management').export(p['id'])
    other = Store(tmp_path / 'other')
    manager = BackupManager(other, tmp_path / 'other-management')
    with pytest.raises(Invalid, match='迁移'):
        manager.restore(data)
    assert manager.restore(data, allow_migration=True)['title'].endswith('（恢复副本）')


def test_deleting_restored_copy_does_not_delete_original_after_restart(setup, tmp_path):
    s, p = setup
    bm = BackupManager(s, tmp_path / 'management')
    restored = bm.restore(bm.export(p['id']))
    bm.delete_project(restored['id'])
    BackupManager(Store(tmp_path), tmp_path / 'management')
    assert s.project(p['id'])['title'] == '钥匙'
    assert len(s.list_projects()) == 1
