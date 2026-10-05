"""Internal content/hash-only HTTP worker. It never opens a caller-supplied path."""
import argparse
import base64
import binascii
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
import multiprocessing
import os
from pathlib import PurePosixPath
import select
import shutil
import socket
import signal
import subprocess
import threading
import time
from dataclasses import dataclass

from runtime import (SFCAttributeLimitError, is_text_fallback_path, load_runtime, load_sfc_runtime,
                     parse_source, parse_vue_source, runtime_version)

MAX_FILE_BYTES = 16 << 20
MAX_REQUEST_BYTES = 23 << 20
SLOTS = threading.BoundedSemaphore(2)
PROCESS_CONTEXT = multiprocessing.get_context('spawn')
MAX_RESPONSE_BYTES = 32 << 20


@dataclass(frozen=True)
class ParserLimits:
    max_connections: int = 8
    max_request_bytes: int = MAX_REQUEST_BYTES
    header_timeout_seconds: int = 10
    body_timeout_seconds: int = 10
    parse_workers: int = 2
    parse_timeout_seconds: int = 6
    response_timeout_seconds: int = 10
    max_response_bytes: int = MAX_RESPONSE_BYTES


def load_parser_limits(environ=None):
    """Load bounded deployment limits; malformed settings fail startup closed."""
    environ = os.environ if environ is None else environ
    ranges = {
        'SOURCE_PARSER_MAX_CONNECTIONS': (8, 1, 64),
        'SOURCE_PARSER_MAX_REQUEST_BYTES': (MAX_REQUEST_BYTES, 1024, 32 << 20),
        'SOURCE_PARSER_HEADER_TIMEOUT_SECONDS': (10, 1, 120),
        'SOURCE_PARSER_BODY_TIMEOUT_SECONDS': (10, 1, 120),
        'SOURCE_PARSER_PARSE_WORKERS': (2, 1, 8),
        'SOURCE_PARSER_PARSE_TIMEOUT_SECONDS': (6, 1, 300),
        'SOURCE_PARSER_RESPONSE_TIMEOUT_SECONDS': (10, 1, 120),
        'SOURCE_PARSER_MAX_RESPONSE_BYTES': (MAX_RESPONSE_BYTES, 1024, 64 << 20),
    }
    values = {}
    for name, (default, minimum, maximum) in ranges.items():
        raw = environ.get(name)
        if raw is None or raw == '':
            value = default
        elif not raw.isascii() or not raw.isdecimal():
            raise ValueError('invalid parser resource limit: ' + name)
        else:
            value = int(raw)
        if not minimum <= value <= maximum:
            raise ValueError('invalid parser resource limit: ' + name)
        values[name] = value
    return ParserLimits(
        max_connections=values['SOURCE_PARSER_MAX_CONNECTIONS'],
        max_request_bytes=values['SOURCE_PARSER_MAX_REQUEST_BYTES'],
        header_timeout_seconds=values['SOURCE_PARSER_HEADER_TIMEOUT_SECONDS'],
        body_timeout_seconds=values['SOURCE_PARSER_BODY_TIMEOUT_SECONDS'],
        parse_workers=values['SOURCE_PARSER_PARSE_WORKERS'],
        parse_timeout_seconds=values['SOURCE_PARSER_PARSE_TIMEOUT_SECONDS'],
        response_timeout_seconds=values['SOURCE_PARSER_RESPONSE_TIMEOUT_SECONDS'],
        max_response_bytes=values['SOURCE_PARSER_MAX_RESPONSE_BYTES'],
    )


DEFAULT_LIMITS = ParserLimits()


class ParserHTTPServer(ThreadingHTTPServer):
    """Threaded HTTP server with a finite accepted-connection budget."""

    daemon_threads = True
    block_on_close = False
    request_queue_size = 16

    def __init__(self, server_address, handler_class, limits=None):
        self.limits = limits or load_parser_limits()
        self.request_queue_size = self.limits.max_connections
        self.connection_slots = threading.BoundedSemaphore(self.limits.max_connections)
        self.parse_slots = threading.BoundedSemaphore(self.limits.parse_workers)
        self.parse_timeout = self.limits.parse_timeout_seconds
        super().__init__(server_address, handler_class)

    def process_request(self, request, client_address):
        if not self.connection_slots.acquire(blocking=False):
            body = b'{"error":"HTTP connection capacity reached"}'
            response = (b'HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\n'
                        b'Content-Type: application/json; charset=utf-8\r\nContent-Length: ' +
                        str(len(body)).encode('ascii') + b'\r\n\r\n' + body)
            try:
                request.settimeout(1)
                request.sendall(response)
            except OSError:
                pass
            finally:
                self.shutdown_request(request)
            return
        try:
            super().process_request(request, client_address)
        except BaseException:
            self.connection_slots.release()
            raise

    def process_request_thread(self, request, client_address):
        try:
            super().process_request_thread(request, client_address)
        finally:
            self.connection_slots.release()


