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
import threading

from runtime import load_runtime, parse_source, runtime_version

MAX_FILE_BYTES = 16 << 20
MAX_REQUEST_BYTES = 23 << 20
SLOTS = threading.BoundedSemaphore(2)
PROCESS_CONTEXT = multiprocessing.get_context('spawn')


def parse_child(connection, cache, raw, maximum, language, path):
    try:
        versions = load_runtime(cache)
        connection.send((True, parse_source(raw, maximum, runtime_version(versions), language, path)))
    except Exception:
        # Exceptions may include source text; do not put them in RPC errors/logs.
        connection.send((False, {'error': 'source parsing failed'}))
    finally:
        connection.close()


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
                          'typescript': ('.ts', '.mts', '.cts'), 'tsx': ('.tsx',),
                          'mybatis-xml': ('.xml',)}
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
        parent, child = PROCESS_CONTEXT.Pipe(duplex=False)
        process = PROCESS_CONTEXT.Process(target=parse_child, args=(child, self.server.cache, raw, maximum, language, path))
        try:
            process.start()
            child.close()
            if not parent.poll(6):
                self.respond(504, {'error': 'source parsing time limit exceeded'})
                return
            success, result = parent.recv()
            self.respond(200 if success else 422, result)
        except (EOFError, OSError):
            self.respond(422, {'error': 'source parsing failed'})
        finally:
            if process.is_alive():
                process.terminate()
            if process.pid is not None:
                process.join(timeout=2)
            parent.close()
            child.close()
            SLOTS.release()


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
    parser_version = runtime_version(versions)
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    languages = sorted(versions)
    if 'java' in versions:
        languages = sorted([*languages, 'mybatis-xml'])
    server.cache, server.parser_version, server.versions, server.languages = cache, parser_version, versions, languages
    print(json.dumps({'port': server.server_port}), flush=True)
    server.serve_forever()


if __name__ == '__main__':
    multiprocessing.freeze_support()
    main()
