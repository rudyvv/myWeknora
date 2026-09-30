"""Locked Tree-sitter runtime and a provenance adapter over the mature chunker."""
import hashlib
from bisect import bisect_left
import heapq
from importlib.metadata import version
import json
from pathlib import Path

if __package__:
    from .mybatis_parser import extract_java_facts, parse_mybatis_xml
else:
    from mybatis_parser import extract_java_facts, parse_mybatis_xml
import tree_sitter_language_pack as pack

PACK_VERSION = '1.19.0'
LANGUAGES = ('java', 'javascript', 'typescript', 'tsx', 'python')
RULES_VERSIONS = {'java': 10, 'javascript': 3, 'typescript': 3, 'tsx': 3, 'python': 4}
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
        rules_version = RULES_VERSIONS[language]
        versions[language] = language + '-pack-' + PACK_VERSION + '-rules-' + str(rules_version) + '-' + entry['grammar_sha256']
    pack.configure(pack.PackConfig(cache_dir=str(cache)))
    for language in versions:
        # Only already verified libraries can reach the language registry.
        pack.get_parser(language)
    return versions


def runtime_version(versions):
    """One stable process version binds the full verified grammar set and extraction rules."""
    if not versions:
        return ''
    processing = {'grammars': versions, 'sqlglot': version('sqlglot'), 'xml_rules': 'mybatis-expat-rules-2'}
    fingerprint = hashlib.sha256(json.dumps(processing, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    rules_version = max(RULES_VERSIONS[language] for language in versions)
    return 'source-pack-' + PACK_VERSION + '-rules-' + str(rules_version) + '-' + fingerprint[:32]


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
    if language == 'python':
        root = tree.root_node
        first_child = root.named_children[0] if root.named_children else None
        add_symbol('module', path, root, [path], first_child)
        stack = [(root, [path], [])]
        while stack:
            node, parents, containers = stack.pop()
            if node.type in ('class_definition', 'function_definition'):
                name = node.child_by_field_name('name')
                body = node.child_by_field_name('body')
                if name is not None:
                    name_text = raw[name.start_byte:name.end_byte].decode('utf-8')
                    kind = 'class' if node.type == 'class_definition' else (
                        'method' if containers and containers[-1] == 'class' else 'function')
                    lineage = parents + [name_text]
                    add_symbol(kind, name_text, node, lineage, body)
                    parents, containers = lineage, containers + [kind]
            elif node.type in ('import_statement', 'import_from_statement'):
                imported_names = []
                if node.type == 'import_statement':
                    targets = node.children_by_field_name('name')
                    for target in targets:
                        if target.type == 'aliased_import':
                            target = target.child_by_field_name('name')
                        if target is not None:
                            imported_names.append(raw[target.start_byte:target.end_byte].decode('utf-8'))
                    name_text = ', '.join(imported_names) or 'import'
                else:
                    module = node.child_by_field_name('module_name')
                    if module is not None:
                        module_name = raw[module.start_byte:module.end_byte].decode('utf-8')
                        for target in node.children_by_field_name('name'):
                            if target.type == 'aliased_import':
                                target = target.child_by_field_name('name')
                            if target is not None:
                                imported_names.append(raw[target.start_byte:target.end_byte].decode('utf-8'))
                        name_text = module_name
                        if imported_names:
                            name_text += ':' + ', '.join(imported_names)
                    else:
                        name_text = 'import'
                add_symbol('import', name_text, node, parents + [name_text])
            stack.extend((child, parents, containers) for child in reversed(node.named_children))
    elif tree is not None:
        stack = [(tree.root_node, [])]
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
    facts.sort(key=lambda fact: (fact['range']['start_byte'], fact['range']['end_byte'], fact['kind'], fact.get('name', '')))
    chunks = []
    cursor = 0
    chunk_ranges = [(chunk.start_byte, chunk.end_byte) for chunk in parsed.chunks]
    expected_chunk_content = {(chunk.start_byte, chunk.end_byte): chunk.content for chunk in parsed.chunks}
    if xml_result is not None and xml_result['quality'] == 'structural':
        regions = sorted((fact['range']['start_byte'], fact['range']['end_byte']) for fact in facts
                         if fact['kind'] in ('mybatis_statement', 'mybatis_sql_fragment', 'mybatis_result_map'))
        non_overlapping = []
        for start, end in regions:
            if non_overlapping and start < non_overlapping[-1][1]:
                non_overlapping[-1] = (non_overlapping[-1][0], max(end, non_overlapping[-1][1]))
            else:
                non_overlapping.append((start, end))
        chunk_ranges = []
        def append_bounded(start, end):
            while start < end:
                cut = min(end, start + max_bytes)
                while cut < end and cut > start and raw[cut] & 0xC0 == 0x80:
                    cut -= 1
                if cut <= start:
                    cut = min(end, start + max_bytes)
                    while cut < end and raw[cut] & 0xC0 == 0x80:
                        cut += 1
                chunk_ranges.append((start, cut))
                start = cut
        region_cursor = 0
        for region_start, region_end in non_overlapping:
            append_bounded(region_cursor, region_start)
            append_bounded(region_start, region_end)
            region_cursor = region_end
        append_bounded(region_cursor, len(raw))

    fact_starts = [fact['range']['start_byte'] for fact in facts]
    fact_ends = [fact['range']['end_byte'] for fact in facts]
    fact_cursor, active_facts, active_fact_ends = 0, set(), []
    for start, end in chunk_ranges:
        expected = raw[start:end].decode('utf-8') if xml_result is not None and xml_result['quality'] == 'structural' else expected_chunk_content.get((start, end))
        if start != cursor or end <= start or end > len(raw) or expected is None or raw[start:end].decode('utf-8') != expected:
            raise RuntimeError('chunker produced a non-verifiable source range')
        content = raw[start:end].decode('utf-8')
        cursor = end
        while fact_cursor < len(facts) and fact_starts[fact_cursor] < end:
            if fact_ends[fact_cursor] > start:
                active_facts.add(fact_cursor)
                heapq.heappush(active_fact_ends, (fact_ends[fact_cursor], fact_cursor))
            fact_cursor += 1
        while active_fact_ends and active_fact_ends[0][0] <= start:
            _, expired = heapq.heappop(active_fact_ends)
            active_facts.discard(expired)
        overlapping_facts = [facts[index] for index in sorted(active_facts)]
        chunks.append({
            'content': content, 'range': span(start, end), 'quality': quality,
            'symbols': ([s['qualified_name'] for s in symbols if s['range']['start_byte'] < end and s['range']['end_byte'] > start]
                        + [((fact.get('namespace', '') + '.') if fact.get('namespace') else '')
                           + fact.get('name', fact.get('statement_type', fact['kind']))
                           for fact in overlapping_facts]),
            'context': [context(s) for s in symbols
                        if s['range']['start_byte'] <= start and s['range']['end_byte'] >= end][-2:],
        })
    if cursor != len(raw):
        raise RuntimeError('chunker did not cover the entire original file')
    return {'parser_version': parser_version, 'sha256': hashlib.sha256(raw).hexdigest(),
            'byte_length': len(raw), 'encoding': 'utf-8-bom' if raw.startswith(b'\xef\xbb\xbf') else 'utf-8',
            'quality': quality, 'symbols': symbols, 'chunks': chunks, 'facts': facts, 'diagnostics': diagnostics}
