"""Contract checks over HTTP against the real, offline Java runtime."""
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
import urllib.error
import urllib.request


class JavaHTTPContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cache = os.environ.get('SOURCE_PARSER_CACHE')
        if not cache:
            raise RuntimeError('SOURCE_PARSER_CACHE must name the prefetched, locked grammar cache')
        cls.process = subprocess.Popen(
            [sys.executable, str(Path(__file__).parents[1] / 'server.py'), '--host', '127.0.0.1', '--port', '0'],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8',
        )
        startup = cls.process.stdout.readline()
        if not startup:
            raise RuntimeError(cls.process.stderr.read())
        cls.url = 'http://127.0.0.1:' + str(json.loads(startup)['port'])

    @classmethod
    def tearDownClass(cls):
        cls.process.terminate()
        cls.process.wait(timeout=10)
        cls.process.stdout.close()
        cls.process.stderr.close()

    def request(self, path, body=None):
        request = urllib.request.Request(self.url + path, data=None if body is None else json.dumps(body).encode(),
                                         headers={'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as response:
            return response.code, json.load(response)

    def test_java_annotations_and_original_crlf_unicode_ranges(self):
        raw = ('package demo;\r\n// 中文\r\npublic class Service {\r\n @Deprecated\r\n'
               ' public String getPushSchedule(String 名称) {\r\n  return "预约";\r\n }\r\n}\r\n').encode()
        status, health = self.request('/health')
        self.assertEqual(status, 200)
        self.assertTrue(health['ready'])
        self.assertEqual(health['languages'], ['java'])
        status, result = self.request('/v1/parse', {
            'path': 'nsb/src/main/java/demo/Service.java', 'language': 'java',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
            'chunk_max_bytes': 128,
        })
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        self.assertEqual(result['encoding'], 'utf-8')
        self.assertEqual(result['byte_length'], 140)
        method = next(s for s in result['symbols'] if s['name'] == 'getPushSchedule')
        self.assertEqual(method['kind'], 'method')
        self.assertEqual(method['qualified_name'], 'Service.getPushSchedule')
        self.assertEqual(method['signature'], '@Deprecated\r\n public String getPushSchedule(String 名称)')
        self.assertEqual(method['range'], {'start_byte': 51, 'end_byte': 135, 'start_line': 4, 'end_line': 7})
        self.assertEqual(method['annotations'][0]['text'], '@Deprecated')
        self.assertEqual(method['annotations'][0]['range']['start_line'], 4)
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        for chunk in result['chunks']:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])
        self.assertEqual(result['chunks'][0]['range']['start_line'], 1)

    def test_oversized_unicode_structure_keeps_exact_separate_context(self):
        raw = ('class Large { public String getPushSchedule() { return "' + '中文' * 1000 + '"; } }').encode()
        status, result = self.request('/v1/parse', {
            'path': 'Large.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': 128,
        })
        self.assertEqual(status, 200, result)
        self.assertGreater(len(result['chunks']), 10)
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        self.assertLessEqual(max(len(c['content'].encode()) for c in result['chunks']), 128)
        contexts = [context for chunk in result['chunks'] for context in chunk['context']]
        self.assertTrue(any(c['text'] == 'public String getPushSchedule()' for c in contexts))
        for context in contexts:
            span = context['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), context['text'])

    def test_long_signature_context_is_bounded_and_still_verifiable(self):
        raw = ('class Large { @A("' + '中文' * 2000 + '") public String method() { return "ok"; } }').encode()
        status, result = self.request('/v1/parse', {
            'path': 'Large.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': 128,
        })
        self.assertEqual(status, 200, result)
        contexts = [context for chunk in result['chunks'] for context in chunk['context']]
        self.assertTrue(contexts)
        for context in contexts:
            self.assertLessEqual(len(context['text'].encode()), 512)
            span = context['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), context['text'])

    def test_bom_and_syntax_errors_are_reported_without_rewriting_source(self):
        raw = b'\xef\xbb\xbfclass Broken { public void run( {\r\n'
        body = {'path': 'Broken.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
                'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': 128}
        status, result = self.request('/v1/parse', body)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'syntax_error')
        self.assertEqual(result['encoding'], 'utf-8-bom')
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        for extra in ({'path': '../Broken.java'}, {'path': 'C:/Broken.java'}, {'language': 'python'},
                      {'sha256': '0' * 64}, {'access_token': 'must-never-be-accepted'}):
            status, response = self.request('/v1/parse', {**body, **extra})
            self.assertEqual(status, 400, response)
            self.assertNotIn('class Broken', json.dumps(response))