class DeadlineSocketIO(socket.SocketIO):
    """Refresh each buffered socket read from one absolute header deadline."""

    def __init__(self, sock, handler):
        super().__init__(sock, 'r')
        self.handler = handler

    def readinto(self, buffer):
        deadline = self.handler.header_deadline
        if deadline is not None:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise socket.timeout('HTTP request headers exceeded their time limit')
            self._sock.settimeout(remaining)
        return super().readinto(buffer)


def parse_child(connection, cache, raw, maximum, language, path, sfc,
                max_response_bytes=MAX_RESPONSE_BYTES):
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
        encoded_result = json.dumps(parsed, ensure_ascii=False, allow_nan=False).encode('utf-8')
        if len(encoded_result) > max_response_bytes:
            result_message = ('result', False, {'error': 'parser response exceeds output limit'})
        else:
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
        return True
    process_group = False
    if os.name != 'nt':
        try:
            process_group = os.getpgid(process.pid) == process.pid
        except ProcessLookupError:
            pass
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
        process.join(timeout=3)
    if process.is_alive():
        return False
    if process_group:
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            try:
                os.killpg(process.pid, 0)
            except ProcessLookupError:
                return True
            except OSError:
                return True
            time.sleep(0.05)
        try:
            os.killpg(process.pid, 0)
        except (ProcessLookupError, OSError):
            return True
        return False
    return True


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def setup(self):
        super().setup()
        limits = getattr(self.server, 'limits', DEFAULT_LIMITS)
        self.header_deadline = None
        self.connection.settimeout(limits.header_timeout_seconds)
        self.rfile.close()
        self.rfile = io.BufferedReader(DeadlineSocketIO(self.connection, self))

    def handle_one_request(self):
        limits = getattr(self.server, 'limits', DEFAULT_LIMITS)
        self.header_deadline = time.monotonic() + limits.header_timeout_seconds
        try:
            super().handle_one_request()
        finally:
            self.header_deadline = None

    def parse_request(self):
        try:
            return super().parse_request()
        finally:
            self.header_deadline = None
            limits = getattr(self.server, 'limits', DEFAULT_LIMITS)
            self.connection.settimeout(limits.body_timeout_seconds)

    def respond(self, status, body):
        limits = getattr(self.server, 'limits', DEFAULT_LIMITS)
        try:
            encoded = json.dumps(body, ensure_ascii=False, allow_nan=False).encode('utf-8')
        except (TypeError, ValueError):
            status, encoded = 422, b'{"error":"parser response is invalid"}'
        if len(encoded) > limits.max_response_bytes:
            status, encoded = 422, b'{"error":"parser response exceeds output limit"}'
        try:
            self.connection.settimeout(limits.response_timeout_seconds)
            self.send_response(status)
            self.send_header('Content-Type', 'application/json; charset=utf-8')
            self.send_header('Content-Length', str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)
            self.wfile.flush()
        except OSError:
            self.close_connection = True

    def read_request_body(self, length):
        """Read exactly Content-Length bytes under one absolute wall-clock deadline."""
        deadline = time.monotonic() + getattr(self.server, 'limits', DEFAULT_LIMITS).body_timeout_seconds
        body = bytearray()
        while len(body) < length:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return None, 408
            self.connection.settimeout(remaining)
            try:
                block = self.rfile.read1(min(64 << 10, length - len(body)))
            except (TimeoutError, socket.timeout):
                return None, 408
            except OSError:
                return None, 400
            if not block:
                return None, 400
            body.extend(block)
        return bytes(body), 200

    def client_disconnected(self):
        """Check for a closed peer without consuming a pipelined next request."""
        try:
            readable, _, _ = select.select([self.connection], [], [], 0)
            if not readable:
                return False
            return self.connection.recv(1, socket.MSG_PEEK) == b''
        except (OSError, ValueError):
            return True

    def wait_for_worker_message(self, connection, process, deadline):
        while True:
            if self.client_disconnected():
                return 'cancelled', None
            if connection.poll(0):
                return 'message', connection.recv()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return 'timeout', None
            if not process.is_alive():
                if connection.poll(0):
                    return 'message', connection.recv()
                return 'crashed', None
            if connection.poll(min(0.05, remaining)):
                return 'message', connection.recv()

    def do_GET(self):
        if self.path != '/health':
            self.respond(404, {'error': 'not found'})
            return
        limits = getattr(self.server, 'limits', DEFAULT_LIMITS)
        self.respond(200 if self.server.parser_version else 503, {
            'ready': bool(self.server.parser_version), 'parser_version': self.server.parser_version,
            'languages': self.server.languages,
            'limits': {
                'max_connections': limits.max_connections,
                'max_request_bytes': limits.max_request_bytes,
                'header_timeout_seconds': limits.header_timeout_seconds,
                'body_timeout_seconds': limits.body_timeout_seconds,
                'parse_workers': limits.parse_workers,
                'parse_timeout_seconds': limits.parse_timeout_seconds,
                'max_response_bytes': limits.max_response_bytes,
            },
        })

    def do_POST(self):
        if self.path != '/v1/parse':
            self.respond(404, {'error': 'not found'})
            return
        if not self.server.parser_version:
            self.respond(503, {'error': 'offline source grammar unavailable'})
            return
        try:
            length_headers = self.headers.get_all('Content-Length', [])
            if (len(length_headers) != 1 or self.headers.get('Transfer-Encoding') is not None or
                    not length_headers[0].isascii() or not length_headers[0].isdecimal()):
                raise ValueError()
            length = int(length_headers[0])
            limits = getattr(self.server, 'limits', DEFAULT_LIMITS)
            if length <= 0:
                self.respond(400, {'error': 'invalid source content/hash request'})
                self.close_connection = True
                return
            if length > limits.max_request_bytes:
                self.respond(413, {'error': 'request exceeds parser limit'})
                self.close_connection = True
                return
            body_bytes, read_status = self.read_request_body(length)
            if body_bytes is None:
                if read_status == 408:
                    self.respond(408, {'error': 'request body time limit exceeded'})
                else:
                    self.respond(400, {'error': 'incomplete source request body'})
                self.close_connection = True
                return
            body = json.loads(body_bytes)
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
            available = language in self.server.versions or language == 'text' or (
                language == 'mybatis-xml' and 'java' in self.server.versions)
            path_matches = (is_text_fallback_path(path) if language == 'text'
                            else path.lower().endswith(extensions.get(language, ())))
            if not available or not path_matches:
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
        except (ValueError, TypeError, KeyError, UnicodeError, binascii.Error, RecursionError):
            self.respond(400, {'error': 'invalid source content/hash request'})
            return
        parse_slots = getattr(self.server, 'parse_slots', SLOTS)
        if not parse_slots.acquire(blocking=False):
            self.respond(429, {'error': 'parser capacity reached'})
            return
        parent = child = process = None
        started = False
        cleanup_complete = True
        send_response = True
        response_status, response_body = 422, {'error': 'source parsing failed'}
        try:
            parent, child = PROCESS_CONTEXT.Pipe(duplex=True)
            worker = getattr(self.server, 'parser_target', parse_child)
            limits = getattr(self.server, 'limits', DEFAULT_LIMITS)
            process = PROCESS_CONTEXT.Process(target=worker, args=(
                child, self.server.cache, raw, maximum, language, path, self.server.sfc,
                limits.max_response_bytes))
            deadline = time.monotonic() + getattr(self.server, 'parse_timeout', 6)
            process.start()
            started = True
            child.close()
            state, message = self.wait_for_worker_message(parent, process, deadline)
            if state == 'timeout':
                response_status, response_body = 504, {'error': 'source parsing time limit exceeded'}
            elif state == 'cancelled':
                send_response = False
            elif state == 'message' and message[0] == 'ready':
                state, message = self.wait_for_worker_message(parent, process, deadline)
                if state == 'timeout':
                    response_status, response_body = 504, {'error': 'source parsing time limit exceeded'}
                elif state == 'cancelled':
                    send_response = False
                elif state == 'message' and message[0] == 'result':
                    _, success, result = message
                    response_status, response_body = (200 if success else 422), result
        except (EOFError, OSError):
            response_status, response_body = 422, {'error': 'source parsing failed'}
        finally:
            try:
                if started:
                    cleanup_complete = terminate_parser_process_tree(process)
            finally:
                if parent is not None:
                    parent.close()
                if child is not None:
                    child.close()
                if cleanup_complete:
                    parse_slots.release()
        if send_response:
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
    try:
        limits = load_parser_limits()
    except ValueError as error:
        raise SystemExit('source parser resource configuration is invalid') from error
    server = ParserHTTPServer((args.host, args.port), Handler, limits)
    languages = sorted([*versions, 'text']) if versions else []
    if 'java' in versions:
        languages = sorted([*languages, 'mybatis-xml'])
    server.cache, server.parser_version, server.versions, server.languages, server.sfc = cache, parser_version, versions, languages, sfc
    server.parse_timeout = limits.parse_timeout_seconds
    print(json.dumps({'port': server.server_port}), flush=True)
    server.serve_forever()


if __name__ == '__main__':
    multiprocessing.freeze_support()
    main()
