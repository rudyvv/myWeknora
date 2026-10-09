"""Regression checks through the real HTTP worker, with exact source evidence."""
import base64
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import unittest
import urllib.request
from runtime import TEXT_EXTENSIONS


class SemanticChunkingContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.worker = subprocess.Popen(
            [sys.executable, str(Path(__file__).parents[1] / 'server.py'), '--port', '0'],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8')
        cls.url = 'http://127.0.0.1:' + str(json.loads(cls.worker.stdout.readline())['port'])

    @classmethod
    def tearDownClass(cls):
        cls.worker.terminate()
        cls.worker.wait(timeout=10)
        cls.worker.stdout.close()
        cls.worker.stderr.close()

    def parse(self, path, language, text, budget=512):
        raw = text.encode()
        body = dict(path=path, language=language, sha256=hashlib.sha256(raw).hexdigest(),
                    content_base64=base64.b64encode(raw).decode(), chunk_max_bytes=budget)
        with urllib.request.urlopen(urllib.request.Request(self.url + '/v1/parse',
                data=json.dumps(body).encode(), headers={'Content-Type': 'application/json'})) as response:
            result = json.load(response)
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        cursor = 0
        for c in result['chunks']:
            r = c['range']
            self.assertEqual(r['start_byte'], cursor)
            self.assertEqual(raw[r['start_byte']:r['end_byte']].decode(), c['content'])
            self.assertLessEqual(r['end_byte'] - r['start_byte'], budget)
            self.assertEqual(r['start_line'], raw[:r['start_byte']].count(b'\n') + 1)
            self.assertEqual(r['end_line'], raw[:r['end_byte'] - 1].count(b'\n') + 1)
            cursor = r['end_byte']
        self.assertEqual(cursor, len(raw))
        return result

    def assert_meaningful(self, parsed):
        fragments = {'let', 'const', 'export', 'store =', 'new Vuex.Store', '(', ')', '{', '}', ';', 'await', 'return', '() =>'}
        for c in parsed['chunks']:
            self.assertTrue(c['content'].strip(), 'pure whitespace must attach to a source unit')
            self.assertNotIn(c['content'].strip(), fragments)

    def test_declarations_keep_prefixes_and_delimiters_with_body(self):
        cases = [
            ('store.js', 'javascript', 'let store = new Vuex.Store({state: {\n' + ''.join(f'field{i}: {i},\n' for i in range(200)) + '}});\nexport default store;'),
            ('values.ts', 'typescript', 'export const values = {\n' + ''.join(f'field{i}: {i},\n' for i in range(200)) + '};'),
            ('view.tsx', 'tsx', 'export const View = () => <section>\n' + ''.join(f'<p>item{i}</p>\n' for i in range(200)) + '</section>;'),
            ('Sample.java', 'java', 'class Sample { int calculate(int x) {\n' + ''.join(f'x += {i};\n' for i in range(200)) + 'return x;\n}}'),
            ('sample.py', 'python', '@decorator\nasync def calculate(x):\n' + ''.join(f'    x += {i}\n' for i in range(200)) + '    return x\n'),
        ]
        for path, language, text in cases:
            for budget in (64, 512, 4096):
                with self.subTest(path=path, budget=budget):
                    self.assert_meaningful(self.parse(path, language, text, budget))

    def test_mybatis_whitespace_attaches_to_statements(self):
        text = '<mapper namespace="demo.Mapper">\n' + ''.join(
            f'<select id="s{i}">SELECT {i}</select>\n' for i in range(100)) + '</mapper>\n'
        parsed = self.parse('Mapper.xml', 'mybatis-xml', text)
        self.assert_meaningful(parsed)
        self.assertEqual(sum(f['kind'] == 'mybatis_statement' for f in parsed['facts']), 100)

    def test_hidden_env_filename(self):
        self.assertEqual(self.parse('.env', 'text', 'NAME=value\r\n')['quality'], 'text_fallback')

    def test_long_lexical_units_are_partial(self):
        for path, language, text in [
            ('long.js', 'javascript', 'export const value = "' + '中文😀' * 800 + '";'),
            ('long.py', 'python', 'value = """\n' + '中文😀\n' * 800 + '"""'),
        ]:
            with self.subTest(path=path):
                self.assertEqual(self.parse(path, language, text)['quality'], 'partial')

    def test_small_complete_files_remain_single_chunk(self):
        for path, language, text in [('small.js', 'javascript', 'export const small = {value: 1};'),
                                     ('small.py', 'python', '@decorator\ndef small(x):\n    return x\n')]:
            self.assertEqual(len(self.parse(path, language, text)['chunks']), 1)

    def test_vue_normal_wrappers_attach_to_their_own_region(self):
        text = ('<template><p>中文😀</p></template>\r\n'
                '<script>let store = new Vuex.Store({state: {\n' +
                ''.join(f'field{i}: {i},\n' for i in range(200)) +
                '}});</script>\r\n<style>p {color: red;}</style>\r\n')
        for budget in (64, 512, 4096):
            with self.subTest(budget=budget):
                parsed = self.parse('view.vue', 'vue', text, budget)
                self.assert_meaningful(parsed)
                for chunk in parsed['chunks']:
                    self.assertIn(chunk['region']['kind'], ('template', 'script', 'style'))
                    for other in ('template', 'script', 'style'):
                        if other != chunk['region']['kind']:
                            self.assertNotIn('<' + other, chunk['content'])
                script = [c for c in parsed['chunks'] if c['region']['kind'] == 'script']
                self.assertTrue(any('store = new Vuex.Store' in c['text']
                                    for part in script for c in part['context']))

    def test_all_supported_extensions_and_docker_names(self):
        lines = ''.join(f'key{i}=中文😀value{i}\r\n' for i in range(80))
        for path in ['audit' + ext for ext in TEXT_EXTENSIONS] + [
                '.env', 'Dockerfile', 'Dockerfile.dev', 'Containerfile', 'Containerfile.dev']:
            with self.subTest(path=path):
                parsed = self.parse(path, 'text', lines, 128)
                self.assertEqual(parsed['quality'], 'text_fallback')
                self.assert_meaningful(parsed)
                self.assertTrue(all(c['content'].endswith('\r\n') for c in parsed['chunks']))
        for language, extensions, text in [
            ('javascript', ('js', 'jsx', 'mjs', 'cjs'), 'export const value = {name: "中文😀"};\r\n'),
            ('typescript', ('ts', 'mts', 'cts'), 'export const value: string = "中文😀";\r\n'),
            ('tsx', ('tsx',), 'export const View = () => <p>中文😀</p>;\r\n'),
            ('java', ('java',), 'class Value {String name = "中文😀";}\r\n'),
            ('python', ('py',), 'def value():\r\n    return "中文😀"\r\n'),
            ('mybatis-xml', ('xml',), '<mapper namespace="demo.Value"><select id="v">SELECT 1</select></mapper>'),
            ('vue', ('vue',), '<template><p>中文😀</p></template>\r\n')]:
            for ext in extensions:
                with self.subTest(ext=ext):
                    self.assert_meaningful(self.parse('audit.' + ext, language, '\ufeff' + text, 128))

    def test_vue_file_propagates_partial_script_quality(self):
        parsed = self.parse('large.vue', 'vue', '<script>export const data = "' + '中文😀' * 800 + '";</script>')
        self.assertEqual(parsed['quality'], 'partial')
        self.assertTrue(any(c['quality'] == 'partial' and c['region']['quality'] == 'partial'
                            for c in parsed['chunks']))

    def test_python_assignments_and_array_elements_have_structural_boundaries(self):
        cases = [
            ('assign.py', 'python', '@trace\nasync def calculate(value):\n' +
             ''.join(f'    value += {i}\n' for i in range(650)) + '    return value\n'),
            ('array.py', 'python', 'values = [\n' + ''.join(f'"value{i}",\n' for i in range(600)) + ']\n'),
            ('array.js', 'javascript', 'export const values = [\n' + ''.join(f'"value{i}",\n' for i in range(600)) + '];\n'),
        ]
        for path, language, text in cases:
            with self.subTest(path=path):
                parsed = self.parse(path, language, text)
                self.assertEqual(parsed['quality'], 'structural')
                self.assert_meaningful(parsed)


if __name__ == '__main__':
    unittest.main()
