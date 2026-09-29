"""Locked Tree-sitter runtime and a provenance adapter over the mature chunker."""
import hashlib
from bisect import bisect_left
from importlib.metadata import version
import json
import os
from pathlib import Path
import shutil
import subprocess

import tree_sitter_language_pack as pack

PACK_VERSION = '1.19.0'
LANGUAGES = ('java', 'javascript', 'typescript', 'tsx', 'python')
RULES_VERSIONS = {'java': 3, 'javascript': 3, 'typescript': 3, 'tsx': 3, 'python': 4}
BUNDLES = {
    'linux-x86_64': '86995c25a95d59a1235276c8bdfc5156f7ffb1db1d53653c9e58a60e92d4346e',
    'linux-aarch64': '4e0cf38459547f10fd2c0f33c783a9c92e744baf2c591453d59233d15d0ab2de',
    'windows-x86_64': '1fd72fbba863c57445f0c570804561a4c569584edf79067d0651e450b6222609',
}


def load_sfc_runtime():
    """Advertise Vue only when the locked Node executable and parser are usable."""
    root = Path(__file__).resolve().parent / 'sfc'
    try:
        lock = json.loads((root / 'runtime.lock.json').read_text(encoding='utf-8'))
        package_lock = json.loads((root / 'package-lock.json').read_text(encoding='utf-8'))
        compiler_lock = package_lock['packages']['node_modules/@vue/compiler-sfc']
        if (lock.get('rules_version') != 1 or compiler_lock.get('version') != lock.get('compiler_version') or
                compiler_lock.get('integrity') != lock.get('compiler_integrity')):
            return None
        node = os.environ.get('SOURCE_PARSER_NODE') or shutil.which('node')
        if not node or not (root / 'parse_sfc.cjs').is_file():
            return None
        environment = dict(os.environ)
        environment.pop('NODE_OPTIONS', None)
        environment.pop('NODE_PATH', None)
        result = subprocess.run(
            [node, str(root / 'parse_sfc.cjs'), '--health'], cwd=root,
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            timeout=3, check=False, text=True, encoding='utf-8', env=environment)
        health = json.loads(result.stdout) if result.returncode == 0 else {}
        if (health.get('node_version') != 'v' + lock.get('node_version', '') or
                health.get('compiler_version') != lock.get('compiler_version') or
                health.get('rules_version') != lock.get('rules_version')):
            return None
        runtime = 'vue-sfc-node-' + lock['node_version'] + '-compiler-' + lock['compiler_version'] + '-rules-' + str(lock['rules_version'])
        return {'node': node, 'root': str(root), 'runtime': runtime}
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        return None


