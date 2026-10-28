"""Isolated browser test host. Never used by the production launcher."""
import sys
import tempfile
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import uvicorn
from studio.api import create_app
from test_worker import ScriptedProvider

root = Path(__file__).resolve().parents[1] / '.test-tmp'
root.mkdir(exist_ok=True)
data = Path(tempfile.mkdtemp(prefix='browser-', dir=root))
app = create_app(data, provider=ScriptedProvider(), port=8766)
uvicorn.run(app, host='127.0.0.1', port=8766, access_log=False)
