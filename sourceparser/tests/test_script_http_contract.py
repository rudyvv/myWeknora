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
import threading
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer
from unittest.mock import patch


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
        lock = json.loads((Path(os.environ['SOURCE_PARSER_CACHE']) / 'grammar.lock.json').read_text(encoding='utf-8'))
        expected = set(lock.get('grammars', {'java': lock}))
        if 'java' in expected:
            expected.add('mybatis-xml')
        if expected:
            expected.add('text')
        if 'vue' in health['languages']:
            expected.add('vue')
        expected = sorted(expected)
        self.assertEqual(health['languages'], expected)
        expected_rules = 'rules-11' if 'java' in expected else ('rules-4' if 'python' in expected else 'rules-3')
        self.assertIn(expected_rules, health['parser_version'])

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
            four_language = destination / 'four-language'
            four_language_lock = {k: lock[k] for k in ('pack_version', 'bundle_sha256')}
            four_language_lock['grammars'] = {language: dict(lock['grammars'][language])
                                                for language in ('java', 'javascript', 'typescript', 'tsx')}
            for entry in four_language_lock['grammars'].values():
                grammar = four_language / entry['grammar']
                grammar.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(cache / entry['grammar'], grammar)
            (four_language / 'grammar.lock.json').write_text(json.dumps(four_language_lock), encoding='utf-8')
            status, health = health_for(four_language)
            self.assertEqual(status, 200, health)
            expected_languages = ['java', 'javascript', 'mybatis-xml', 'text', 'tsx', 'typescript']
            if 'vue' in health['languages']:
                expected_languages.append('vue')
                expected_languages.sort()
            self.assertEqual(health['languages'], expected_languages)
            self.assertIn('rules-11', health['parser_version'])
            legacy = {k: lock[k] for k in ('pack_version', 'bundle_sha256')}
            legacy.update(lock['grammars']['java'])
            grammar = destination / legacy['grammar']
            grammar.parent.mkdir(parents=True)
            shutil.copyfile(cache / legacy['grammar'], grammar)
            (destination / 'grammar.lock.json').write_text(json.dumps(legacy), encoding='utf-8')
            status, health = health_for(destination)
            self.assertEqual(status, 200, health)
            self.assertEqual(health['languages'], ['java', 'mybatis-xml', 'text'])
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

    def test_response_waits_for_cleanup_and_next_sequential_parse_keeps_capacity(self):
        parser_root = Path(__file__).parents[1]
        sys.path.insert(0, str(parser_root))
        try:
            import server as parser_server
        finally:
            sys.path.remove(str(parser_root))

        cache = os.environ['SOURCE_PARSER_CACHE']
        local_server = ThreadingHTTPServer(('127.0.0.1', 0), parser_server.Handler)
        local_server.cache = cache
        local_server.parser_version = 'test-parser'
        local_server.versions = {'typescript': 'test-typescript-runtime'}
        local_server.languages = ['typescript']
        local_server.sfc = None
        server_thread = threading.Thread(target=local_server.serve_forever, daemon=True)
        cleanup_started = threading.Event()
        cleanup_finished = threading.Event()
        allow_cleanup = threading.Event()
        success_responded = threading.Event()
        first_response = []
        request_thread = None
        reserved_slot = parser_server.SLOTS.acquire(timeout=1)
        self.assertTrue(reserved_slot, 'one parser slot should be available for the test reservation')
        raw = b'export type Version = string;'
        body = {
            'path': 'version.ts', 'language': 'typescript',
            'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': 128}

        def post():
            request = urllib.request.Request(
                'http://127.0.0.1:' + str(local_server.server_port) + '/v1/parse',
                data=json.dumps(body).encode(), headers={'Content-Type': 'application/json'})
            try:
                with urllib.request.urlopen(request, timeout=15) as response:
                    return response.status, json.load(response)
            except urllib.error.HTTPError as response:
                return response.code, json.load(response)

        original_cleanup = parser_server.terminate_parser_process_tree
        original_respond = parser_server.Handler.respond

        def blocked_cleanup(process):
            cleanup_started.set()
            if not allow_cleanup.wait(timeout=12):
                raise TimeoutError('test did not release the parser cleanup barrier')
            try:
                return original_cleanup(process)
            finally:
                cleanup_finished.set()

        def observed_respond(handler, status, result):
            original_respond(handler, status, result)
            if status == 200:
                success_responded.set()

        def first_request():
            first_response.append(post())

        server_thread.start()
        try:
            with patch.object(parser_server, 'terminate_parser_process_tree', blocked_cleanup), \
                    patch.object(parser_server.Handler, 'respond', observed_respond):
                request_thread = threading.Thread(target=first_request, daemon=True)
                request_thread.start()
                self.assertTrue(cleanup_started.wait(timeout=10), 'successful parsing should enter cleanup')

                early_response = success_responded.wait(timeout=0.2)
                overload_status, overload_result = post()
                self.assertEqual((overload_status, overload_result),
                                 (429, {'error': 'parser capacity reached'}),
                                 'a request during genuine parser saturation should retain direct 429 behavior')
                if early_response:
                    # With the old ordering the first response is visible while its
                    # only parser slot is still held, so the next request is rejected.
                    second_status, second_result = post()
                else:
                    # Release the deterministic barrier, then issue the next request
                    # immediately after the first HTTP response.
                    allow_cleanup.set()
                    request_thread.join(timeout=15)
                    self.assertFalse(request_thread.is_alive(), 'first HTTP request should finish')
                    self.assertEqual(first_response[0][0], 200, first_response[0])
                    second_status, second_result = post()

                allow_cleanup.set()
                request_thread.join(timeout=15)
                self.assertFalse(request_thread.is_alive(), 'first parser cleanup should finish')
                self.assertTrue(cleanup_finished.wait(timeout=10), 'process-tree cleanup should complete')

            self.assertEqual(second_status, 200, second_result)
            self.assertFalse(early_response, 'HTTP success must wait for process cleanup and slot release')
        finally:
            allow_cleanup.set()
            if request_thread is not None:
                request_thread.join(timeout=15)
            if reserved_slot:
                parser_server.SLOTS.release()
            local_server.shutdown()
            local_server.server_close()
            server_thread.join(timeout=3)

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
