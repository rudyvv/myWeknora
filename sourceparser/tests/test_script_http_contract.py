"""JS/TS contract tests exercise only the deployed HTTP boundary."""
import base64
import hashlib
import json
import os
import shutil
import tempfile
from pathlib import Path
import subprocess
import sys
import unittest
import urllib.error
import urllib.request


class ScriptHTTPContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
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

    def parse(self, path, language, raw, maximum=128):
        return self.request('/v1/parse', {
            'path': path, 'language': language, 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': maximum})

    def test_javascript_module_functions_keep_original_unicode_crlf(self):
        raw = ('import { send } from "./api.js";\r\n'
               '// 创建预约😀\r\n'
               'export async function createBooking(名称) {\r\n'
               '  return await send({ 名称 });\r\n'
               '}\r\n'
               'export const cancelBooking = id => send({ id });\r\n').encode()
        status, result = self.parse('web/booking.js', 'javascript', raw)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        names = {s['name']: s for s in result['symbols']}
        self.assertEqual(names['createBooking']['kind'], 'function')
        self.assertEqual(names['cancelBooking']['kind'], 'function')
        self.assertEqual(names['createBooking']['signature'], 'async function createBooking(名称)')
        self.assertEqual(names['createBooking']['range'],
                         {'start_byte': 62, 'end_byte': 137, 'start_line': 3, 'end_line': 5})
        self.assertTrue(any(s['kind'] == 'import' and 'send' in s['signature'] for s in result['symbols']))
        self.assertTrue(any(s['kind'] == 'export' for s in result['symbols']))
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        for chunk in result['chunks']:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])


    def test_typescript_generic_decorated_namespace_has_original_parent_structure(self):
        raw = (Path(__file__).parent / 'fixtures' / 'reservations.ts').read_bytes()
        status, result = self.parse('web/reservations.ts', 'typescript', raw)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        names = {s['name']: s for s in result['symbols'] if s['kind'] != 'export'}
        self.assertEqual(names['BookingId']['kind'], 'type')
        self.assertEqual(names['Booking']['kind'], 'interface')
        self.assertEqual(names['Reservations']['kind'], 'module')
        self.assertEqual(names['Scheduler']['qualified_name'], 'web/reservations.ts.Reservations.Scheduler')
        method = names['reserve']
        self.assertEqual(method['qualified_name'], 'web/reservations.ts.Reservations.Scheduler.reserve')
        self.assertEqual(method['signature'], '@trace\r\n    async reserve(request: Request<T>): Promise<T>')
        self.assertEqual(method['annotations'][0], {'text': '@trace', 'range':
                         {'start_byte': 243, 'end_byte': 249, 'start_line': 7, 'end_line': 7}})
        self.assertEqual(names['Scheduler']['annotations'][0]['text'], '@sealed')
        self.assertEqual(names['Scheduler']['range']['start_byte'], 183)
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        for symbol in result['symbols']:
            span = symbol['signature_range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), symbol['signature'])

    def test_vue_style_javascript_object_members_preserve_parent_structure(self):
        raw = ('export default {\r\n'
               '  name: "BookingPanel",\r\n'
               '  methods: {\r\n'
               '    async reserve(id) { return await api.reserve(id); },\r\n'
               '    cancel: id => api.cancel(id),\r\n'
               '    nested: { retry() { return "预约😀"; } }\r\n'
               '  }\r\n'
               '};\r\n').encode()
        status, result = self.parse('web/booking-panel.js', 'javascript', raw)
        self.assertEqual(status, 200, result)
        names = {s['name']: s for s in result['symbols'] if s['kind'] != 'export'}
        self.assertEqual(names['reserve']['qualified_name'], 'web/booking-panel.js.default.methods.reserve')
        self.assertEqual(names['cancel']['kind'], 'function')
        self.assertEqual(names['cancel']['qualified_name'], 'web/booking-panel.js.default.methods.cancel')
        self.assertEqual(names['retry']['qualified_name'], 'web/booking-panel.js.default.methods.nested.retry')
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)

    def test_anonymous_default_function_and_js_class_have_named_retrievable_structure(self):
        raw = ('export default async function (name) { return `你好${name}`; }\r\n'
               '@registry\r\n'
               'export class BookingClient {\r\n'
               '  #token = "secret";\r\n'
               '  async reserve(name = "😀") { return name?.trim() ?? ""; }\r\n'
               '}\r\n').encode()
        status, result = self.parse('web/client.mjs', 'javascript', raw)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        names = {s['name']: s for s in result['symbols'] if s['kind'] != 'export'}
        self.assertEqual(names['default']['kind'], 'function')
        self.assertEqual(names['default']['qualified_name'], 'web/client.mjs.default')
        self.assertEqual(names['BookingClient']['kind'], 'class')
        self.assertEqual(names['BookingClient']['annotations'][0]['text'], '@registry')
        self.assertEqual(names['reserve']['qualified_name'], 'web/client.mjs.BookingClient.reserve')
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)

    def test_tsx_and_jsx_select_real_dialects_and_preserve_code_ranges(self):
        for path, language, source in (
            ('ui/BookingPanel.tsx', 'tsx', 'type Props = { 名称: string };\r\nexport const BookingPanel = ({名称}: Props) => <button>{名称}😀</button>;\r\n'),
            ('ui/BookingPanel.jsx', 'javascript', 'export default function BookingPanel({名称}) { return <button>{名称}😀</button>; }\r\n'),
        ):
            with self.subTest(path=path):
                raw = source.encode()
                status, result = self.parse(path, language, raw)
                self.assertEqual(status, 200, result)
                self.assertEqual(result['quality'], 'structural')
                self.assertTrue(any(s['name'] == 'BookingPanel' and s['kind'] == 'function' for s in result['symbols']))
                self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
                status, rejection = self.parse(path, 'typescript' if language == 'tsx' else 'tsx', raw)
                self.assertEqual(status, 400, rejection)

    def test_broken_script_syntax_is_explicit_without_losing_any_readable_region(self):
        for path, language in (('web/broken.js', 'javascript'), ('web/broken.ts', 'typescript')):
            with self.subTest(path=path):
                raw = ('\ufeffexport function valid() { return "中文😀"; }\r\n'
                       'export function broken( {\r\n'
                       'const readable = "坏语法后仍可读取";\r\n').encode()
                status, result = self.parse(path, language, raw, 64)
                self.assertEqual(status, 200, result)
                self.assertEqual(result['quality'], 'syntax_error')
                self.assertEqual(result['encoding'], 'utf-8-bom')
                self.assertTrue(all(c['quality'] == 'syntax_error' for c in result['chunks']))
                self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
                for chunk in result['chunks']:
                    span = chunk['range']
                    self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])

    def test_large_script_leaf_and_decorated_context_remain_bounded_and_verifiable(self):
        raw = ('@Trace("' + '中文😀' * 1000 + '")\r\nexport class Large {\r\n'
               '  method() { return "' + '预约😀' * 1000 + '"; }\r\n}\r\n').encode()
        status, result = self.parse('web/large.ts', 'typescript', raw)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        self.assertLessEqual(max(len(c['content'].encode()) for c in result['chunks']), 128)
        contexts = [context for chunk in result['chunks'] for context in chunk['context']]
        self.assertTrue(contexts)
        for context in contexts:
            self.assertLessEqual(len(context['text'].encode()), 512)
            span = context['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), context['text'])

    def test_health_advertises_only_verified_offline_grammars(self):
        status, health = self.request('/health')
        self.assertEqual(status, 200, health)
        self.assertEqual(health['languages'], ['java', 'javascript', 'mybatis-xml', 'tsx', 'typescript'])
        self.assertIn('rules-5', health['parser_version'])

    def test_legacy_java_cache_stays_ready_but_changed_script_grammar_cannot_advertise_readiness(self):
        cache = Path(os.environ['SOURCE_PARSER_CACHE'])
        lock = json.loads((cache / 'grammar.lock.json').read_text(encoding='utf-8'))
        def health_for(test_cache):
            process = subprocess.Popen(
                [sys.executable, str(Path(__file__).parents[1] / 'server.py'), '--host', '127.0.0.1', '--port', '0'],
                env={**os.environ, 'SOURCE_PARSER_CACHE': str(test_cache)},
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8')
            try:
                startup = process.stdout.readline()
                self.assertTrue(startup)
                request = 'http://127.0.0.1:' + str(json.loads(startup)['port']) + '/health'
                try:
                    with urllib.request.urlopen(request, timeout=10) as response:
                        return response.status, json.load(response)
                except urllib.error.HTTPError as response:
                    return response.code, json.load(response)
            finally:
                process.terminate()
                process.wait(timeout=10)
                process.stdout.close()
                process.stderr.close()
        with tempfile.TemporaryDirectory(prefix='source-grammar-contract-') as directory:
            destination = Path(directory)
            legacy = {k: lock[k] for k in ('pack_version', 'bundle_sha256')}
            legacy.update(lock['grammars']['java'])
            grammar = destination / legacy['grammar']
            grammar.parent.mkdir(parents=True)
            shutil.copyfile(cache / legacy['grammar'], grammar)
            (destination / 'grammar.lock.json').write_text(json.dumps(legacy), encoding='utf-8')
            status, health = health_for(destination)
            self.assertEqual(status, 200, health)
            self.assertEqual(health['languages'], ['java', 'mybatis-xml'])
            changed = {k: lock[k] for k in ('pack_version', 'bundle_sha256')}
            changed['grammars'] = {'javascript': dict(lock['grammars']['javascript'])}
            grammar = destination / changed['grammars']['javascript']['grammar']
            shutil.copyfile(cache / changed['grammars']['javascript']['grammar'], grammar)
            changed['grammars']['javascript']['grammar_sha256'] = '0' * 64
            (destination / 'grammar.lock.json').write_text(json.dumps(changed), encoding='utf-8')
            status, health = health_for(destination)
            self.assertEqual(status, 503, health)
            self.assertFalse(health['ready'])
            self.assertEqual(health['languages'], [])

    def test_typescript_callable_class_fields_retain_class_parent(self):
        raw = ('export class Repository<T> {\r\n'
               '  readonly resolve = async (id: string): Promise<T> => { return this.load(id); };\r\n'
               '  load(id: string): T { throw new Error(id); }\r\n'
               '}\r\n').encode()
        status, result = self.parse('src/repository.cts', 'typescript', raw)
        self.assertEqual(status, 200, result)
        names = {s['name']: s for s in result['symbols'] if s['kind'] != 'export'}
        self.assertEqual(names['resolve']['kind'], 'function')
        self.assertEqual(names['resolve']['qualified_name'], 'src/repository.cts.Repository.resolve')
        self.assertEqual(names['resolve']['signature'], 'readonly resolve = async (id: string): Promise<T> =>')
        self.assertEqual(names['load']['qualified_name'], 'src/repository.cts.Repository.load')

    def test_file_artifact_version_matches_verified_worker_health_for_every_dialect(self):
        status, health = self.request('/health')
        self.assertEqual(status, 200, health)
        for path, language, raw in (
            ('Version.java', 'java', b'public class Version {}'),
            ('version.js', 'javascript', b'export function version() { return 1; }'),
            ('version.ts', 'typescript', b'export type Version = string;'),
            ('version.tsx', 'tsx', b'export const Version = () => <div/>;'),
        ):
            with self.subTest(language=language):
                status, result = self.parse(path, language, raw)
                self.assertEqual(status, 200, result)
                self.assertEqual(result['parser_version'], health['parser_version'])

    def test_javascript_typescript_named_object_variables_keep_distinct_parent_structure(self):
        raw = ('export const api = {\r\n'
               '  reserve() { return "预约😀"; },\r\n'
               '  nested: { retry() { return 1; }, nested: { retry() { return 2; } } }\r\n'
               '};\r\n'
               'const otherApi = { reserve: id => id };\r\n'
               'function make() { const api = { reserve() { return 3; } }; return api; }\r\n').encode()
        for path, language in (('src/api.js', 'javascript'), ('src/api.ts', 'typescript')):
            with self.subTest(language=language):
                status, result = self.parse(path, language, raw)
                self.assertEqual(status, 200, result)
                self.assertEqual(result['quality'], 'structural')
                members = [s['qualified_name'] for s in result['symbols'] if s['name'] in ('reserve', 'retry')]
                self.assertEqual(members, [path + '.api.reserve', path + '.api.nested.retry',
                                          path + '.api.nested.nested.retry', path + '.otherApi.reserve',
                                          path + '.make.api.reserve'])
                objects = [s['qualified_name'] for s in result['symbols'] if s['kind'] == 'object']
                self.assertIn(path + '.api', objects)
                self.assertIn(path + '.otherApi', objects)
                self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
                for symbol in result['symbols']:
                    span = symbol['signature_range']
                    self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), symbol['signature'])

    def test_default_exported_arrows_have_function_identity_and_original_context(self):
        for path, language, text, signature, end in (
            ('src/default.js', 'javascript', 'export default (name) => {\r\n  // ' + 'a' * 100 + '\r\n  return name;\r\n};\r\n', '(name) =>', 152),
            ('src/default.ts', 'typescript', 'export default (name: string): string => {\r\n  // ' + 'a' * 100 + '\r\n  return name;\r\n};\r\n', '(name: string): string =>', 168),
        ):
            with self.subTest(language=language):
                raw = text.encode()
                status, result = self.parse(path, language, raw)
                self.assertEqual(status, 200, result)
                functions = [s for s in result['symbols'] if s['kind'] == 'function']
                self.assertEqual(len(functions), 1)
                function = functions[0]
                self.assertEqual(function['name'], 'default')
                self.assertEqual(function['qualified_name'], path + '.default')
                self.assertEqual(function['signature'], signature)
                self.assertEqual(function['range'], {'start_byte': 15, 'end_byte': end, 'start_line': 1, 'end_line': 4})
                self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
                self.assertTrue(any(c['text'] == signature for part in result['chunks'] for c in part['context']))
                for part in result['chunks']:
                    for context in part['context']:
                        span = context['range']
                        self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), context['text'])
        raw = b'const invoke = value => values.map(item => item + value);'
        status, result = self.parse('src/callback.js', 'javascript', raw)
        self.assertEqual(status, 200, result)
        self.assertFalse(any(s['name'] == 'default' for s in result['symbols']))

    def test_typescript_import_equals_preserves_static_import_signature_and_source(self):
        raw = ('import foo = require("foo");\r\n'
               'import { send } from "./api";\r\n'
               'const dynamic = require(selectedModule);\r\n'
               'export const reserve = () => foo.reserve();\r\n').encode()
        status, result = self.parse('src/imports.ts', 'typescript', raw)
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        imports = [s for s in result['symbols'] if s['kind'] == 'import']
        self.assertEqual(len(imports), 2)
        self.assertEqual(imports[0]['name'], '"foo"')
        self.assertEqual(imports[0]['signature'], 'import foo = require("foo");')
        # ASCII statement is 28 bytes; the following CRLF is outside both ranges.
        self.assertEqual(imports[0]['range'], {'start_byte': 0, 'end_byte': 28, 'start_line': 1, 'end_line': 1})
        self.assertEqual(imports[0]['signature_range'], {'start_byte': 0, 'end_byte': 28, 'start_line': 1, 'end_line': 1})
        self.assertEqual(imports[1]['name'], '"./api"')
        self.assertFalse(any(s['kind'] == 'import' and 'selectedModule' in s['signature'] for s in result['symbols']))
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
