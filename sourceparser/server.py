"""Internal content/hash-only HTTP worker. It never opens a caller-supplied path."""
import argparse
import base64
import binascii
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import multiprocessing
import os
from pathlib import PurePosixPath
import shutil
import signal
import subprocess
import threading
import time

from runtime import SFCAttributeLimitError, load_runtime, load_sfc_runtime, parse_source, parse_vue_source, runtime_version

MAX_FILE_BYTES = 16 << 20
MAX_REQUEST_BYTES = 23 << 20
SLOTS = threading.BoundedSemaphore(2)
PROCESS_CONTEXT = multiprocessing.get_context('spawn')


def parse_child(connection, cache, raw, maximum, language, path, sfc):
    result_message = None
    try:
        if os.name != 'nt':
            os.setsid()
        connection.send(('ready',))
        versions = load_runtime(cache)
        if sfc and {'javascript', 'typescript'}.issubset(versions):
            versions['vue'] = sfc['runtime']
        if language == 'vue':
            if not sfc or 'vue' not in versions:
                raise RuntimeError('Vue parser runtime unavailable')
            parsed = parse_vue_source(raw, maximum, runtime_version(versions), path, sfc, set(versions))
        else:
            parsed = parse_source(raw, maximum, runtime_version(versions), language, path)
        result_message = ('result', True, parsed)
    except SFCAttributeLimitError:
        result_message = ('result', False, {'error': 'sfc_attribute_limit'})
    except Exception:
        # Exceptions may include source text; do not put them in RPC errors/logs.
        result_message = ('result', False, {'error': 'source parsing failed'})
    try:
        if result_message is not None:
            connection.send(result_message)
            # Keep the worker alive until the HTTP parent tears down this request's
            # process tree, so descendants remain addressable on every platform.
            connection.recv()
    except (EOFError, OSError):
        pass
    finally:
        connection.close()


