"""Vue SFC contract tests use the same locked HTTP worker as production."""
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import tracemalloc
import unittest
from http.server import ThreadingHTTPServer
import urllib.error
import urllib.request
from unittest.mock import patch

try:
    from sourceparser import runtime
    from sourceparser.runtime import load_runtime, load_sfc_runtime, runtime_version
    from sourceparser.server import Handler, SLOTS
except ModuleNotFoundError:
    import runtime
    from runtime import load_runtime, load_sfc_runtime, runtime_version
    from server import Handler, SLOTS


def _process_is_running(pid):
    if os.name == 'nt':
        tasklist = subprocess.run(['tasklist', '/FI', f'PID eq {pid}', '/FO', 'CSV', '/NH'],
                                  stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                  stderr=subprocess.DEVNULL, timeout=3, check=False, text=True)
        return f'"{pid}"' in tasklist.stdout
    proc_stat = Path('/proc') / str(pid) / 'stat'
    try:
        stat = proc_stat.read_text(encoding='utf-8')
        fields = stat[stat.rfind(')') + 2:].split()
        return bool(fields) and fields[0] not in ('Z', 'X')
    except FileNotFoundError:
        return False
    except OSError:
        try:
            os.kill(pid, 0)
            return True
        except ProcessLookupError:
            return False


