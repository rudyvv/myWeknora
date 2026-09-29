"""Locked Tree-sitter runtime and a provenance adapter over the mature chunker."""
import hashlib
from bisect import bisect_left
from importlib.metadata import version
import json
from pathlib import Path

from mybatis_parser import extract_java_facts, parse_mybatis_xml
import tree_sitter_language_pack as pack

PACK_VERSION = '1.19.0'
LANGUAGES = ('java', 'javascript', 'typescript', 'tsx')
BUNDLES = {
    'linux-x86_64': '86995c25a95d59a1235276c8bdfc5156f7ffb1db1d53653c9e58a60e92d4346e',
    'linux-aarch64': '4e0cf38459547f10fd2c0f33c783a9c92e744baf2c591453d59233d15d0ab2de',
    'windows-x86_64': '1fd72fbba863c57445f0c570804561a4c569584edf79067d0651e450b6222609',
}


def load_runtime(cache):
    """Never prefetch here. Validate every advertised grammar before loading it."""
    if (version('tree-sitter-language-pack') != PACK_VERSION or version('tree-sitter') != '0.26.0'
            or version('sqlglot') != '30.20.0'):
        raise RuntimeError('parser dependency version differs from requirements.lock')
    cache = Path(cache).resolve()
    lock = json.loads((cache / 'grammar.lock.json').read_text(encoding='utf-8'))
    if lock['pack_version'] != PACK_VERSION or lock['bundle_sha256'] not in BUNDLES.values():
        raise RuntimeError('grammar release differs from the locked release')
    # Existing Java-only installations remain usable for their selected language.
    grammars = lock.get('grammars', {'java': lock})
    if not grammars or set(grammars) - set(LANGUAGES):
        raise RuntimeError('invalid locked language set')
    versions = {}
    expected = Path('tree-sitter-language-pack') / ('v' + PACK_VERSION) / 'libs'
    for language, entry in grammars.items():
        relative = Path(entry['grammar'])
        names = ('tree_sitter_' + language + '.dll', 'libtree_sitter_' + language + '.so', 'tree_sitter_' + language + '.so')
        if relative.parent != expected or relative.name not in names:
            raise RuntimeError('invalid locked grammar path')
        grammar = cache / relative
        if grammar.resolve().parent != (cache / expected).resolve() or any(p.is_symlink() for p in [grammar, grammar.parent, grammar.parent.parent, grammar.parent.parent.parent]) or hashlib.sha256(grammar.read_bytes()).hexdigest() != entry['grammar_sha256']:
            raise RuntimeError('grammar checksum mismatch')
        versions[language] = language + '-pack-' + PACK_VERSION + '-rules-5-' + entry['grammar_sha256']
    pack.configure(pack.PackConfig(cache_dir=str(cache)))
    for language in versions:
        # Only already verified libraries can reach the language registry.
        pack.get_parser(language)
    return versions