def terminate_parser_process_tree(process):
    """Stop only this request's parser worker and its descendants before releasing its slot."""
    if process.pid is None:
        return
    if os.name == 'nt':
        taskkill = shutil.which('taskkill')
        system_root = os.environ.get('SystemRoot')
        if not taskkill and system_root:
            candidate = os.path.join(system_root, 'System32', 'taskkill.exe')
            if os.path.isfile(candidate):
                taskkill = candidate
        if taskkill:
            try:
                subprocess.run([taskkill, '/PID', str(process.pid), '/T', '/F'],
                               stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL, timeout=3, check=False)
            except (OSError, subprocess.SubprocessError):
                pass
        if process.is_alive():
            process.terminate()
    else:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            if process.is_alive():
                process.terminate()
        except OSError:
            if process.is_alive():
                process.terminate()
    process.join(timeout=3)
    if process.is_alive():
        process.kill()
        process.join()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def setup(self):
        super().setup()
        self.connection.settimeout(10)

    def respond(self, status, body):
        encoded = json.dumps(body, ensure_ascii=False).encode('utf-8')
        self.send_response(status)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Content-Length', str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        if self.path != '/health':
            self.respond(404, {'error': 'not found'})
            return
        self.respond(200 if self.server.parser_version else 503, {
            'ready': bool(self.server.parser_version), 'parser_version': self.server.parser_version,
            'languages': self.server.languages,
        })

    def do_POST(self):
        if self.path != '/v1/parse':
            self.respond(404, {'error': 'not found'})
            return
        if not self.server.parser_version:
            self.respond(503, {'error': 'offline source grammar unavailable'})
            return
        try:
            length = int(self.headers.get('Content-Length', '0'))
            if not 0 < length <= MAX_REQUEST_BYTES:
                self.respond(413, {'error': 'request exceeds parser limit'})
                self.close_connection = True
                return
            body = json.loads(self.rfile.read(length))
            allowed = {'path', 'language', 'sha256', 'content_base64', 'chunk_max_bytes'}
            if not isinstance(body, dict) or set(body) - allowed:
                raise ValueError()
            path = body['path']
            if not isinstance(path, str) or not path or len(path.encode()) > 4096 or path.startswith('/') or any(c in path for c in '\\:\x00\r\n') or '..' in PurePosixPath(path).parts:
                raise ValueError()
            language = body['language']
            extensions = {'java': ('.java',), 'javascript': ('.js', '.jsx', '.mjs', '.cjs'),
                          'typescript': ('.ts', '.mts', '.cts'), 'tsx': ('.tsx',), 'python': ('.py',),
                          'mybatis-xml': ('.xml',), 'vue': ('.vue',)}
            available = language in self.server.versions or (language == 'mybatis-xml' and 'java' in self.server.versions)
            if not available or not path.lower().endswith(extensions[language]):
                raise ValueError()
            raw = base64.b64decode(body['content_base64'], validate=True)
            if len(raw) > MAX_FILE_BYTES or hashlib.sha256(raw).hexdigest() != body['sha256']:
                raise ValueError()
            raw.decode('utf-8', errors='strict')
            if b'\x00' in raw:
                raise ValueError()
            maximum = body.get('chunk_max_bytes', 4096)
            if type(maximum) is not int or not 64 <= maximum <= 65536:
                raise ValueError()
        except (ValueError, TypeError, KeyError, UnicodeError, binascii.Error):
            self.respond(400, {'error': 'invalid source content/hash request'})
            return
        if not SLOTS.acquire(blocking=False):
            self.respond(429, {'error': 'parser capacity reached'})
            return
        parent = child = process = None
        started = False
        response_status, response_body = 422, {'error': 'source parsing failed'}
        try:
            parent, child = PROCESS_CONTEXT.Pipe(duplex=True)
            process = PROCESS_CONTEXT.Process(target=parse_child, args=(child, self.server.cache, raw, maximum, language, path, self.server.sfc))
            deadline = time.monotonic() + getattr(self.server, 'parse_timeout', 6)
            process.start()
            started = True
            child.close()
            remaining = max(0, deadline - time.monotonic())
            if not parent.poll(remaining):
                response_status, response_body = 504, {'error': 'source parsing time limit exceeded'}
            else:
                message = parent.recv()
                if message[0] == 'ready':
                    remaining = max(0, deadline - time.monotonic())
                    if not parent.poll(remaining):
                        response_status, response_body = 504, {'error': 'source parsing time limit exceeded'}
                        message = None
                    else:
                        message = parent.recv()
                if message is not None and message[0] == 'result':
                    _, success, result = message
                    response_status, response_body = (200 if success else 422), result
        except (EOFError, OSError):
            response_status, response_body = 422, {'error': 'source parsing failed'}
        finally:
            try:
                if started:
                    terminate_parser_process_tree(process)
            finally:
                if parent is not None:
                    parent.close()
                if child is not None:
                    child.close()
                SLOTS.release()
        self.respond(response_status, response_body)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--host', default='0.0.0.0')
    parser.add_argument('--port', type=int, default=8081)
    args = parser.parse_args()
    cache = os.environ.get('SOURCE_PARSER_CACHE', '/opt/source-parser/grammar')
    try:
        versions = load_runtime(cache)
    except Exception:
        versions = {}
    sfc = load_sfc_runtime()
    if sfc and {'javascript', 'typescript'}.issubset(versions):
        versions['vue'] = sfc['runtime']
    else:
        sfc = None
    parser_version = runtime_version(versions)
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    languages = sorted(versions)
    if 'java' in versions:
        languages = sorted([*languages, 'mybatis-xml'])
    server.cache, server.parser_version, server.versions, server.languages, server.sfc = cache, parser_version, versions, languages, sfc
    print(json.dumps({'port': server.server_port}), flush=True)
    server.serve_forever()


if __name__ == '__main__':
    multiprocessing.freeze_support()
    main()
