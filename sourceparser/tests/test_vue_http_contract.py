"""Vue SFC contract tests use the same locked HTTP worker as production."""
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

try:
    from sourceparser.runtime import load_runtime, load_sfc_runtime
except ModuleNotFoundError:
    from runtime import load_runtime, load_sfc_runtime


class VueHTTPContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cache = os.environ.get('SOURCE_PARSER_CACHE')
        if not cache:
            raise unittest.SkipTest('SOURCE_PARSER_CACHE must name the prefetched grammar cache')
        try:
            versions = load_runtime(cache)
        except Exception as error:
            raise unittest.SkipTest('locked Tree-sitter runtime is unavailable: ' + str(error))
        if not {'javascript', 'typescript'}.issubset(versions) or not load_sfc_runtime():
            raise unittest.SkipTest('locked Vue, JavaScript, and TypeScript runtimes are required')
        cls.process = subprocess.Popen(
            [sys.executable, str(Path(__file__).parents[1] / 'server.py'), '--host', '127.0.0.1', '--port', '0'],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8')
        startup = cls.process.stdout.readline()
        if not startup:
            raise RuntimeError(cls.process.stderr.read())
        cls.url = 'http://127.0.0.1:' + str(json.loads(startup)['port'])

    @classmethod
    def tearDownClass(cls):
        process = getattr(cls, 'process', None)
        if process is not None:
            process.terminate()
            process.wait(timeout=10)
            process.stdout.close()
            process.stderr.close()

    def request(self, endpoint, body=None):
        request = urllib.request.Request(self.url + endpoint,
                                         data=None if body is None else json.dumps(body).encode(),
                                         headers={'Content-Type': 'application/json'})
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as response:
            return response.code, json.load(response)

    def parse(self, raw, maximum=128):
        return self.request('/v1/parse', {
            'path': 'src/BookingPanel.vue', 'language': 'vue',
            'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': maximum})

    def test_vue_health_and_sfc_artifact_share_the_verified_fingerprint(self):
        status, health = self.request('/health')
        self.assertEqual(status, 200, health)
        self.assertIn('vue', health['languages'])
        self.assertIn('rules-4-', health['parser_version'])
        status, parsed = self.parse(b'<template><div>ok</div></template>\r\n')
        self.assertEqual(status, 200, parsed)
        self.assertEqual(parsed['parser_version'], health['parser_version'])

    def test_static_vue2_blocks_and_typed_script_preserve_original_utf8_crlf_ranges(self):
        raw = ('<!-- 😀 before -->\r\n'
               '<template lang="html">\r\n'
               '  <div><button v-on:click.native="reserve">预约😀</button><slot name="legacy" slot-scope="p">{{ p.value }}</slot></div>\r\n'
               '</template>\r\n'
               '<script lang="ts">\r\n'
               'import { send } from "./api";\r\n'
               'export default { methods: { reserve(id: string) { return send(id); } } };\r\n'
               '</script>\r\n'
               '<style lang="scss" scoped>\r\n.预约 { color: red; }\r\n</style>\r\n'
               '<i18n lang="json">\r\n{"title":"🧭"}\r\n</i18n>\r\n').encode()
        status, parsed = self.parse(raw, 96)
        self.assertEqual(status, 200, parsed)
        self.assertEqual(parsed['quality'], 'structural')
        self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)
        regions = [symbol for symbol in parsed['symbols'] if symbol['kind'] == 'sfc_region']
        self.assertEqual([symbol['region']['kind'] for symbol in regions],
                         ['template', 'script', 'style', 'custom'])
        reserve = next(symbol for symbol in parsed['symbols'] if symbol['name'] == 'reserve')
        self.assertEqual(reserve['region']['kind'], 'script')
        self.assertEqual(reserve['range']['start_line'], 7)
        for chunk in parsed['chunks']:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])
            self.assertEqual(raw[:span['start_byte']].count(b'\n') + 1, span['start_line'])
            line_end = max(span['start_byte'], span['end_byte'] - 1)
            self.assertEqual(raw[:line_end].count(b'\n') + 1, span['end_line'])
        self.assertTrue(any((chunk.get('region') or {}).get('kind') == 'style' for chunk in parsed['chunks']))
        self.assertTrue(any(chunk.get('region') is None and '<template' in chunk['content']
                            for chunk in parsed['chunks']))

    def test_external_script_references_are_literal_and_never_loaded(self):
        for source, status_value in (('./api.js', 'unchecked'), ('../private.js', 'rejected'),
                                     ('/absolute.js', 'rejected')):
            raw = ('<script src="' + source + '"></script>\r\n').encode()
            with self.subTest(source=source):
                status, parsed = self.parse(raw)
                self.assertEqual(status, 200, parsed)
                marker = next(symbol for symbol in parsed['symbols'] if symbol['kind'] == 'sfc_region')
                self.assertEqual(marker['region']['external_source'], source)
                self.assertEqual(marker['region']['external_status'], status_value)
                self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)

    def test_unknown_preprocessor_and_descriptor_diagnostics_remain_readable(self):
        raw = ('<style lang="mystery-style">\r\nraw 😀\r\n</style>\r\n'
               '<template><div>first</div></template>\r\n'
               '<template><div>duplicate</div></template>\r\n').encode()
        status, parsed = self.parse(raw)
        self.assertEqual(status, 200, parsed)
        self.assertEqual(parsed['quality'], 'unknown_preprocess')
        self.assertTrue(any(symbol['kind'] == 'sfc_diagnostic' for symbol in parsed['symbols']))
        self.assertTrue(any(chunk['quality'] == 'unknown_preprocess' for chunk in parsed['chunks']))
        self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)

    def test_closing_script_text_cannot_drop_or_rewrite_original_file_bytes(self):
        raw = b'<script>const text = "</script>";\r\nexport default { name: "A" };\r\n</script>\r\n'
        status, parsed = self.parse(raw)
        self.assertEqual(status, 200, parsed)
        self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)
        for chunk in parsed['chunks']:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])