def runtime_version(versions):
    """One stable process version binds the full verified grammar set and rules."""
    if not versions:
        return ''
    processing = {'grammars': versions, 'sqlglot': version('sqlglot'), 'xml_rules': 'mybatis-expat-rules-1'}
    fingerprint = hashlib.sha256(json.dumps(processing, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    return 'source-pack-' + PACK_VERSION + '-rules-5-' + fingerprint[:32]


def parse_source(raw, max_bytes, parser_version, language, path):
    text = raw.decode('utf-8', errors='strict')
    newlines = [i for i, byte in enumerate(raw) if byte == 10]
    def span(start, end):
        return {'start_byte': start, 'end_byte': end,
                'start_line': bisect_left(newlines, start) + 1,
                'end_line': bisect_left(newlines, max(start, end - 1)) + 1}
    def context(symbol):
        start = symbol['signature_range']['start_byte']
        prefix = raw[start:min(symbol['signature_range']['end_byte'], start + 512)].decode('utf-8', errors='ignore')
        return {'text': prefix, 'range': span(start, start + len(prefix.encode()))}
    xml_result = parse_mybatis_xml(raw) if language == 'mybatis-xml' else None
    chunk_language = 'java' if language == 'mybatis-xml' else language
    parsed = pack.process(text, pack.ProcessConfig(
        language=chunk_language, structure=language != 'mybatis-xml', symbols=language != 'mybatis-xml', diagnostics=language != 'mybatis-xml',
        chunk_max_size=max_bytes, max_source_bytes=16 << 20, parse_timeout_ms=4000,
    ))
    tree = None if language == 'mybatis-xml' else pack.get_parser(language).parse(raw)
    symbols = []
    java_declarations = {
        'class_declaration': 'class', 'interface_declaration': 'interface',
        'enum_declaration': 'enum', 'annotation_type_declaration': 'annotation',
        'method_declaration': 'method', 'constructor_declaration': 'constructor',
    }
    script_declarations = {
        'class_declaration': 'class', 'abstract_class_declaration': 'class', 'class': 'class',
        'function_declaration': 'function', 'generator_function_declaration': 'function',
        'function_expression': 'function', 'generator_function': 'function',
        'method_definition': 'method', 'method_signature': 'method',
        'abstract_method_signature': 'method', 'function_signature': 'function',
        'interface_declaration': 'interface', 'type_alias_declaration': 'type',
        'enum_declaration': 'enum', 'internal_module': 'module', 'module': 'module',
    }
    declarations = java_declarations if language == 'java' else script_declarations
    def add_symbol(kind, name_text, node, lineage, body=None):
        decorators = []
        if language != 'java' and node.parent is not None:
            sibling = node.prev_named_sibling
            while sibling is not None and sibling.type == 'decorator':
                decorators.insert(0, sibling)
                sibling = sibling.prev_named_sibling
        start = decorators[0].start_byte if decorators else node.start_byte
        signature_end = body.start_byte if body is not None else node.end_byte
        signature = raw[start:signature_end].decode('utf-8').rstrip()
        signature_end = start + len(signature.encode('utf-8'))
        annotations = []
        for child in decorators + node.named_children:
            modifiers = child.named_children if child.type == 'modifiers' else [child]
            for modifier in modifiers:
                if modifier.type in ('annotation', 'marker_annotation', 'decorator'):
                    annotations.append({'text': raw[modifier.start_byte:modifier.end_byte].decode('utf-8'),
                                        'range': span(modifier.start_byte, modifier.end_byte)})
        symbols.append({
            'kind': kind, 'name': name_text, 'qualified_name': '.'.join(lineage),
            'signature': signature, 'signature_range': span(start, signature_end),
            'annotations': annotations, 'range': span(start, node.end_byte),
        })
        if len(symbols) > 10000:
            raise RuntimeError('source declaration limit exceeded')
    # Language node extraction rules only; parsing and structural splitting stay upstream.
    stack = [] if tree is None else [(tree.root_node, [])]
    while stack:
        node, parents = stack.pop()
        lineage = parents
        kind = declarations.get(node.type)
        inferred_name = None
        name = node.child_by_field_name('name')
        body = node.child_by_field_name('body')
        if language != 'java':
            if node.type == 'program':
                kind, name_text = 'module', path
                # Module context need not repeat the complete source text.
                body = node.named_children[0] if node.named_children else None
            elif node.type == 'import_statement':
                kind = 'import'
                name = node.child_by_field_name('source')
                if name is None:
                    name = next((child.child_by_field_name('source') for child in node.named_children
                                 if child.type == 'import_require_clause'), None)
            elif node.type == 'export_statement':
                declaration = node.child_by_field_name('declaration')
                exported_name = declaration.child_by_field_name('name') if declaration is not None else None
                name_text = raw[exported_name.start_byte:exported_name.end_byte].decode('utf-8') if exported_name is not None else 'export'
                add_symbol('export', name_text, node, parents + [name_text], declaration)
                value = node.child_by_field_name('value')
                if value is not None and value.type == 'object':
                    lineage = parents + ['default']
                elif value is not None and value.type == 'arrow_function':
                    lineage = parents + ['default']
                    add_symbol('function', 'default', value, lineage, value.child_by_field_name('body'))
            elif node.type == 'pair':
                name = node.child_by_field_name('key')
                value = node.child_by_field_name('value')
                if value is not None and value.type in ('arrow_function', 'function_expression', 'generator_function'):
                    kind, body = 'function', value.child_by_field_name('body')
                elif value is not None and value.type == 'object':
                    kind, body = 'object', value
            elif node.type in ('variable_declarator', 'public_field_definition', 'field_definition'):
                value = node.child_by_field_name('value')
                if value is not None and value.type in ('arrow_function', 'function_expression', 'generator_function', 'class'):
                    kind = 'class' if value.type == 'class' else 'function'
                    body = value.child_by_field_name('body')
                elif value is not None and value.type == 'object' and name is not None and name.type in ('identifier', 'property_identifier', 'private_property_identifier'):
                    kind, body = 'object', value
        if language != 'java' and kind and name is None and node.parent is not None and node.parent.type == 'export_statement':
            inferred_name = 'default'
        if kind and (name is not None or node.type == 'program' or inferred_name):
            if inferred_name:
                name_text = inferred_name
            if name is not None:
                name_text = raw[name.start_byte:name.end_byte].decode('utf-8')
            lineage = parents + [name_text]
            add_symbol(kind, name_text, node, lineage, body)
            if kind == 'import':
                lineage = parents
        stack.extend((child, lineage) for child in reversed(node.named_children))
    symbols.sort(key=lambda symbol: symbol['range']['start_byte'])
    quality = (xml_result['quality'] if xml_result is not None
               else ('syntax_error' if tree.root_node.has_error else 'structural'))
    facts, diagnostics = [], []
    if language == 'java':
        facts, diagnostics = extract_java_facts(raw, tree, path)
    elif xml_result is not None:
        facts, diagnostics = xml_result['facts'], xml_result['diagnostics']
    chunks = []
    cursor = 0
    for chunk in parsed.chunks:
        start, end = chunk.start_byte, chunk.end_byte
        if start != cursor or end <= start or end > len(raw) or raw[start:end].decode('utf-8') != chunk.content:
            raise RuntimeError('chunker produced a non-verifiable source range')
        cursor = end
        chunks.append({
            'content': chunk.content, 'range': span(start, end), 'quality': quality,
            'symbols': ([s['qualified_name'] for s in symbols if s['range']['start_byte'] < end and s['range']['end_byte'] > start]
                        + [((fact.get('namespace', '') + '.') if fact.get('namespace') else '')
                           + fact.get('name', fact.get('statement_type', fact['kind']))
                           for fact in facts if fact['range']['start_byte'] < end and fact['range']['end_byte'] > start]),
            'context': [context(s) for s in symbols
                        if s['range']['start_byte'] <= start and s['range']['end_byte'] >= end][-2:],
        })
    if cursor != len(raw):
        raise RuntimeError('chunker did not cover the entire original file')
    return {'parser_version': parser_version, 'sha256': hashlib.sha256(raw).hexdigest(),
            'byte_length': len(raw), 'encoding': 'utf-8-bom' if raw.startswith(b'\xef\xbb\xbf') else 'utf-8',
            'quality': quality, 'symbols': symbols, 'chunks': chunks, 'facts': facts, 'diagnostics': diagnostics}