def load_runtime(cache):
    """Never prefetch here. Validate every advertised grammar before loading it."""
    if version('tree-sitter-language-pack') != PACK_VERSION or version('tree-sitter') != '0.26.0':
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
    fingerprint = hashlib.sha256(json.dumps(versions, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    rules_version = max((RULES_VERSIONS[language] for language in versions if language in RULES_VERSIONS), default=1)
    return 'source-pack-' + PACK_VERSION + '-rules-' + str(rules_version) + '-' + fingerprint[:32]


def _source_span(newlines, start, end):
    return {'start_byte': start, 'end_byte': end,
            'start_line': bisect_left(newlines, start) + 1,
            'end_line': bisect_left(newlines, max(start, end - 1)) + 1}


def _utf16_offsets(text, raw):
    offsets = {0: 0}
    utf16, byte = 0, 0
    for character in text:
        utf16 += 2 if ord(character) > 0xFFFF else 1
        byte += len(character.encode('utf-8'))
        offsets[utf16] = byte
    if byte != len(raw):
        raise RuntimeError('SFC source encoding mismatch')
    return offsets


def _offset_vue_blocks(text, raw, blocks, offsets=None):
    """Map compiler UTF-16 positions to verified original UTF-8 byte offsets."""
    offsets = offsets or _utf16_offsets(text, raw)
    mapped = []
    for block in blocks:
        start, end = block['start_utf16'], block['end_utf16']
        if start not in offsets or end not in offsets:
            raise RuntimeError('SFC parser returned a split Unicode character range')
        start_byte, end_byte = offsets[start], offsets[end]
        if hashlib.sha256(raw[start_byte:end_byte]).hexdigest() != block.get('content_sha256'):
            raise RuntimeError('SFC parser returned a non-verifiable block range')
        item = dict(block)
        item['start_byte'], item['end_byte'] = start_byte, end_byte
        mapped.append(item)
    mapped.sort(key=lambda item: (item['start_byte'], item['end_byte']))
    cursor = 0
    for item in mapped:
        if item['start_byte'] < cursor:
            raise RuntimeError('SFC parser returned overlapping block ranges')
        cursor = item['end_byte']
    return mapped


def _split_raw(raw, start, end, maximum, span, quality, region):
    """Split opaque SFC bytes only at UTF-8 boundaries; never transform them."""
    chunks = []
    cursor = start
    while cursor < end:
        stop = min(end, cursor + maximum)
        while stop < end and raw[stop] & 0xC0 == 0x80:
            stop -= 1
        if stop <= cursor:
            raise RuntimeError('SFC block cannot be split at a UTF-8 boundary')
        chunks.append({'content': raw[cursor:stop].decode('utf-8'), 'range': span(cursor, stop),
                       'quality': quality, 'symbols': [], 'context': [], 'region': region})
        cursor = stop
    return chunks


def _shift_sfc_fragment(fragment, base, span):
    shifted = dict(fragment)
    for key in ('range', 'signature_range'):
        if key in shifted:
            original = shifted[key]
            shifted[key] = span(base + original['start_byte'], base + original['end_byte'])
    for key in ('annotations', 'context'):
        if key in shifted:
            values = []
            for value in shifted[key]:
                item = dict(value)
                item['range'] = span(base + value['range']['start_byte'], base + value['range']['end_byte'])
                values.append(item)
            shifted[key] = values
    return shifted


def parse_vue_source(raw, max_bytes, parser_version, path, node_runtime, script_languages):
    """Extract static Vue SFC structure; embedded script is syntax-only Tree-sitter."""
    raw.decode('utf-8', errors='strict')
    text = raw.decode('utf-8')
    digest = hashlib.sha256(raw).hexdigest()
    request = json.dumps({'path': path, 'source': text, 'sha256': digest}, ensure_ascii=False)
    environment = dict(os.environ)
    environment.pop('NODE_OPTIONS', None)
    environment.pop('NODE_PATH', None)
    try:
        result = subprocess.run(
            [node_runtime['node'], str(Path(node_runtime['root']) / 'parse_sfc.cjs')],
            cwd=node_runtime['root'], input=request, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, timeout=4, check=False, text=True,
            encoding='utf-8', env=environment)
        response = json.loads(result.stdout) if result.stdout else {}
    except (OSError, ValueError, subprocess.SubprocessError):
        raise RuntimeError('SFC parser unavailable') from None
    if result.returncode != 0 or response.get('sha256') != digest or response.get('source_bytes') != len(raw):
        raise RuntimeError('SFC parser rejected source')
    newlines = [index for index, byte in enumerate(raw) if byte == 10]
    span = lambda start, end: _source_span(newlines, start, end)
    utf16_offsets = _utf16_offsets(text, raw)
    blocks = _offset_vue_blocks(text, raw, response.get('blocks', []), utf16_offsets)
    chunks, symbols = [], []
    cursor = 0
    degraded = False
    unknown_preprocess = False
    syntax_error = False
    diagnostics = response.get('diagnostics', [])

    def region_for(block, quality):
        block_type = block['type']
        kind = block_type if block_type in ('template', 'script', 'style') else 'custom'
        language = block.get('lang', '').lower()
        external = block.get('src', '') if kind == 'script' else ''
        status = ''
        if external:
            status = 'rejected' if (external.startswith(('/', '\\')) or '\\' in external or
                                    ':' in external or any(mark in external for mark in ('\x00', '?', '#')) or
                                    any(part == '..' for part in external.replace('\\', '/').split('/'))) else 'unchecked'
        return {'kind': kind, 'language': language, 'quality': quality,
                'external_source': external, 'external_status': status}

    known_raw_languages = {'', 'html', 'vue', 'css', 'scss', 'sass', 'less', 'stylus', 'postcss',
                           'json', 'yaml', 'yml', 'js', 'javascript', 'jsx', 'ts', 'typescript', 'tsx'}
    for index, block in enumerate(blocks):
        start, end = block['start_byte'], block['end_byte']
        if start > cursor:
            chunks.extend(_split_raw(raw, cursor, start, max_bytes, span, 'structural', None))
        block_type, language = block['type'], block.get('lang', '').lower()
        kind = block_type if block_type in ('template', 'script', 'style') else 'custom'
        script_language = {'': 'javascript', 'js': 'javascript', 'javascript': 'javascript',
                           'ts': 'typescript', 'typescript': 'typescript', 'tsx': 'tsx'}.get(language)
        body = raw[start:end]
        block_quality = 'structural'
        block_symbols = []
        is_script = (kind == 'script' and not block.get('src') and
                     script_language in script_languages)
        if is_script and body:
            parsed = parse_source(body, max_bytes, parser_version, script_language, path)
            block_quality = parsed['quality']
            syntax_error = syntax_error or block_quality == 'syntax_error'
            block_symbols = [_shift_sfc_fragment(symbol, start, span) for symbol in parsed['symbols']]
            block_chunks = [_shift_sfc_fragment(chunk, start, span) for chunk in parsed['chunks']]
            for chunk in block_chunks:
                chunk['region'] = region_for(block, block_quality)
            chunks.extend(block_chunks)
        else:
            if kind == 'script' and (script_language not in script_languages or block.get('src')):
                block_quality = 'degraded' if block.get('src') else 'unknown_preprocess'
                degraded = True
                unknown_preprocess = unknown_preprocess or block_quality == 'unknown_preprocess'
            elif language not in known_raw_languages:
                block_quality = 'unknown_preprocess'
                degraded = True
                unknown_preprocess = True
            if not body and end > start:
                raise RuntimeError('SFC empty body range mismatch')
            chunks.extend(_split_raw(raw, start, end, max_bytes, span, block_quality,
                                     region_for(block, block_quality)))
        region = region_for(block, block_quality)
        region_range = span(start, end)
        symbols.append({'kind': 'sfc_region', 'name': kind, 'qualified_name': path + '#' + kind + '[' + str(index) + ']',
                        'signature': '', 'signature_range': region_range, 'range': region_range,
                        'annotations': [], 'region': region})
        for symbol in block_symbols:
            symbol['region'] = region
            symbols.append(symbol)
        cursor = end
    if cursor < len(raw):
        chunks.extend(_split_raw(raw, cursor, len(raw), max_bytes, span, 'structural', None))
    for diagnostic in diagnostics:
        position = diagnostic.get('start_utf16') if isinstance(diagnostic, dict) else None
        try:
            byte_position = utf16_offsets.get(position, 0)
        except (RuntimeError, TypeError, KeyError):
            byte_position = 0
        point = span(byte_position, byte_position)
        symbols.append({'kind': 'sfc_diagnostic', 'name': 'SFC descriptor warning',
                        'qualified_name': path + '#diagnostic[' + str(len(symbols)) + ']',
                        'signature': '', 'signature_range': point, 'range': point, 'annotations': [],
                        'region': {'kind': 'custom', 'quality': 'degraded'}})
        degraded = True
    for chunk in chunks:
        area = chunk['range']
        chunk['symbols'] = [symbol['qualified_name'] for symbol in symbols
                            if symbol['kind'] not in ('sfc_diagnostic',) and
                            symbol['range']['start_byte'] < area['end_byte'] and
                            symbol['range']['end_byte'] > area['start_byte']]
    chunks.sort(key=lambda item: item['range']['start_byte'])
    verified_cursor = 0
    for chunk in chunks:
        area = chunk['range']
        if (area['start_byte'] != verified_cursor or area['end_byte'] <= verified_cursor or
                raw[area['start_byte']:area['end_byte']].decode('utf-8') != chunk['content']):
            raise RuntimeError('SFC chunker produced a non-verifiable source range')
        verified_cursor = area['end_byte']
    if verified_cursor != len(raw):
        raise RuntimeError('SFC chunker did not cover the original file')
    quality = ('syntax_error' if syntax_error else
               'unknown_preprocess' if unknown_preprocess else
               'degraded' if degraded else 'structural')
    symbols.sort(key=lambda symbol: (symbol['range']['start_byte'], symbol['range']['end_byte'], symbol['kind']))
    return {'parser_version': parser_version, 'sha256': digest, 'byte_length': len(raw),
            'encoding': 'utf-8-bom' if raw.startswith(b'\xef\xbb\xbf') else 'utf-8',
            'quality': quality, 'symbols': symbols, 'chunks': chunks}


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
    parsed = pack.process(text, pack.ProcessConfig(
        language=language, structure=True, symbols=True, diagnostics=True,
        chunk_max_size=max_bytes, max_source_bytes=16 << 20, parse_timeout_ms=4000,
    ))
    tree = pack.get_parser(language).parse(raw)
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
    else:
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
    quality = 'syntax_error' if tree.root_node.has_error else 'structural'
    chunks = []
    cursor = 0
    for chunk in parsed.chunks:
        start, end = chunk.start_byte, chunk.end_byte
        if start != cursor or end <= start or end > len(raw) or raw[start:end].decode('utf-8') != chunk.content:
            raise RuntimeError('chunker produced a non-verifiable source range')
        cursor = end
        chunks.append({
            'content': chunk.content, 'range': span(start, end), 'quality': quality,
            'symbols': [s['qualified_name'] for s in symbols if s['range']['start_byte'] < end and s['range']['end_byte'] > start],
            'context': [context(s) for s in symbols
                        if s['range']['start_byte'] <= start and s['range']['end_byte'] >= end][-2:],
        })
    if cursor != len(raw):
        raise RuntimeError('chunker did not cover the entire original file')
    return {'parser_version': parser_version, 'sha256': hashlib.sha256(raw).hexdigest(),
            'byte_length': len(raw), 'encoding': 'utf-8-bom' if raw.startswith(b'\xef\xbb\xbf') else 'utf-8',
            'quality': quality, 'symbols': symbols, 'chunks': chunks}
