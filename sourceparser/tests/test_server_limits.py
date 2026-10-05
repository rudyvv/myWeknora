"""Real HTTP admission, request and child-worker bounds."""
import hashlib
import json
import os
from pathlib import Path
import socket
import sys
import threading
import time
import unittest
import base64

from unittest.mock import patch

PARSER_ROOT = Path(__file__).parents[1]
sys.path.insert(0, str(PARSER_ROOT))
try:
    import server as parser_server
finally:
    sys.path.remove(str(PARSER_ROOT))


def _slow_worker(connection, *_args):
    connection.send(('ready',))
    time.sleep(10)


def _crashed_worker(connection, *_args):
    connection.close()
    os._exit(7)


def _oversized_worker(connection, *_args):
    connection.send(('ready',))
    connection.send(('result', True, {'result': 'x' * 4096}))
    connection.recv()


def _successful_worker(connection, *_args):
    connection.send(('ready',))
    connection.send(('result', True, {'ok': True}))
    connection.recv()


class ParserHTTPResourceLimits(unittest.TestCase):
    def setUp(self):
        self.limits = parser_server.ParserLimits(
            max_connections=4,
            max_request_bytes=1024,
            header_timeout_seconds=2,
            body_timeout_seconds=1,
            parse_workers=1,
            parse_timeout_seconds=2,
            response_timeout_seconds=2,
            max_response_bytes=512,
        )
        self.server = parser_server.ParserHTTPServer(('127.0.0.1', 0), parser_server.Handler, self.limits)
        self.server.cache = os.environ.get('SOURCE_PARSER_CACHE', '')
        self.server.parser_version = 'test-parser'
        self.server.versions = {'typescript': 'test-typescript-runtime'}
        self.server.languages = ['typescript']
        self.server.sfc = None
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)

    @property
    def address(self):
        return self.server.server_address

    def _read_response(self, connection):
        stream = connection.makefile('rb')
        status = int(stream.readline().split()[1])
        headers = {}
        while True:
            line = stream.readline()
            if line in (b'\r\n', b'\n', b''):
                break
            name, value = line.decode('latin-1').split(':', 1)
            headers[name.lower()] = value.strip()
        body = stream.read(int(headers.get('content-length', '0')))
        return status, json.loads(body) if body else None

    def _post(self, worker=None, timeout=5):
        raw = b'export const answer = 42;'
        body = json.dumps({
            'path': 'src/answer.ts', 'language': 'typescript',
            'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        }).encode()
        request = (b'POST /v1/parse HTTP/1.1\r\nHost: localhost\r\n'
                   b'Content-Type: application/json\r\nContent-Length: ' + str(len(body)).encode() +
                   b'\r\nConnection: close\r\n\r\n' + body)
        connection = socket.create_connection(self.address, timeout=timeout)
        connection.settimeout(timeout)
        if worker is not None:
            self.server.parser_target = worker
        connection.sendall(request)
        try:
            return self._read_response(connection)
        finally:
            connection.close()

    def _get_health(self):
        with socket.create_connection(self.address, timeout=3) as connection:
            connection.settimeout(3)
            connection.sendall(b'GET /health HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n')
            return self._read_response(connection)

    def test_connection_cap_rejects_excess_and_reclaims_after_slow_body(self):
        self.server.limits = parser_server.ParserLimits(
            max_connections=1, max_request_bytes=1024,
            header_timeout_seconds=2, body_timeout_seconds=1,
            parse_workers=1, parse_timeout_seconds=2,
            response_timeout_seconds=2, max_response_bytes=512)
        self.server.connection_slots = threading.BoundedSemaphore(1)
        first = socket.create_connection(self.address, timeout=3)
        first.settimeout(3)
        first.sendall(b'POST /v1/parse HTTP/1.1\r\nHost: localhost\r\nContent-Length: 10\r\n\r\nx')
        try:
            status, body = 200, None
            deadline = time.monotonic() + 0.8
            while status != 503 and time.monotonic() < deadline:
                status, body = self._get_health()
                if status != 503:
                    time.sleep(0.02)
            self.assertEqual((status, body), (503, {'error': 'HTTP connection capacity reached'}))
            status, body = self._read_response(first)
            self.assertEqual((status, body), (408, {'error': 'request body time limit exceeded'}))
            self.assertEqual(self._get_health()[0], 200, 'the accepted connection slot must be reclaimed')
        finally:
            first.close()

    def test_body_deadline_is_absolute_while_client_drips_bytes(self):
        first = socket.create_connection(self.address, timeout=3)
        first.settimeout(3)
        first.sendall(b'POST /v1/parse HTTP/1.1\r\nHost: localhost\r\nContent-Length: 10\r\n\r\nx')
        try:
            time.sleep(0.4)
            first.sendall(b'x')
            time.sleep(0.4)
            first.sendall(b'x')
            time.sleep(0.4)
            self.assertEqual(self._read_response(first), (408, {'error': 'request body time limit exceeded'}))
            self.assertEqual(self._get_health()[0], 200)
        finally:
            first.close()

    def test_header_deadline_is_absolute_while_client_drips_bytes_and_releases_slot(self):
        self.server.limits = parser_server.ParserLimits(
            max_connections=1, max_request_bytes=1024,
            header_timeout_seconds=1, body_timeout_seconds=1,
            parse_workers=1, parse_timeout_seconds=2,
            response_timeout_seconds=2, max_response_bytes=512)
        self.server.connection_slots = threading.BoundedSemaphore(1)
        first = socket.create_connection(self.address, timeout=3)
        first.settimeout(0.1)
        first.sendall(b'GET /health HTTP/1.1\r\nHost: localhost\r\nX-Probe: ')
        stop_drip = threading.Event()

        def drip_header_bytes():
            while not stop_drip.wait(0.3):
                try:
                    first.sendall(b'x')
                except OSError:
                    return

        dripper = threading.Thread(target=drip_header_bytes, daemon=True)
        started = time.monotonic()
        dripper.start()
        peer_closed = False
        try:
            deadline = started + 3
            while time.monotonic() < deadline:
                try:
                    if not first.recv(1):
                        peer_closed = True
                        break
                except socket.timeout:
                    continue
                except OSError:
                    peer_closed = True
                    break
            elapsed = time.monotonic() - started
            self.assertTrue(peer_closed, 'dripped header bytes must not extend the absolute deadline')
            self.assertLess(elapsed, 2, 'header deadline should expire near its configured one second')
            self.assertEqual(self._get_health()[0], 200,
                             'the timed-out header must release its accepted connection slot')
        finally:
            stop_drip.set()
            dripper.join(timeout=1)
            first.close()

    def test_deeply_nested_json_returns_generic_bad_request_and_releases_connection(self):
        self.server.limits = parser_server.ParserLimits(
            max_connections=1, max_request_bytes=8192,
            header_timeout_seconds=2, body_timeout_seconds=2,
            parse_workers=1, parse_timeout_seconds=2,
            response_timeout_seconds=2, max_response_bytes=512)
        self.server.connection_slots = threading.BoundedSemaphore(1)
        body = b'[' * 3000 + b'0' + b']' * 3000
        self.assertEqual(len(body), 6001)
        self.assertLessEqual(len(body), self.server.limits.max_request_bytes)
        request = (b'POST /v1/parse HTTP/1.1\r\nHost: localhost\r\n'
                   b'Content-Type: application/json\r\nContent-Length: 6001\r\n'
                   b'Connection: close\r\n\r\n' + body)
        connection = socket.create_connection(self.address, timeout=3)
        connection.settimeout(3)
        try:
            connection.sendall(request)
            self.assertEqual(self._read_response(connection),
                             (400, {'error': 'invalid source content/hash request'}))
        finally:
            connection.close()
        self.assertEqual(self._get_health()[0], 200,
                         'a malformed JSON request must not retain the accepted connection slot')

    def test_declared_oversized_body_is_rejected_before_reading(self):
        first = socket.create_connection(self.address, timeout=3)
        first.settimeout(3)
        first.sendall(b'POST /v1/parse HTTP/1.1\r\nHost: localhost\r\nContent-Length: 1025\r\n\r\n')
        try:
            self.assertEqual(self._read_response(first), (413, {'error': 'request exceeds parser limit'}))
            self.assertEqual(self._get_health()[0], 200)
        finally:
            first.close()

    def test_oversized_child_response_is_replaced_with_bounded_error(self):
        status, body = self._post(_oversized_worker)
        self.assertEqual((status, body), (422, {'error': 'parser response exceeds output limit'}))
        self.assertLessEqual(len(json.dumps(body).encode()), self.limits.max_response_bytes)
        self.assertEqual(self._get_health()[0], 200)

    def test_crashed_child_returns_generic_error_and_reclaims_worker_slot(self):
        status, body = self._post(_crashed_worker)
        self.assertEqual((status, body), (422, {'error': 'source parsing failed'}))
        self.assertTrue(self.server.parse_slots.acquire(timeout=1))
        self.server.parse_slots.release()

    def test_timeout_kills_worker_before_releasing_capacity(self):
        self.server.limits = parser_server.ParserLimits(
            max_connections=4, max_request_bytes=1024,
            header_timeout_seconds=2, body_timeout_seconds=1,
            parse_workers=1, parse_timeout_seconds=1,
            response_timeout_seconds=2, max_response_bytes=512)
        self.server.parse_slots = __import__('threading').BoundedSemaphore(1)
        status, body = self._post(_slow_worker)
        self.assertEqual((status, body), (504, {'error': 'source parsing time limit exceeded'}))
        self.assertTrue(self.server.parse_slots.acquire(timeout=1))
        self.server.parse_slots.release()
        self.assertEqual(self._get_health()[0], 200)

    def test_worker_saturation_is_rejected_and_capacity_returns_after_timeout(self):
        self.server.limits = parser_server.ParserLimits(
            max_connections=4, max_request_bytes=1024,
            header_timeout_seconds=2, body_timeout_seconds=1,
            parse_workers=1, parse_timeout_seconds=3,
            response_timeout_seconds=2, max_response_bytes=512)
        self.server.parse_timeout = 3
        self.server.parse_slots = threading.BoundedSemaphore(1)
        first_response = []
        first = threading.Thread(target=lambda: first_response.append(self._post(_slow_worker)), daemon=True)
        first.start()
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            if not self.server.parse_slots.acquire(blocking=False):
                break
            self.server.parse_slots.release()
            time.sleep(0.01)
        else:
            self.fail('the first request did not reserve its parser worker slot')
        self.assertEqual(self._post(), (429, {'error': 'parser capacity reached'}))
        first.join(timeout=6)
        self.assertFalse(first.is_alive(), 'timed-out parser request must finish')
        self.assertEqual(first_response, [(504, {'error': 'source parsing time limit exceeded'})])
        self.assertEqual(self._post(_successful_worker), (200, {'ok': True}))

    def test_client_disconnect_cancels_worker_and_reclaims_capacity(self):
        raw = b'export const answer = 42;'
        body = json.dumps({
            'path': 'src/answer.ts', 'language': 'typescript',
            'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        }).encode()
        self.server.parser_target = _slow_worker
        connection = socket.create_connection(self.address, timeout=3)
        connection.sendall(b'POST /v1/parse HTTP/1.1\r\nHost: localhost\r\nContent-Length: ' +
                           str(len(body)).encode() + b'\r\nConnection: close\r\n\r\n' + body)
        connection.close()
        self.assertTrue(self.server.parse_slots.acquire(timeout=3), 'client cancellation must stop and reap its worker')
        self.server.parse_slots.release()
        self.assertEqual(self._get_health()[0], 200)

    def test_invalid_limit_configuration_fails_closed(self):
        for name, value in (
            ('SOURCE_PARSER_MAX_CONNECTIONS', '0'),
            ('SOURCE_PARSER_PARSE_WORKERS', 'many'),
            ('SOURCE_PARSER_MAX_RESPONSE_BYTES', str(128 << 20)),
        ):
            with self.subTest(name=name), patch.dict(os.environ, {name: value}):
                with self.assertRaisesRegex(ValueError, 'invalid parser resource limit'):
                    parser_server.load_parser_limits()


if __name__ == '__main__':
    unittest.main()
