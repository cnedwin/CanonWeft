import secrets
from contextlib import asynccontextmanager
from pathlib import Path
from urllib.parse import quote
from fastapi import FastAPI, Request, UploadFile, File, Form
from fastapi.responses import JSONResponse, Response, FileResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field, ConfigDict
from .store import Store, Invalid, Conflict
from .worker import Worker, DEFAULT_BUDGET
from .provider import OpenAIProvider
from .skills import SkillRegistry
from .backup import BackupManager


class Input(BaseModel):
    model_config = ConfigDict(extra='forbid')


class ProjectInput(Input):
    title: str = Field(min_length=1, max_length=150)
    brief: str = Field(min_length=1, max_length=30000)


class LoreInput(Input):
    lore: dict
    expected_revision: int


class DraftInput(Input):
    chapter: int = Field(ge=1, le=10000)
    title: str = Field(min_length=1, max_length=200)
    body: str = Field(min_length=1, max_length=100000)
    expected_revision: int
    source_candidate_id: str | None = None


class TaskInput(Input):
    chapter: int | None = None
    request: str = Field(default='', max_length=30000)
    candidate_id: str | None = None


class RevisionInput(Input):
    expected_revision: int


class SkillInput(Input):
    instructions: str = Field(max_length=15000)
    expected_hash: str


class BudgetInput(Input):
    enabled: bool
    total_cap: float = Field(ge=0, le=1000, allow_inf_nan=False)
    task_cap: float = Field(gt=0, le=10, allow_inf_nan=False)


class KeyInput(Input):
    key: str = Field(min_length=10, max_length=1000)


