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

    def test_java_ast_emits_mapper_facts_and_ignores_comment_lookalikes(self):
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
        calls = [f for f in result['facts'] if f['kind'] == 'java_mapper_call']
        self.assertEqual([(f['receiver'], f['name']) for f in calls], [('mapper', 'findById')])
        for fact in methods + calls:
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
                                       ('updateJoin', 'customers'), ('query', 'base_table'),
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