class UTF16OffsetMapTests(unittest.TestCase):
    def test_utf16_mapping_preserves_unicode_boundaries(self):
        raw = 'A\r\n😀Z'.encode()
        offsets = runtime._utf16_offsets(raw.decode(), raw)
        self.assertEqual(offsets.get(0), 0)
        self.assertEqual(offsets.get(3), 3)
        self.assertIsNone(offsets.get(4), 'the interior of an emoji surrogate pair is not a source boundary')
        self.assertEqual(offsets.get(5), 7)
        self.assertEqual(offsets.get(6), 8)

    def test_utf16_mapping_memory_is_bounded_for_large_sfc(self):
        raw = b'a' * (1 << 20)
        text = raw.decode()
        tracemalloc.start()
        try:
            offsets = runtime._utf16_offsets(text, raw)
            _, peak = tracemalloc.get_traced_memory()
        finally:
            tracemalloc.stop()
        self.assertLess(peak, 32 << 20, 'one MiB of source coordinates must not allocate a per-character object map')
        self.assertEqual(offsets.get(len(text)), len(raw))


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
        sfc = load_sfc_runtime()
        if not {'javascript', 'typescript'}.issubset(versions) or not sfc:
            raise unittest.SkipTest('locked Vue, JavaScript, and TypeScript runtimes are required')
        cls.sfc_runtime = sfc['runtime']
        cls.versions = dict(versions, vue=cls.sfc_runtime)
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
        """Retry a transient worker-capacity response without changing request()."""
        body = {
            'path': 'src/BookingPanel.vue', 'language': 'vue',
            'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': maximum}
        deadline = time.monotonic() + 4
        delay = 0.025
        while True:
            status, parsed = self.request('/v1/parse', body)
            if (status != 429 or not isinstance(parsed, dict) or
                    parsed.get('error') != 'parser capacity reached'):
                return status, parsed
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return status, parsed
            time.sleep(min(delay, remaining))
            delay = min(delay * 2, 0.25)

    def test_parse_retries_capacity_but_returns_parser_errors_without_retrying(self):
        parser_error = {'error': 'source parsing failed'}
        with patch.object(self, 'request', side_effect=[
                (429, {'error': 'parser capacity reached'}), (422, parser_error), (200, {})]) as request:
            status, result = self.parse(b'<script></script>')
        self.assertEqual((status, result), (422, parser_error))
        self.assertEqual(request.call_count, 2, 'retry only capacity responses, never parser errors')

    def test_vue_health_and_sfc_artifact_share_the_verified_fingerprint(self):
        status, health = self.request('/health')
        self.assertEqual(status, 200, health)
        self.assertIn('vue', health['languages'])
        expected_rules = ('rules-11' if 'java' in health['languages'] else
                          'rules-4' if 'python' in health['languages'] else 'rules-3')
        self.assertIn(expected_rules, health['parser_version'])
        self.assertEqual(self.sfc_runtime, 'vue-sfc-node-24.19.0-compiler-2.7.16-rules-4')
        self.assertEqual(health['parser_version'], runtime_version(self.versions))
        previous_rules = dict(self.versions)
        previous_rules['vue'] = self.sfc_runtime.removesuffix('-rules-4') + '-rules-3'
        self.assertNotEqual(health['parser_version'], runtime_version(previous_rules),
                            'the SFC rules fingerprint must affect the complete parser fingerprint')
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
        for symbol in parsed['symbols']:
            signature_range = symbol['signature_range']
            self.assertEqual(raw[signature_range['start_byte']:signature_range['end_byte']].decode(),
                             symbol['signature'], symbol['qualified_name'])
        self.assertTrue(any((chunk.get('region') or {}).get('kind') == 'style' for chunk in parsed['chunks']))
        self.assertTrue(any(chunk.get('region') is None and '<template' in chunk['content']
                            for chunk in parsed['chunks']))

    def test_preprocessor_quality_is_scoped_to_block_kind(self):
        raw = ('<template lang="ts">template_marker</template>\r\n'
               '<style lang="json">style_marker</style>\r\n'
               '<script lang="ts">\r\nexport const supportedScriptMarker = 1;\r\n</script>\r\n').encode()
        status, parsed = self.parse(raw)
        self.assertEqual(status, 200, parsed)
        self.assertEqual(parsed['quality'], 'unknown_preprocess')
        regions = {symbol['region']['kind']: symbol['region'] for symbol in parsed['symbols']
                   if symbol['kind'] == 'sfc_region'}
        self.assertEqual(regions['template']['quality'], 'unknown_preprocess')
        self.assertEqual(regions['style']['quality'], 'unknown_preprocess')
        self.assertEqual(regions['script']['quality'], 'structural')
        for marker, kind, quality in (('template_marker', 'template', 'unknown_preprocess'),
                                      ('style_marker', 'style', 'unknown_preprocess'),
                                      ('supportedScriptMarker', 'script', 'structural')):
            chunk = next(chunk for chunk in parsed['chunks'] if marker in chunk['content'])
            self.assertEqual(chunk['quality'], quality)
            self.assertEqual(chunk['region']['kind'], kind)
            self.assertEqual(chunk['region']['quality'], quality)
        script_chunk = next(chunk for chunk in parsed['chunks'] if 'supportedScriptMarker' in chunk['content'])
        script_range = script_chunk['range']
        self.assertEqual(raw[script_range['start_byte']:script_range['end_byte']].decode(),
                         script_chunk['content'])
        self.assertLessEqual(script_range['start_byte'], raw.index(b'supportedScriptMarker'))
        self.assertGreater(script_range['end_byte'], raw.index(b'supportedScriptMarker'))

    def test_descriptor_warning_on_opening_tag_is_attached_to_template_body(self):
        raw = b'<template><div>ok</div>'
        status, parsed = self.parse(raw)
        self.assertEqual(status, 200, parsed)
        body_chunks = [chunk for chunk in parsed['chunks']
                       if (chunk.get('region') or {}).get('kind') == 'template' and '<div>' in chunk['content']]
        self.assertEqual(len(body_chunks), 1)
        self.assertEqual(body_chunks[0]['quality'], 'degraded')
        self.assertEqual(body_chunks[0]['region']['quality'], 'degraded')
        self.assertEqual(body_chunks[0]['diagnostics'][0]['code'], 'vue_sfc_parse_warning')
        diagnostic = next(symbol for symbol in parsed['symbols'] if symbol['kind'] == 'sfc_diagnostic')
        self.assertEqual(diagnostic['range']['start_byte'], 0,
                         'the original warning location remains on the opening tag')

    def test_descriptor_warning_at_eof_is_attached_to_last_script_body_chunk(self):
        raw = b'<script>const a=1'
        status, parsed = self.parse(raw)
        self.assertEqual(status, 200, parsed)
        body_chunks = [chunk for chunk in parsed['chunks']
                       if (chunk.get('region') or {}).get('kind') == 'script' and 'const a=1' in chunk['content']]
        self.assertEqual(len(body_chunks), 1)
        self.assertEqual(body_chunks[0]['quality'], 'degraded')
        self.assertEqual(body_chunks[0]['region']['quality'], 'degraded')
        self.assertEqual(body_chunks[0]['diagnostics'][0]['code'], 'vue_sfc_parse_warning')
        diagnostic = body_chunks[0]['diagnostics'][0]['range']
        self.assertEqual(diagnostic['start_byte'], len(raw))
        self.assertEqual(diagnostic['end_byte'], len(raw), 'EOF remains a zero-width original parser location')

    def test_duplicate_singleton_bodies_omitted_from_descriptor_are_retained_as_degraded_raw(self):
        cases = (
            ('script', ('function firstScriptMarker() { return 1; }',
                        'function secondScriptMarker() { return 2; }'),
             '<script>{}</script>\n<script>{}</script>\n'),
            ('template', ('<div>firstTemplateMarker</div>', '<div>secondTemplateMarker</div>'),
             '<template>{}</template>\n<template>{}</template>\n'),
        )
        for kind, bodies, wrapper in cases:
            with self.subTest(kind=kind):
                raw = wrapper.format(*bodies).encode()
                status, parsed = self.parse(raw)
                self.assertEqual(status, 200, parsed)
                self.assertEqual(parsed['quality'], 'degraded')
                for marker in (bodies[0].split('(')[0].split()[-1] if kind == 'script' else 'firstTemplateMarker',
                               bodies[1].split('(')[0].split()[-1] if kind == 'script' else 'secondTemplateMarker'):
                    body_chunks = [chunk for chunk in parsed['chunks']
                                   if (chunk.get('region') or {}).get('kind') == kind and marker in chunk['content']]
                    self.assertEqual(len(body_chunks), 1, marker)
                    chunk = body_chunks[0]
                    span = chunk['range']
                    self.assertGreater(span['end_byte'], span['start_byte'])
                    self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])
                    self.assertEqual(chunk['quality'], 'degraded')
                    self.assertEqual(chunk['region']['quality'], 'degraded')
                missing_symbol = 'firstScriptMarker' if kind == 'script' else 'firstTemplateMarker'
                self.assertFalse(any(symbol.get('name') == missing_symbol for symbol in parsed['symbols']),
                                 'the discarded block remains raw; no declaration is invented')
                self.assertTrue(any(symbol.get('kind') == 'sfc_diagnostic' and
                                    symbol['region']['kind'] == kind for symbol in parsed['symbols']))

    def test_recovered_unknown_template_language_keeps_unknown_preprocess_quality(self):
        raw = ('<template><div>firstTemplateMarker</div></template>\n'
               '<template lang="pug">secondTemplateMarker</template>\n').encode()
        status, parsed = self.parse(raw)
        self.assertEqual(status, 200, parsed)
        recovered_chunks = [chunk for chunk in parsed['chunks'] if 'secondTemplateMarker' in chunk['content']]
        self.assertEqual(len(recovered_chunks), 1)
        recovered = recovered_chunks[0]
        self.assertEqual(recovered['quality'], 'unknown_preprocess')
        self.assertEqual(recovered['region']['kind'], 'template')
        self.assertEqual(recovered['region']['language'], 'pug')
        self.assertEqual(recovered['region']['quality'], 'unknown_preprocess')

    def test_locked_parser_diagnostics_do_not_depend_on_parent_node_env(self):
        raw = b'<template><div>first</template>\n'
        results = []
        request_body = {
            'path': 'src/BookingPanel.vue', 'language': 'vue',
            'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': 128,
        }
        for value in ('production', 'development'):
            environment = dict(os.environ)
            environment['NODE_ENV'] = value
            process = subprocess.Popen(
                [sys.executable, str(Path(__file__).parents[1] / 'server.py'),
                 '--host', '127.0.0.1', '--port', '0'],
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8', env=environment)
            try:
                startup = process.stdout.readline()
                if not startup:
                    self.fail('isolated parser worker failed to start: ' + process.stderr.read())
                url = 'http://127.0.0.1:' + str(json.loads(startup)['port']) + '/v1/parse'
                request = urllib.request.Request(url, data=json.dumps(request_body).encode(),
                                                 headers={'Content-Type': 'application/json'})
                with urllib.request.urlopen(request, timeout=20) as response:
                    parsed = json.load(response)
                results.append((parsed['quality'], [symbol['range'] for symbol in parsed['symbols']
                                                     if symbol['kind'] == 'sfc_diagnostic']))
            finally:
                process.terminate()
                process.wait(timeout=10)
                process.stdout.close()
                process.stderr.close()
        self.assertEqual(results[0], results[1])
        self.assertTrue(results[0][1], 'the pinned compiler mode must retain the malformed-descriptor warning')

    def test_self_closing_template_and_external_script_keep_wrapper_evidence(self):
        cases = (
            (b'<template/>', 'template', ''),
            (b'<script src="./api.js"/>', 'script', './api.js'),
        )
        for raw, kind, external_source in cases:
            with self.subTest(kind=kind):
                status, parsed = self.parse(raw)
                self.assertEqual(status, 200, parsed)
                self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)
                wrapper_chunks = [chunk for chunk in parsed['chunks'] if chunk.get('region')]
                self.assertEqual(len(wrapper_chunks), 1)
                wrapper = wrapper_chunks[0]
                area = wrapper['range']
                self.assertGreater(area['end_byte'], area['start_byte'])
                self.assertEqual(raw[area['start_byte']:area['end_byte']].decode(), wrapper['content'])
                self.assertEqual(wrapper['content'], raw.decode())
                self.assertEqual(wrapper['region']['kind'], kind)
                if external_source:
                    region = next(symbol['region'] for symbol in parsed['symbols']
                                  if symbol['kind'] == 'sfc_region' and symbol['region']['kind'] == 'script')
                    self.assertEqual(region['external_source'], external_source)
                    self.assertEqual(region['external_status'], 'unchecked')

    def test_empty_template_warning_keeps_template_region_on_its_wrapper_chunk(self):
        raw = b'<template>'
        status, parsed = self.parse(raw)
        self.assertEqual(status, 200, parsed)
        warning_chunks = [chunk for chunk in parsed['chunks'] if chunk.get('diagnostics')]
        self.assertEqual(len(warning_chunks), 1)
        warning_chunk = warning_chunks[0]
        self.assertEqual(warning_chunk['content'], raw.decode())
        self.assertEqual(warning_chunk['region']['kind'], 'template')
        self.assertEqual(warning_chunk['quality'], 'degraded')
        self.assertEqual(warning_chunk['diagnostics'][0]['code'], 'vue_sfc_parse_warning')
        warning_range = warning_chunk['diagnostics'][0]['range']
        self.assertEqual(raw[warning_range['start_byte']:warning_range['end_byte']], b'<template>')

    def test_descriptor_warning_degrades_only_its_region_and_attaches_exact_safe_diagnostic(self):
        template_body = '<div>first ' + ('unrelated ' * 20)
        raw = ('<template>' + template_body + '</template>\r\n'
               '<style lang="scss">.safe{color:red}</style>\r\n').encode()
        status, parsed = self.parse(raw, maximum=64)
        self.assertEqual(status, 200, parsed)
        self.assertEqual(parsed['quality'], 'degraded')
        regions = {symbol['region']['kind']: symbol['region'] for symbol in parsed['symbols']
                   if symbol['kind'] == 'sfc_region'}
        self.assertEqual(regions['template']['quality'], 'degraded')
        self.assertEqual(regions['style']['quality'], 'structural')
        template_chunks = [chunk for chunk in parsed['chunks']
                           if (chunk.get('region') or {}).get('kind') == 'template']
        self.assertGreater(len(template_chunks), 1)
        diagnostic_chunks = [chunk for chunk in template_chunks if chunk.get('diagnostics')]
        self.assertEqual(len(diagnostic_chunks), 1)
        self.assertEqual(diagnostic_chunks[0]['quality'], 'degraded')
        self.assertEqual(diagnostic_chunks[0]['region']['quality'], 'degraded')
        self.assertEqual(diagnostic_chunks[0]['diagnostics'], [{
            'code': 'vue_sfc_parse_warning',
            'range': {'start_byte': 10, 'end_byte': 15, 'start_line': 1, 'end_line': 1},
        }])
        for chunk in template_chunks:
            if chunk is diagnostic_chunks[0]:
                continue
            self.assertEqual(chunk['quality'], 'structural')
            self.assertEqual(chunk['region']['quality'], 'structural')
            self.assertFalse(chunk.get('diagnostics'))
        style_chunk = next(chunk for chunk in parsed['chunks'] if '.safe' in chunk['content'])
        self.assertEqual(style_chunk['quality'], 'structural')
        self.assertFalse(style_chunk.get('diagnostics'))
        diagnostic = next(symbol for symbol in parsed['symbols'] if symbol['kind'] == 'sfc_diagnostic')
        self.assertEqual(diagnostic['range']['start_byte'], 10)
        self.assertEqual(diagnostic['range']['end_byte'], 15)
        self.assertEqual(raw[diagnostic['range']['start_byte']:diagnostic['range']['end_byte']], b'<div>')

    def test_jsx_script_alias_uses_javascript_structure_with_original_ranges(self):
        raw = ('<script lang="jsx">\r\n'
               'export const Widget = () => <button>go</button>;\r\n'
               '</script>\r\n').encode()
        status, parsed = self.parse(raw)
        self.assertEqual(status, 200, parsed)
        self.assertEqual(parsed['quality'], 'structural')
        widget = next(symbol for symbol in parsed['symbols'] if symbol['name'] == 'Widget')
        self.assertEqual(widget['region']['kind'], 'script')
        self.assertEqual(widget['region']['language'], 'jsx')
        signature_range = widget['signature_range']
        self.assertEqual(raw[signature_range['start_byte']:signature_range['end_byte']].decode(), widget['signature'])
        self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)

    def test_sfc_attribute_limits_reject_instead_of_erasing_declarations(self):
        lang_at_limit = ('<script lang="' + 'x' * 64 + '"></script>\r\n').encode()
        status, parsed = self.parse(lang_at_limit)
        self.assertEqual(status, 200, parsed)
        lang_region = next(symbol['region'] for symbol in parsed['symbols'] if symbol['kind'] == 'sfc_region')
        self.assertEqual(len(lang_region['language']), 64)
        self.assertEqual(parsed['quality'], 'unknown_preprocess')

        lang_over_limit = ('<script lang="' + 'x' * 65 + '"></script>\r\n').encode()
        status, rejected = self.parse(lang_over_limit)
        self.assertEqual(status, 422, rejected)
        self.assertEqual(rejected, {'error': 'sfc_attribute_limit'})

        src_at_limit = ('<script src="./' + 'a' * 4094 + '"></script>\r\n').encode()
        status, parsed = self.parse(src_at_limit)
        self.assertEqual(status, 200, parsed)
        src_region = next(symbol['region'] for symbol in parsed['symbols'] if symbol['kind'] == 'sfc_region')
        self.assertEqual(len(src_region['external_source']), 4096)
        self.assertEqual(src_region['external_status'], 'unchecked')

        src_over_limit = ('<script src="./' + 'a' * 4095 + '"></script>\r\n').encode()
        status, rejected = self.parse(src_over_limit)
        self.assertEqual(status, 422, rejected)
        self.assertEqual(rejected, {'error': 'sfc_attribute_limit'})

    def test_timeout_kills_only_the_request_process_tree_before_slot_release(self):
        cache = os.environ['SOURCE_PARSER_CACHE']
        runtime = load_sfc_runtime()
        self.assertIsNotNone(runtime)
        acquired_reservation = SLOTS.acquire(timeout=1)
        self.assertTrue(acquired_reservation)
        server = None
        server_thread = None
        sentinel = None
        pid_file = None
        old_pid_file = os.environ.get('SFC_TEST_CHILD_PID_FILE')
        try:
            with tempfile.TemporaryDirectory(prefix='vue-process-tree-', dir=os.environ.get('TMPDIR')) as directory:
                root = Path(directory)
                pid_file = root / 'child-pids.txt'
                sentinel = subprocess.Popen([runtime['node'], '-e', 'setInterval(() => {}, 1000)'],
                                            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                            stderr=subprocess.DEVNULL)
                (root / 'parse_sfc.cjs').write_text(
                    "const { spawn } = require('node:child_process')\n"
                    "const fs = require('node:fs')\n"
                    "const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' })\n"
                    "fs.writeFileSync(process.env.SFC_TEST_CHILD_PID_FILE, `${process.pid}\\n${child.pid}\\n`)\n"
                    "setInterval(() => {}, 1000)\n", encoding='utf-8')
                os.environ['SFC_TEST_CHILD_PID_FILE'] = str(pid_file)
                server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
                server.cache = cache
                server.parser_version = 'test-timeout'
                server.versions = {'vue': 'test-timeout'}
                server.sfc = runtime
                server.parse_timeout = 8
                server_thread = threading.Thread(target=server.serve_forever, daemon=True)
                server_thread.start()

                def request(source):
                    raw = source.encode('utf-8')
                    body = {'path': 'src/Slow.vue', 'language': 'vue',
                            'sha256': hashlib.sha256(raw).hexdigest(),
                            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': 128}
                    req = urllib.request.Request('http://127.0.0.1:' + str(server.server_port) + '/v1/parse',
                                                 data=json.dumps(body).encode(),
                                                 headers={'Content-Type': 'application/json'})
                    try:
                        with urllib.request.urlopen(req, timeout=15) as response:
                            return response.status, json.load(response)
                    except urllib.error.HTTPError as response:
                        return response.code, json.load(response)

                status, parsed = request('<template><div>normal</div></template>')
                self.assertEqual(status, 200, parsed)
                self.assertTrue(SLOTS.acquire(timeout=5), 'normal completion must release its parser slot')
                SLOTS.release()
                SLOTS.release()
                acquired_reservation = False

                acquired_reservation = SLOTS.acquire(timeout=1)
                self.assertTrue(acquired_reservation)
                server.sfc = {**runtime, 'root': str(root)}
                response = []
                request_thread = threading.Thread(target=lambda: response.append(
                    request('<script>slow</script>')), daemon=True)
                request_thread.start()
                deadline = time.monotonic() + 7
                while not pid_file.exists() and request_thread.is_alive() and time.monotonic() < deadline:
                    time.sleep(0.02)
                self.assertTrue(pid_file.exists(), 'the controlled Node adapter should have started before timeout')
                request_thread.join(timeout=12)
                self.assertFalse(request_thread.is_alive(), 'the bounded HTTP request should finish')
                self.assertEqual(response[0][0], 504, response[0] if response else 'no HTTP response')

                self.assertTrue(SLOTS.acquire(timeout=5), 'timeout cleanup must eventually release its slot')
                SLOTS.release()
                SLOTS.release()
                acquired_reservation = False
                pids = [int(value) for value in pid_file.read_text(encoding='utf-8').splitlines()]
                for pid in pids:
                    self.assertFalse(_process_is_running(pid), f'parser descendant {pid} survived slot release')
                self.assertTrue(_process_is_running(sentinel.pid), 'timeout cleanup must not kill unrelated Node processes')
        finally:
            if server is not None:
                server.shutdown()
                server.server_close()
            if server_thread is not None:
                server_thread.join(timeout=3)
            if acquired_reservation:
                SLOTS.release()
            if old_pid_file is None:
                os.environ.pop('SFC_TEST_CHILD_PID_FILE', None)
            else:
                os.environ['SFC_TEST_CHILD_PID_FILE'] = old_pid_file
            if sentinel is not None and sentinel.poll() is None:
                sentinel.terminate()
                sentinel.wait(timeout=3)

    def test_external_script_references_are_literal_and_never_loaded(self):
        for source, body, status_value in (('./api.js', '', 'unchecked'),
                                           ('./api.js', ' \r\n\t ', 'unchecked'),
                                           ('../shared/api.js', '', 'unchecked'),
                                           ('../private.js', '', 'unchecked'),
                                           ('../../../private.js', '', 'unchecked'),
                                           ('/absolute.js', '', 'rejected')):
            raw = ('<script src="' + source + '">' + body + '</script>\r\n').encode()
            with self.subTest(source=source, body=body):
                status, parsed = self.parse(raw)
                self.assertEqual(status, 200, parsed)
                marker = next(symbol for symbol in parsed['symbols'] if symbol['kind'] == 'sfc_region')
                self.assertEqual(marker['region']['external_source'], source)
                self.assertEqual(marker['region']['external_status'], status_value)
                self.assertEqual(marker['region']['quality'], 'degraded')
                self.assertNotIn('resolved_path', marker['region'])
                self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)
                wrapper_chunks = [chunk for chunk in parsed['chunks']
                                  if '<script' in chunk['content'] or '</script>' in chunk['content']]
                self.assertTrue(wrapper_chunks)
                for chunk in wrapper_chunks:
                    self.assertEqual(chunk['quality'], 'degraded')
                    self.assertEqual(chunk['region']['kind'], 'script')
                    self.assertEqual(chunk['region']['external_status'], status_value)
                    span = chunk['range']
                    self.assertGreater(span['end_byte'], span['start_byte'])
                    self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])
                self.assertTrue(all('resolved_path' not in chunk['region'] for chunk in wrapper_chunks))

    def test_external_script_with_unsupported_language_keeps_unknown_preprocess_quality(self):
        cases = (
            '<script lang="coffee" src="./x.js">raw 😀\r\n</script>\r\n'.encode(),
            b'<script lang="coffee" src="./x.js"></script>\r\n',
            b'<script lang="coffee" src="./x.js"/>\r\n',
        )
        for raw in cases:
            with self.subTest(source=raw):
                status, parsed = self.parse(raw)
                self.assertEqual(status, 200, parsed)
                self.assertEqual(parsed['quality'], 'unknown_preprocess')
                self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)
                marker = next(symbol for symbol in parsed['symbols']
                              if symbol['kind'] == 'sfc_region' and symbol['region']['kind'] == 'script')
                self.assertEqual(marker['region']['language'], 'coffee')
                self.assertEqual(marker['region']['quality'], 'unknown_preprocess')
                self.assertEqual(marker['region']['external_source'], './x.js')
                self.assertEqual(marker['region']['external_status'], 'unchecked')
                self.assertNotIn('resolved_path', marker['region'])

                script_chunks = [chunk for chunk in parsed['chunks']
                                 if (chunk.get('region') or {}).get('kind') == 'script']
                self.assertTrue(script_chunks)
                for chunk in script_chunks:
                    self.assertEqual(chunk['quality'], 'unknown_preprocess')
                    self.assertEqual(chunk['region']['quality'], 'unknown_preprocess')
                    self.assertEqual(chunk['region']['external_source'], './x.js')
                    area = chunk['range']
                    self.assertEqual(raw[area['start_byte']:area['end_byte']].decode(), chunk['content'])

    def test_empty_unknown_preprocessor_wrapper_has_positive_region_evidence(self):
        cases = (
            (b'<template lang="pug"></template>\r\n', 'template', '<template lang="pug">', '</template>'),
            (b'<i18n lang="toml"></i18n>\r\n', 'custom', '<i18n lang="toml">', '</i18n>'),
        )
        for raw, kind, opening, closing in cases:
            with self.subTest(kind=kind):
                status, parsed = self.parse(raw)
                self.assertEqual(status, 200, parsed)
                self.assertEqual(''.join(chunk['content'] for chunk in parsed['chunks']).encode(), raw)
                wrappers = [chunk for chunk in parsed['chunks'] if opening in chunk['content'] or closing in chunk['content']]
                self.assertEqual([chunk['content'] for chunk in wrappers], [opening, closing])
                for chunk in wrappers:
                    self.assertEqual(chunk['quality'], 'unknown_preprocess')
                    self.assertEqual(chunk['region']['kind'], kind)
                    self.assertEqual(chunk['region']['quality'], 'unknown_preprocess')
                    self.assertEqual(raw[chunk['range']['start_byte']:chunk['range']['end_byte']].decode(), chunk['content'])

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
