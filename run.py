"""Single-process local launcher; never binds a public interface."""
import argparse
import os
import threading
import webbrowser
from pathlib import Path
import uvicorn
from studio.api import create_app


def acquire_lock(data):
    data.mkdir(parents=True, exist_ok=True)
    handle = (data / '.instance.lock').open('a+b')
    handle.seek(0)
    handle.write(b'0')
    handle.flush()
    handle.seek(0)
    try:
        if os.name == 'nt':
            import msvcrt
            msvcrt.locking(handle.fileno(), msvcrt.LK_NBLCK, 1)
        else:
            import fcntl
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        handle.close()
        raise SystemExit('这个数据目录已由另一个工作台打开。请使用已有窗口。')
    return handle


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--port', type=int, default=8765)
    parser.add_argument('--data', type=Path, default=Path(__file__).parent / 'data')
    parser.add_argument('--no-browser', action='store_true')
    args = parser.parse_args()
    try:
        instance_lock = acquire_lock(args.data.resolve())
    except SystemExit as exc:
        if args.no_browser:
            raise
        print(str(exc))
        webbrowser.open(f'http://127.0.0.1:{args.port}')
        raise SystemExit(0)
    app = create_app(args.data.resolve(), port=args.port)
    if not args.no_browser:
        threading.Timer(1.2, lambda: webbrowser.open(f'http://127.0.0.1:{args.port}')).start()
    uvicorn.run(app, host='127.0.0.1', port=args.port, access_log=False)
