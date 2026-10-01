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
        self.assertIn('java', health['languages'])
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

    def test_template_text_fallback_preserves_complete_original_slices(self):
        raw = ('<main>\r\n  <h1>预约 {{ customer.name }}</h1>\r\n'
               '  <#if customer.active>欢迎回来</#if>\r\n</main>\r\n').encode()
        status, health = self.request('/health')
        self.assertEqual(status, 200)
        self.assertIn('text', health['languages'])
        status, result = self.request('/v1/parse', {
            'path': 'src/main/templates/dashboard.ftl', 'language': 'text',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
            'chunk_max_bytes': 64,
        })
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'text_fallback')
        self.assertEqual([d['code'] for d in result['diagnostics']], ['text_fallback'])
        self.assertEqual(result['symbols'], [])
        self.assertEqual(result['facts'], [])
        self.assertTrue(result['chunks'])
        self.assertLessEqual(max(len(c['content'].encode()) for c in result['chunks']), 64)
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        for chunk in result['chunks']:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])
            self.assertEqual(chunk['quality'], 'text_fallback')
            self.assertEqual(chunk['context'], [])

    def test_dockerfile_uses_text_fallback_route(self):
        raw = b'FROM scratch\nLABEL title="demo"\n'
        status, result = self.request('/v1/parse', {
            'path': 'Dockerfile', 'language': 'text', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': 64,
        })
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'text_fallback')

    def test_oversized_comment_is_retained_with_visible_partial_quality(self):
        raw = ('class LargeComment {\r\n  // ' + 'comment_marker ' * 1200
               + '\r\n  String run() { return "after_comment"; }\r\n}\r\n').encode()
        status, result = self.request('/v1/parse', {
            'path': 'LargeComment.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(), 'chunk_max_bytes': 128,
        })
        self.assertEqual(status, 200, result)
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        self.assertEqual(result['quality'], 'partial')
        self.assertTrue(any(d['code'] == 'oversized_unstructured_region' for d in result['diagnostics']))
        degraded = [chunk for chunk in result['chunks'] if chunk['quality'] == 'partial']
        self.assertTrue(degraded)
        for chunk in degraded:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])

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

    def test_java_ast_emits_mapper_method_facts_without_unverified_call_bindings(self):
        raw = (b'package /* Mapper package */ demo;\r\npublic interface PushScheduleMapper {\r\n'
               b'  // String fake();\r\n  Schedule findById(Long id);\r\n}\r\n'
               b'class Service { PushScheduleMapper mapper; void run() { mapper.findById(1L); } }\r\n')
        status, result = self.request('/v1/parse', {
            'path': 'src/Service.java', 'language': 'java',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        methods = [f for f in result['facts'] if f['kind'] == 'java_mapper_method']
        self.assertEqual([f['name'] for f in methods], ['findById'])
        self.assertEqual(methods[0]['namespace'], 'demo.PushScheduleMapper')
        self.assertFalse(any(f['kind'] in ('java_field', 'java_mapper_call') for f in result['facts']))
        for fact in methods:
            span = fact['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), fact['text'])

    def test_java_mapper_sql_annotations_use_ast_literals_and_known_constants(self):
        raw = (b'package demo; import org.apache.ibatis.annotations.Select; '
               b'import org.apache.ibatis.annotations.SelectProvider; '
               b'interface Mapper { String SQL = "SELECT * FROM schedule WHERE id=#{id}"; '
               b'@Select(SQL) Object find(); '
               b'@Select({"SELECT * FROM detail", " WHERE enabled=#{enabled}"}) Object list(); '
               b'@Select("SELECT * FROM " + table) Object dynamic(); '
               b'@SelectProvider(type=X.class, method="build") Object provider(); }')
        status, result = self.request('/v1/parse', {
            'path': 'Mapper.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        sql_facts = [f for f in result['facts'] if f['kind'] == 'java_annotation_sql']
        self.assertEqual([(f['name'], f['method_name']) for f in sql_facts], [('Select', 'find'), ('Select', 'list')])
        tables = [(f.get('method_name', f.get('statement_id')), f['name'])
                  for f in result['facts'] if f['kind'] == 'sql_table']
        self.assertCountEqual(tables, [('find', 'schedule'), ('list', 'detail')])
        self.assertEqual([d['code'] for d in result['diagnostics']],
                         ['java_mapper_sql_dynamic', 'java_mapper_provider_dynamic'])

    def test_java_constant_and_annotation_identity_are_lexically_scoped(self):
        raw = (b'package demo; import org.apache.ibatis.annotations.Select; '
               b'interface First { String SQL = "SELECT * FROM first_table"; @Select(SQL) Object find(); } '
               b'interface Second { String SQL = "SELECT * FROM second_table"; @Select(SQL) Object find(); } '
               b'interface Qualified { @org.apache.ibatis.annotations.Select("SELECT * FROM qualified_table") Object find(); } '
               b'interface Unrelated { @demo.Select("SELECT * FROM not_mapper") Object find(); }')
        status, result = self.request('/v1/parse', {
            'path': 'Mapper.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        sql = [f['sql'] for f in result['facts'] if f['kind'] == 'java_annotation_sql']
        self.assertCountEqual(sql, ['SELECT * FROM first_table', 'SELECT * FROM second_table',
                                    'SELECT * FROM qualified_table'])
        tables = [f['name'] for f in result['facts'] if f['kind'] == 'sql_table']
        self.assertCountEqual(tables, ['first_table', 'second_table', 'qualified_table'])

    def test_unresolved_short_annotation_is_not_treated_as_mybatis(self):
        raw = b'package demo; interface Unknown { @Select("SELECT * FROM false_positive") Object find(); }'
        status, result = self.request('/v1/parse', {
            'path': 'Unknown.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        self.assertFalse(any(f['kind'] == 'java_annotation_sql' for f in result['facts']))
        self.assertFalse(any(f['kind'] == 'sql_table' for f in result['facts']))
        self.assertIn('java_mapper_annotation_identity_unknown', [d['code'] for d in result['diagnostics']])

    def test_member_annotation_type_shadows_imported_mybatis_short_name(self):
        raw = (b'import org.apache.ibatis.annotations.Select; class Outer { '
               b'@interface Select { String value(); } interface M { '
               b'@Select("SELECT * FROM false_table") Object find(); } }')
        status, result = self.request('/v1/parse', {
            'path': 'Outer.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        self.assertFalse(any(f['kind'] == 'java_annotation_sql' for f in result['facts']))
        self.assertFalse(any(f['kind'] == 'sql_table' for f in result['facts']))
        self.assertIn('java_mapper_annotation_identity_shadowed', [d['code'] for d in result['diagnostics']])

    def test_sibling_member_annotation_does_not_shadow_import_in_other_type(self):
        raw = (b'import org.apache.ibatis.annotations.Select; interface Good { '
               b'@Select("SELECT * FROM real_table") Object find(); } '
               b'class Other { @interface Select { String value(); } }')
        status, result = self.request('/v1/parse', {
            'path': 'Mapper.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        self.assertTrue(any(f['kind'] == 'sql_table' and f['name'] == 'real_table' for f in result['facts']))
        self.assertNotIn('java_mapper_annotation_identity_shadowed', [d['code'] for d in result['diagnostics']])

    def test_nested_mapper_namespace_preserves_enclosing_binary_type(self):
        raw = (b'package p; class Outer { interface M { String Q="SELECT * FROM nested_table"; '
               b'@org.apache.ibatis.annotations.Select(Q) Object find(); } }')
        status, result = self.request('/v1/parse', {
            'path': 'Outer.java', 'language': 'java', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        method = next(f for f in result['facts'] if f['kind'] == 'java_mapper_method')
        self.assertEqual(method['namespace'], 'p.Outer$M')
        sql = next(f for f in result['facts'] if f['kind'] == 'java_annotation_sql')
        self.assertEqual(sql['namespace'], 'p.Outer$M')

    def test_mybatis_xml_uses_inert_standard_dtd_and_sqlglot_relations(self):
        raw = ('<?xml version="1.0"?>\r\n'
               '<!DOCTYPE mapper PUBLIC "-//mybatis.org//DTD Mapper 3.0//EN" "http://mybatis.org/dtd/mybatis-3-mapper.dtd">\r\n'
               '<mapper namespace="demo.PushScheduleMapper">\r\n'
               ' <select id="findById" resultMap="Schedule&amp;Map"><![CDATA[WITH active AS (SELECT id FROM schedule WHERE enabled=1) '
               'SELECT a.id FROM active a JOIN (SELECT id FROM audit_log) x ON x.id=a.id, schedule_detail d]]></select>\r\n'
               ' <sql id="base"><![CDATA[SELECT id FROM schedule]]></sql>\r\n'
               '</mapper>').encode()
        status, result = self.request('/v1/parse', {
            'path': 'src/mapper/PushScheduleMapper.xml', 'language': 'mybatis-xml',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'structural')
        mapper = next(f for f in result['facts'] if f['kind'] == 'mybatis_mapper')
        statement = next(f for f in result['facts'] if f['kind'] == 'mybatis_statement')
        self.assertEqual(mapper['namespace'], 'demo.PushScheduleMapper')
        self.assertEqual(statement['result_map_refs'], ['Schedule&Map'])
        self.assertEqual(statement['name'], 'findById')
        tables = sorted(f['name'] for f in result['facts'] if f['kind'] == 'sql_table')
        self.assertEqual(tables, ['audit_log', 'schedule', 'schedule', 'schedule_detail'])
        for fact in (mapper, statement):
            span = fact['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), fact['text'])

    def test_mybatis_fragment_and_result_map_references_keep_owner_and_exact_range(self):
        raw = (b'<mapper namespace="demo.M">'
               b'<sql id="outer"><include refid="inner"/></sql>'
               b'<sql id="dynamic"><include refid="${prefix}"/></sql>'
               b'<sql id="inner">SELECT id FROM records</sql>'
               b'<resultMap id="Base"/>'
               b'<resultMap id="Derived" extends="Base">'
               b'<association property="child" resultMap="Base"/>'
               b'<collection property="items" resultMap="Base"/>'
               b'</resultMap>'
               b'<select id="find" resultMap="Derived"><include refid="outer"/></select>'
               b'</mapper>')
        status, result = self.request('/v1/parse', {
            'path': 'src/mapper.xml', 'language': 'mybatis-xml',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        references = [f for f in result['facts'] if f['kind'] in ('mybatis_include', 'mybatis_result_map_reference')]
        self.assertCountEqual(
            [(f['kind'], f.get('owner_kind'), f.get('owner_name'), f.get('reference_kind'), f.get('name'))
             for f in references],
            [('mybatis_include', 'mybatis_sql_fragment', 'outer', 'include', 'inner'),
             ('mybatis_include', 'mybatis_statement', 'find', 'include', 'outer'),
             ('mybatis_include', 'mybatis_sql_fragment', 'dynamic', 'include', '${prefix}'),
             ('mybatis_result_map_reference', 'mybatis_result_map', 'Derived', 'extends', 'Base'),
             ('mybatis_result_map_reference', 'mybatis_result_map', 'Derived', 'association', 'Base'),
             ('mybatis_result_map_reference', 'mybatis_result_map', 'Derived', 'collection', 'Base')],
        )
        for fact in references:
            span = fact['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), fact['text'])
        dynamic = next(f for f in references if f['name'] == '${prefix}')
        self.assertTrue(dynamic['dynamic'])
        self.assertIn('mybatis_reference_dynamic', [d['code'] for d in result['diagnostics']])

    def test_mybatis_rejects_external_dtd_but_accepts_standard_identifier_offline(self):
        raw = b'<!DOCTYPE mapper SYSTEM "https://attacker.invalid/evil.dtd"><mapper namespace="x"/>'
        status, response = self.request('/v1/parse', {
            'path': 'mapper.xml', 'language': 'mybatis-xml', 'sha256': hashlib.sha256(raw).hexdigest(),
            'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 422, response)

    def test_empty_xml_elements_have_exact_expat_byte_ranges(self):
        for raw in (b'<mapper namespace="x"><resultMap id="r"/><sql id="q"/>'
                    b'<select id="a"/><select id="b"/></mapper>',
                    b'<mapper namespace="x"/>'):
            status, result = self.request('/v1/parse', {
                'path': 'src/mapper.xml', 'language': 'mybatis-xml',
                'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
            })
            self.assertEqual(status, 200, result)
            for fact in result['facts']:
                span = fact['range']
                self.assertLessEqual(span['end_byte'], len(raw))
                if fact.get('text'):
                    self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), fact['text'])
            mapper = next(f for f in result['facts'] if f['kind'] == 'mybatis_mapper')
            self.assertEqual(raw[mapper['range']['start_byte']:mapper['range']['end_byte']].decode(), mapper['text'])

    def test_sqlglot_extracts_dml_targets_without_promoting_functions_or_ctes(self):
        raw = (b'<mapper namespace="demo.M">'
               b'<insert id="upsert">INSERT INTO db.schedule (id) VALUES (1) '
               b'ON DUPLICATE KEY UPDATE id=VALUES(id)</insert>'
               b'<update id="update">UPDATE schedule_status SET enabled=1</update>'
               b'<delete id="delete">DELETE FROM old_schedule WHERE id=1</delete>'
               b'<delete id="deleteUsing">DELETE FROM o USING orders o JOIN customers c ON o.id=c.id</delete>'
               b'<delete id="deleteMulti">DELETE o,c FROM orders o JOIN customers c ON o.id=c.id</delete>'
               b'<update id="updateJoin">UPDATE orders o JOIN customers c ON o.id=c.id SET o.x=1</update>'
               b'<update id="updateDerivedJoin">UPDATE orders o JOIN (SELECT * FROM customers) c '
               b'ON o.id=c.id SET o.x=1</update>'
               b'<select id="query">WITH active AS (SELECT * FROM base_table) '
               b'SELECT * FROM active a JOIN JSON_TABLE(a.value, \'$\' COLUMNS(id INT PATH \'$.id\')) jt ON 1=1</select>'
               b'<update id="qualifiedDml">WITH orders AS (SELECT * FROM staging) UPDATE db.orders '
               b'SET x=(SELECT MAX(id) FROM orders)</update>'
               b'<update id="cteTableReference">WITH c AS (SELECT * FROM db.c) UPDATE target '
               b'SET x=(SELECT MAX(id) FROM c)</update>'
               b'<update id="nestedCteScope">WITH outer_cte AS (SELECT * FROM real_a) UPDATE target '
               b'SET x=(WITH target AS (SELECT * FROM shadow) SELECT MAX(id) FROM target)</update>'
               b'<update id="cteJoin">WITH orders AS (SELECT * FROM staging) UPDATE target JOIN orders ON 1=1 SET target.x=1</update>'
               b'</mapper>')
        status, result = self.request('/v1/parse', {
            'path': 'src/mapper.xml', 'language': 'mybatis-xml',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        tables = [(f['statement_id'], f['name']) for f in result['facts'] if f['kind'] == 'sql_table']
        self.assertCountEqual(tables, [('upsert', 'db.schedule'), ('update', 'schedule_status'),
                                       ('delete', 'old_schedule'), ('deleteUsing', 'orders'),
                                       ('deleteUsing', 'customers'), ('deleteMulti', 'orders'),
                                       ('deleteMulti', 'customers'), ('updateJoin', 'orders'),
                                       ('updateJoin', 'customers'), ('updateDerivedJoin', 'orders'),
                                       ('updateDerivedJoin', 'customers'), ('query', 'base_table'),
                                       ('qualifiedDml', 'db.orders'), ('qualifiedDml', 'staging'),
                                       ('cteTableReference', 'target'), ('cteTableReference', 'db.c'),
                                       ('nestedCteScope', 'target'), ('nestedCteScope', 'real_a'),
                                       ('nestedCteScope', 'shadow'), ('cteJoin', 'target'), ('cteJoin', 'staging')])

    def test_large_mybatis_mapper_keeps_every_statement(self):
        statements = ''.join(f'<select id="s{i}">SELECT id FROM table_{i}</select>' for i in range(1200))
        raw = f'<mapper namespace="large.Mapper">{statements}</mapper>'.encode()
        status, result = self.request('/v1/parse', {
            'path': 'src/LargeMapper.xml', 'language': 'mybatis-xml',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
            'chunk_max_bytes': 4096,
        })
        self.assertEqual(status, 200, result)
        self.assertEqual(sum(f['kind'] == 'mybatis_statement' for f in result['facts']), 1200)
        self.assertEqual(sum(f['kind'] == 'sql_table' for f in result['facts']), 1200)
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)

    def test_each_mybatis_statement_gets_an_independent_index_chunk(self):
        raw = (b'<mapper namespace="demo.M"><select id="first">SELECT * FROM first_table</select>'
               b'<select id="second">SELECT * FROM second_table</select></mapper>')
        status, result = self.request('/v1/parse', {
            'path': 'src/mapper.xml', 'language': 'mybatis-xml',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        statement_chunks = [chunk for chunk in result['chunks']
                            if chunk['content'].startswith('<select')]
        self.assertEqual(len(statement_chunks), 2)
        self.assertIn('id="first"', statement_chunks[0]['content'])
        self.assertNotIn('id="second"', statement_chunks[0]['content'])
        self.assertIn('id="second"', statement_chunks[1]['content'])
        self.assertNotIn('id="first"', statement_chunks[1]['content'])
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)

    def test_oversized_mybatis_statement_splits_at_xml_and_sql_boundaries(self):
        terms = ['tenant_id = #{tenantId}'] + [
            f"field_{index:03d} = 'value with spaces {index:03d}'" for index in range(40)
        ]
        sql = 'SELECT id FROM reservation WHERE ' + ' AND '.join(terms)
        raw = f'<mapper namespace="demo.M"><select id="wide">{sql}</select></mapper>'.encode()
        status, result = self.request('/v1/parse', {
            'path': 'src/mapper.xml', 'language': 'mybatis-xml',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
            'chunk_max_bytes': 64,
        })
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'partial')
        statement = next(f for f in result['facts'] if f['kind'] == 'mybatis_statement')
        diagnostic = next(d for d in result['diagnostics'] if d['code'] == 'oversized_mybatis_statement')
        self.assertEqual(diagnostic['range'], statement['range'])
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        chunks = result['chunks']
        for chunk in chunks:
            span = chunk['range']
            self.assertEqual(raw[span['start_byte']:span['end_byte']].decode(), chunk['content'])
        statement_chunks = [chunk for chunk in chunks
                            if chunk['range']['start_byte'] < statement['range']['end_byte']
                            and chunk['range']['end_byte'] > statement['range']['start_byte']]
        self.assertGreater(len(statement_chunks), 1)
        self.assertTrue(all(chunk['quality'] == 'partial' for chunk in statement_chunks))

        select_open_start = raw.index(b'<select')
        select_open_end = raw.index(b'>', select_open_start) + 1
        select_close_start = raw.index(b'</select>')
        select_close_end = select_close_start + len(b'</select>')
        sql_start, sql_end = raw.index(sql.encode()), raw.index(sql.encode()) + len(sql.encode())
        placeholder = b'#{tenantId}'
        placeholder_start = raw.index(placeholder)
        boundaries = [chunk['range']['start_byte'] for chunk in chunks[1:]]
        for boundary in boundaries:
            self.assertFalse(select_open_start < boundary < select_open_end, 'split inside an XML start tag')
            self.assertFalse(select_close_start < boundary < select_close_end, 'split inside an XML end tag')
            if sql_start < boundary < sql_end:
                self.assertFalse(raw[boundary - 1:boundary].isalnum() and raw[boundary:boundary + 1].isalnum(),
                                 'split inside an SQL word')
                self.assertFalse(placeholder_start < boundary < placeholder_start + len(placeholder),
                                 'split inside a MyBatis bind parameter')
                for index in range(40):
                    quoted_value = f"'value with spaces {index:03d}'".encode()
                    literal_start = raw.index(quoted_value)
                    self.assertFalse(literal_start < boundary < literal_start + len(quoted_value),
                                     'split inside an SQL string literal')

    def test_unbreakable_mybatis_sql_token_is_retained_with_a_partial_diagnostic(self):
        token = 'identifier_' + 'x' * 180
        raw = f'<mapper namespace="demo.M"><select id="wide">SELECT {token} FROM reservation</select></mapper>'.encode()
        status, result = self.request('/v1/parse', {
            'path': 'src/mapper.xml', 'language': 'mybatis-xml',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
            'chunk_max_bytes': 64,
        })
        self.assertEqual(status, 200, result)
        self.assertEqual(result['quality'], 'partial')
        self.assertEqual(''.join(c['content'] for c in result['chunks']).encode(), raw)
        diagnostic = next(d for d in result['diagnostics'] if d['code'] == 'oversized_mybatis_token')
        token_start = raw.index(token.encode())
        self.assertEqual(raw[diagnostic['range']['start_byte']:diagnostic['range']['end_byte']], token.encode())
        self.assertEqual(diagnostic['range']['start_byte'], token_start)

    def test_dynamic_or_duplicated_sql_fragments_are_not_certain(self):
        raw = (b'<mapper namespace="demo.M"><sql id="frag"><if test="enabled">SELECT * FROM orders</if></sql>'
               b'<sql id="dup">SELECT * FROM first_table</sql><sql id="dup">SELECT * FROM second_table</sql>'
               b'<select id="query"><include refid="frag"/><include refid="dup"/></select></mapper>')
        status, result = self.request('/v1/parse', {
            'path': 'src/mapper.xml', 'language': 'mybatis-xml',
            'sha256': hashlib.sha256(raw).hexdigest(), 'content_base64': base64.b64encode(raw).decode(),
        })
        self.assertEqual(status, 200, result)
        fragments = [f for f in result['facts'] if f['kind'] == 'sql_table']
        self.assertEqual({f['name']: f['certainty'] for f in fragments},
                         {'orders': 'uncertain', 'first_table': 'certain', 'second_table': 'certain'})
        self.assertIn('sql_fragment_id_duplicate', [d['code'] for d in result['diagnostics']])
        self.assertIn('sql_fragment_dynamic', [d['code'] for d in result['diagnostics']])
        duplicate_diagnostic = next(d for d in result['diagnostics'] if d['code'] == 'sql_fragment_id_duplicate')
        self.assertNotIn('range', duplicate_diagnostic)
