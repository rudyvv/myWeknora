"""Python contract tests exercise the locked parser through its HTTP boundary."""
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest
import urllib.error
import urllib.request

try:
    from sourceparser.runtime import load_runtime
except ModuleNotFoundError:
    from runtime import load_runtime


class PythonHTTPContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cache = os.environ.get('SOURCE_PARSER_CACHE')
        if not cache:
            raise RuntimeError('SOURCE_PARSER_CACHE must name the prefetched Python grammar cache')
        lock = json.loads((Path(cache) / 'grammar.lock.json').read_text(encoding='utf-8'))
        if 'python' not in lock.get('grammars', {}):
            raise unittest.SkipTest('SOURCE_PARSER_CACHE does not include the locked Python grammar')
        cls.process = subprocess.Popen(
            [sys.executable, str(Path(__file__).parents[1] / 'server.py'), '--host', '127.0.0.1', '--port', '0'],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8')
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

    def parse(self, path, raw, maximum=128):
        return self.request('/v1/parse', {
            'path': path, 'language': 'python', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': maximum})

    def test_health_advertises_locked_python_without_claiming_framework_semantics(self):
        status, health = self.request('/health')
        self.assertEqual(status, 200, health)
        self.assertTrue(health['ready'])
        self.assertIn('python', health['languages'])
        self.assertIn('rules-10' if 'java' in health['languages'] else 'rules-4', health['parser_version'])

    def test_python_parse_artifact_version_matches_updated_health_fingerprint(self):
        status, health = self.request('/health')
        self.assertEqual(status, 200, health)
        status, result = self.parse('pkg/version.py', b'import os\n')
        self.assertEqual(status, 200, result)
        self.assertEqual(result['parser_version'], health['parser_version'])
        versions = load_runtime(os.environ['SOURCE_PARSER_CACHE'])
        self.assertIn('python-pack-1.19.0-rules-4-', versions['python'])
        expected_rules = 'rules-10' if 'java' in health['languages'] else 'rules-4'
        self.assertTrue(health['parser_version'].startswith('source-pack-1.19.0-' + expected_rules + '-'))

    def test_module_class_methods_decorators_async_unicode_and_crlf_keep_parent_ranges(self):
        raw = ('@router.get("/预约")\r\n'
               'class Service:\r\n'
               '    @trace\r\n'
               '    async def reserve(self, 名称: str) -> str:\r\n'
               '        return f"预约 {名称}"\r\n'
               '\r\n'
               '    def helper(self):\r\n'
               '        return None\r\n').encode()
        status, result = self.parse('pkg/service.py', raw)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        self.assertEqual(result['encoding'], 'utf-8')
        names = {symbol['name']: symbol for symbol in result['symbols']}
        self.assertEqual(names['pkg/service.py']['kind'], 'module')
        self.assertEqual(names['Service']['kind'], 'class')
        self.assertEqual(names['reserve']['kind'], 'method')
        self.assertEqual(names['helper']['kind'], 'method')
        self.assertEqual(names['reserve']['qualified_name'], 'pkg/service.py.Service.reserve')
        self.assertEqual(names['reserve']['signature'], '@trace\r\n    async def reserve(self, 名称: str) -> str:')
        self.assertEqual(names['reserve']['annotations'][0]['text'], '@trace')
        self.assertEqual(names['Service']['annotations'][0]['text'], '@router.get("/预约")')
        self.assertEqual(names['reserve']['range']['start_line'], 3)
        self.assertEqual(names['reserve']['annotations'][0]['range']['start_line'], 3)
        for symbol in result['symbols']:
            signature = symbol['signature_range']
            self.assertEqual(raw[signature['start_byte']:signature['end_byte']].decode(), symbol['signature'])
            span = symbol['range']
            self.assertTrue(span['start_byte'] <= span['end_byte'])
            self.assertGreaterEqual(span['start_line'], 1)
        self.assertEqual(''.join(chunk['content'] for chunk in result['chunks']).encode(), raw)
        for chunk in result['chunks']:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])

    def test_syntax_error_preserves_readable_source_and_reports_visible_degradation(self):
        raw = ('\ufeffasync def valid():\r\n'
               '    return "中文😀"\r\n'
               '\r\n'
               'def broken(:\r\n'
               '    return "仍可读取"\r\n').encode()
        status, result = self.parse('pkg/broken.py', raw, 64)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'syntax_error')
        self.assertTrue(all(chunk['quality'] == 'syntax_error' for chunk in result['chunks']))
        self.assertEqual(''.join(chunk['content'] for chunk in result['chunks']).encode(), raw)

    def test_deep_expression_within_limits_preserves_every_original_byte(self):
        expression = ' + '.join(['value'] * 1100)
        raw = ('def deep_expression():\r\n'
               '    return ' + expression + '\r\n').encode()
        status, result = self.parse('pkg/deep_expression.py', raw)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        self.assertEqual(''.join(chunk['content'] for chunk in result['chunks']).encode(), raw)
        self.assertTrue(any(symbol['name'] == 'deep_expression' for symbol in result['symbols']))
        for chunk in result['chunks']:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])

    def test_import_forms_preserve_statement_ranges_and_scope_structure(self):
        raw = ('import os as operating_system, sys\r\n'
               'from pkg.submodule import helper as helper_alias, second\r\n'
               'from ..relative import item\r\n'
               'from pkg.submodule import (\r\n'
               '    first,\r\n'
               '    second as second_alias,\r\n'
               ')\r\n'
               'def use_imports():\r\n'
               '    import pathlib as paths\r\n'
               '    from .local import helper\r\n'
               '    return paths.Path(helper)\r\n').encode()
        status, result = self.parse('pkg/imports.py', raw)
        self.assertEqual(status, 200, result)
        imports = [symbol for symbol in result['symbols'] if symbol['kind'] == 'import']
        expected = [
            ('import os as operating_system, sys', 'pkg/imports.py.os, sys'),
            ('from pkg.submodule import helper as helper_alias, second', 'pkg/imports.py.pkg.submodule:helper, second'),
            ('from ..relative import item', 'pkg/imports.py...relative:item'),
            ('from pkg.submodule import (\r\n    first,\r\n    second as second_alias,\r\n)',
             'pkg/imports.py.pkg.submodule:first, second'),
            ('import pathlib as paths', 'pkg/imports.py.use_imports.pathlib'),
            ('from .local import helper', 'pkg/imports.py.use_imports..local:helper'),
        ]
        self.assertEqual([symbol['signature'] for symbol in imports], [item[0] for item in expected])
        self.assertEqual([symbol['qualified_name'] for symbol in imports], [item[1] for item in expected])
        for symbol in imports:
            span = symbol['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), symbol['signature'])
            signature = symbol['signature_range']
            self.assertEqual(raw[signature['start_byte']:signature['end_byte']].decode(), symbol['signature'])
            self.assertEqual(raw[:span['start_byte']].count(b'\n') + 1, span['start_line'])
            last = max(span['start_byte'], span['end_byte'] - 1)
            self.assertEqual(raw[:last].count(b'\n') + 1, span['end_line'])
        self.assertEqual(''.join(chunk['content'] for chunk in result['chunks']).encode(), raw)

    def test_multiline_strings_keep_unicode_crlf_bytes_without_fabricated_symbols(self):
        raw = ('MESSAGE = """预约😀\r\n'
               'import pretend_module\r\n'
               '@router.get("/fake")\r\n'
               'class FakeFramework:\r\n'
               '    pass\r\n'
               '"""\r\n'
               '@trace\r\n'
               'def actual_function():\r\n'
               '    return MESSAGE\r\n').encode()
        status, result = self.parse('pkg/multiline.py', raw)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        self.assertFalse(any(symbol['kind'] == 'import' for symbol in result['symbols']))
        self.assertFalse(any(symbol['name'] == 'FakeFramework' for symbol in result['symbols']))
        actual = next(symbol for symbol in result['symbols'] if symbol['name'] == 'actual_function')
        self.assertEqual(actual['annotations'][0]['text'], '@trace')
        self.assertEqual(''.join(chunk['content'] for chunk in result['chunks']).encode(), raw)
        for chunk in result['chunks']:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])

    def test_http_resource_limit_is_reported_clearly(self):
        connection = http.client.HTTPConnection(self.url.removeprefix('http://'), timeout=10)
        connection.putrequest('POST', '/v1/parse')
        connection.putheader('Content-Type', 'application/json')
        connection.putheader('Content-Length', str((23 << 20) + 1))
        connection.endheaders()
        response = connection.getresponse()
        body = json.loads(response.read())
        connection.close()
        self.assertEqual(response.status, 413, body)
        self.assertEqual(body['error'], 'request exceeds parser limit')

    def test_python_extension_is_required_and_unknown_framework_file_is_not_relabelled(self):
        raw = b'class Service:\n    pass\n'
        status, response = self.request('/v1/parse', {
            'path': 'pkg/service.txt', 'language': 'python', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode()})
        self.assertEqual(status, 400, response)
        status, response = self.request('/v1/parse', {
            'path': 'pkg/service.py', 'language': 'python', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode()})
        self.assertEqual(status, 200, response)
        self.assertFalse(any(symbol['kind'] == 'framework' for symbol in response['symbols']))


if __name__ == '__main__':
    unittest.main()