def create_app(data_root, provider=None, start_worker=True, port=8765):
    store = Store(data_root)
    worker = Worker(store, provider or OpenAIProvider())
    registry = SkillRegistry(store)
    backups = BackupManager(store, Path(data_root).parent / (Path(data_root).name + '-management'))
    token = secrets.token_urlsafe(32)
    allowed_hosts = {f'127.0.0.1:{port}', f'localhost:{port}'}
    allowed_origins = {'http://' + h for h in allowed_hosts}

    @asynccontextmanager
    async def lifespan(app):
        if start_worker:
            worker.start()
        yield
        worker.close()

    app = FastAPI(title='砚台 · 小说工作室', lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)
    app.state.store, app.state.worker, app.state.backups = store, worker, backups

    @app.middleware('http')
    async def protect(request: Request, call_next):
        origin = request.headers.get('origin')
        if request.headers.get('host') not in allowed_hosts or (origin is not None and origin not in allowed_origins) or request.headers.get('sec-fetch-site') == 'cross-site':
            return JSONResponse({'detail': '仅允许本机工作台访问'}, status_code=403)
        if request.url.path.startswith('/api/') and request.url.path != '/api/session':
            if not secrets.compare_digest(request.headers.get('x-studio-token', ''), token):
                return JSONResponse({'detail': '会话已失效，请刷新页面'}, status_code=403)
        response = await call_next(request)
        response.headers['Cache-Control'] = 'no-store'
        response.headers['X-Content-Type-Options'] = 'nosniff'
        response.headers['Referrer-Policy'] = 'no-referrer'
        response.headers['Content-Security-Policy'] = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
        return response

    @app.exception_handler(Invalid)
    async def invalid(request, exc):
        return JSONResponse({'detail': str(exc)}, status_code=422)

    @app.exception_handler(Conflict)
    async def conflict(request, exc):
        return JSONResponse({'detail': str(exc)}, status_code=409)

    @app.get('/api/session')
    def session():
        return {'token': token, 'version': '0.1.0', 'paid_enabled': worker.budget()['enabled']}

    @app.get('/api/projects')
    def projects():
        return store.list_projects()

    @app.post('/api/projects')
    def create(body: ProjectInput):
        return store.create_project(body.title, body.brief)

    @app.get('/api/projects/{pid}')
    def project(pid: str):
        return store.project(pid)

    @app.put('/api/projects/{pid}/lore')
    def lore(pid: str, body: LoreInput):
        return store.save_lore(pid, body.lore, body.expected_revision)

    @app.get('/api/projects/{pid}/candidates')
    def candidates(pid: str):
        return store.candidates(pid)

    @app.post('/api/projects/{pid}/drafts')
    def draft(pid: str, body: DraftInput):
        return store.create_candidate(pid, body.chapter, body.title, body.body, body.expected_revision, source_candidate_id=body.source_candidate_id)

    @app.get('/api/candidates/{cid}')
    def candidate(cid: str):
        return store.candidate(cid)

    @app.post('/api/candidates/{cid}/confirm')
    def confirm(cid: str, body: RevisionInput):
        return store.commit_candidate(cid, body.expected_revision)

    @app.get('/api/projects/{pid}/context')
    def context(pid: str, query: str = '', chapter: int = 1):
        return store.context(pid, query[:1000], chapter)

    @app.get('/api/projects/{pid}/skills')
    def skills(pid: str):
        store.project(pid)
        return registry.all(pid)

    @app.put('/api/projects/{pid}/skills/{role}')
    def skill(pid: str, role: str, body: SkillInput):
        return registry.publish(pid, role, body.instructions, body.expected_hash)

    @app.get('/api/projects/{pid}/tasks')
    def tasks(pid: str):
        return store.tasks(pid)

    @app.post('/api/projects/{pid}/tasks')
    def enqueue(pid: str, body: TaskInput):
        return worker.enqueue(pid, chapter=body.chapter, request=body.request, candidate_id=body.candidate_id)

    @app.post('/api/tasks/{tid}/cancel')
    def cancel(tid: str):
        return worker.cancel(tid)

    @app.post('/api/tasks/{tid}/resume')
    def resume(tid: str):
        return worker.resume(tid)

    @app.get('/api/budget')
    def budget():
        return worker.budget()

    @app.post('/api/budget')
    def set_budget(body: BudgetInput):
        if body.enabled and body.total_cap < worker.budget()['spent'] + worker.budget()['reserved']:
            raise Invalid('额度不能低于已经支出与待核实费用之和')
        store.set_setting('budget', body.model_dump())
        return worker.budget()

    @app.post('/api/key')
    def key(body: KeyInput):
        if not isinstance(worker.provider, OpenAIProvider):
            raise Invalid('当前运行环境不接受密钥设置')
        worker.provider.set_key(body.key)
        return {'ready': True, 'message': '密钥仅保留在本次服务进程内存中；重启后需重新输入或设置 OPENAI_API_KEY。'}

    def download(data, filename, media_type):
        return Response(data, media_type=media_type, headers={'Content-Disposition': "attachment; filename*=UTF-8''" + quote(filename)})

    @app.get('/api/projects/{pid}/export')
    def export(pid: str, format: str = 'md'):
        return download(store.export(pid, format).encode('utf-8-sig'), 'novel.' + format, 'text/plain; charset=utf-8')

    @app.get('/api/candidates/{cid}/export')
    def export_draft(cid: str, format: str = 'md'):
        if format not in ('md', 'txt'):
            raise Invalid('仅支持 Markdown / TXT')
        c = store.candidate(cid)
        text = ('# ' if format == 'md' else '') + c['title'] + ('\n\n【未确认草稿】' if c['status'] != 'confirmed' else '') + '\n\n' + c['body']
        return download(text.encode('utf-8-sig'), 'chapter.' + format, 'text/plain; charset=utf-8')

    @app.get('/api/projects/{pid}/backup')
    def backup(pid: str):
        return download(backups.export(pid), 'novel-backup.zip', 'application/zip')

    @app.post('/api/restore')
    async def restore(file: UploadFile = File(...), allow_migration: bool = Form(False)):
        data = await file.read(50 * 1024 * 1024 + 1)
        return backups.restore(data, allow_migration)

    @app.delete('/api/projects/{pid}')
    def delete(pid: str):
        backups.delete_project(pid)
        return {'deleted': True}

    dist = Path(__file__).resolve().parent.parent / 'frontend' / 'dist'
    if dist.exists():
        app.mount('/', StaticFiles(directory=dist, html=True), name='frontend')
    else:
        @app.get('/')
        def missing_build():
            return Response('前端尚未构建，请运行 Start.ps1 完成首次准备。', media_type='text/plain')
    return app
