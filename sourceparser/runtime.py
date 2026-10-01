"""Locked Tree-sitter runtime and a provenance adapter over the mature chunker."""
import hashlib
from array import array
from bisect import bisect_left, bisect_right
import heapq
from importlib.metadata import version
import json
import os
from pathlib import Path
import shutil
import subprocess

if __package__:
    from .mybatis_parser import extract_java_facts, parse_mybatis_xml
else:
    from mybatis_parser import extract_java_facts, parse_mybatis_xml
import tree_sitter_language_pack as pack

PACK_VERSION = '1.19.0'
LANGUAGES = ('java', 'javascript', 'typescript', 'tsx', 'python')
RULES_VERSIONS = {'java': 10, 'javascript': 3, 'typescript': 3, 'tsx': 3, 'python': 4}
TEXT_EXTENSIONS = (
    '.html', '.htm', '.jsp', '.jspx', '.tag', '.tagx', '.ftl', '.ftlh', '.vm',
    '.css', '.scss', '.sass', '.less', '.styl', '.yaml', '.yml', '.json', '.toml',
    '.properties', '.ini', '.conf', '.cfg', '.env', '.sql', '.sh', '.bash',
    '.md', '.txt',
)
BUNDLES = {
    'linux-x86_64': '86995c25a95d59a1235276c8bdfc5156f7ffb1db1d53653c9e58a60e92d4346e',
    'linux-aarch64': '4e0cf38459547f10fd2c0f33c783a9c92e744baf2c591453d59233d15d0ab2de',
    'windows-x86_64': '1fd72fbba863c57445f0c570804561a4c569584edf79067d0651e450b6222609',
}


class SFCAttributeLimitError(Exception):
    """A bounded, source-free rejection for oversized SFC block attributes."""


def _sfc_node_environment():
    environment = dict(os.environ)
    environment.pop('NODE_OPTIONS', None)
    environment.pop('NODE_PATH', None)
    environment['NODE_ENV'] = 'development'
    return environment


def load_sfc_runtime():
    """Advertise Vue only when the locked Node executable and parser are usable."""
    root = Path(__file__).resolve().parent / 'sfc'
    try:
        lock = json.loads((root / 'runtime.lock.json').read_text(encoding='utf-8'))
        package_lock = json.loads((root / 'package-lock.json').read_text(encoding='utf-8'))
        compiler_lock = package_lock['packages']['node_modules/@vue/compiler-sfc']
        if (lock.get('rules_version') != 4 or compiler_lock.get('version') != lock.get('compiler_version') or
                compiler_lock.get('integrity') != lock.get('compiler_integrity')):
            return None
        node = os.environ.get('SOURCE_PARSER_NODE') or shutil.which('node')
        if not node or not (root / 'parse_sfc.cjs').is_file():
            return None
        result = subprocess.run(
            [node, str(root / 'parse_sfc.cjs'), '--health'], cwd=root,
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            timeout=3, check=False, text=True, encoding='utf-8', env=_sfc_node_environment())
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
    """One stable process version binds the verified runtimes and extraction rules."""
    if not versions:
        return ''
    processing = {'grammars': versions, 'sqlglot': version('sqlglot'), 'xml_rules': 'mybatis-expat-rules-2',
                  'xml_chunk_rules': 1, 'text_fallback_rules': 1}
    fingerprint = hashlib.sha256(json.dumps(processing, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    rules_version = max((RULES_VERSIONS[language] for language in versions if language in RULES_VERSIONS), default=1)
    return 'source-pack-' + PACK_VERSION + '-rules-' + str(rules_version) + '-' + fingerprint[:32]


def _source_span(newlines, start, end):
    return {'start_byte': start, 'end_byte': end,
            'start_line': bisect_left(newlines, start) + 1,
            'end_line': bisect_left(newlines, max(start, end - 1)) + 1}


class _UTF16OffsetMap:
    """Map UTF-16 boundaries to UTF-8 bytes with bounded checkpoint storage."""

    __slots__ = ('_text', '_utf16', '_byte', '_character', '_utf16_length')
    _CHECKPOINT_STRIDE = 256

    def __init__(self, text, raw):
        self._text = text
        self._utf16 = array('I', [0])
        self._byte = array('I', [0])
        self._character = array('I', [0])
        utf16, byte = 0, 0
        next_checkpoint = self._CHECKPOINT_STRIDE
        for character_index, character in enumerate(text, 1):
            codepoint = ord(character)
            utf16 += 2 if codepoint > 0xFFFF else 1
            byte += (1 if codepoint <= 0x7F else 2 if codepoint <= 0x7FF else
                     3 if codepoint <= 0xFFFF else 4)
            if utf16 >= next_checkpoint:
                self._utf16.append(utf16)
                self._byte.append(byte)
                self._character.append(character_index)
                next_checkpoint += self._CHECKPOINT_STRIDE
        if byte != len(raw):
            raise RuntimeError('SFC source encoding mismatch')
        self._utf16_length = utf16

    def get(self, position, default=None):
        if type(position) is not int or position < 0 or position > self._utf16_length:
            return default
        checkpoint = bisect_right(self._utf16, position) - 1
        utf16 = self._utf16[checkpoint]
        byte = self._byte[checkpoint]
        character_index = self._character[checkpoint]
        if utf16 == position:
            return byte
        while character_index < len(self._text):
            codepoint = ord(self._text[character_index])
            utf16 += 2 if codepoint > 0xFFFF else 1
            byte += (1 if codepoint <= 0x7F else 2 if codepoint <= 0x7FF else
                     3 if codepoint <= 0xFFFF else 4)
            character_index += 1
            if utf16 == position:
                return byte
            if utf16 > position:
                return default
        return default


def _utf16_offsets(text, raw):
    return _UTF16OffsetMap(text, raw)


def _offset_vue_blocks(text, raw, blocks, offsets=None):
    """Map compiler UTF-16 positions to verified original UTF-8 byte offsets."""
    offsets = offsets if offsets is not None else _utf16_offsets(text, raw)
    mapped = []
    for block in blocks:
        start, end = block['start_utf16'], block['end_utf16']
        start_byte, end_byte = offsets.get(start), offsets.get(end)
        if start_byte is None or end_byte is None:
            raise RuntimeError('SFC parser returned a split Unicode character range')
        if hashlib.sha256(raw[start_byte:end_byte]).hexdigest() != block.get('content_sha256'):
            raise RuntimeError('SFC parser returned a non-verifiable block range')
        item = dict(block)
        item['start_byte'], item['end_byte'] = start_byte, end_byte
        tag_keys = ('tag_start_utf16', 'tag_end_utf16', 'close_start_utf16', 'close_end_utf16')
        present_tags = [key in block for key in tag_keys]
        if present_tags[:2].count(True) == 1 or present_tags[2:].count(True) == 1 or (
                any(present_tags[2:]) and not all(present_tags[:2])):
            raise RuntimeError('SFC parser returned an incomplete wrapper range')
        if all(present_tags[:2]):
            tag_start, tag_end = (block[key] for key in tag_keys[:2])
            tag_start_byte, tag_end_byte = offsets.get(tag_start), offsets.get(tag_end)
            if tag_start_byte is None or tag_end_byte is None:
                raise RuntimeError('SFC parser returned a split Unicode opening wrapper range')
            if (tag_start_byte >= tag_end_byte or tag_end_byte != start_byte or
                    raw[tag_start_byte:tag_start_byte + 1] != b'<'):
                raise RuntimeError('SFC parser returned a non-verifiable wrapper range')
            item.update(tag_start_byte=tag_start_byte, tag_end_byte=tag_end_byte)
        if all(present_tags[2:]):
            close_start, close_end = (block[key] for key in tag_keys[2:])
            close_start_byte, close_end_byte = offsets.get(close_start), offsets.get(close_end)
            if close_start_byte is None or close_end_byte is None:
                raise RuntimeError('SFC parser returned a split Unicode closing wrapper range')
            if (close_start_byte != end_byte or close_end_byte <= close_start_byte or
                    raw[close_start_byte:close_start_byte + 2] != b'</'):
                raise RuntimeError('SFC parser returned a non-verifiable closing wrapper range')
            item.update(close_start_byte=close_start_byte, close_end_byte=close_end_byte)
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
                       'quality': quality, 'symbols': [], 'context': [],
                       'region': dict(region) if region is not None else None})
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
    try:
        result = subprocess.run(
            [node_runtime['node'], str(Path(node_runtime['root']) / 'parse_sfc.cjs')],
            cwd=node_runtime['root'], input=request, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, check=False, text=True,
            encoding='utf-8', env=_sfc_node_environment())
        response = json.loads(result.stdout) if result.stdout else {}
    except (OSError, ValueError, subprocess.SubprocessError):
        raise RuntimeError('SFC parser unavailable') from None
    if result.returncode != 0 and response.get('error') == 'sfc_attribute_limit':
        raise SFCAttributeLimitError('sfc_attribute_limit') from None
    if result.returncode != 0 or response.get('sha256') != digest or response.get('source_bytes') != len(raw):
        raise RuntimeError('SFC parser rejected source')
    newlines = [index for index, byte in enumerate(raw) if byte == 10]
    span = lambda start, end: _source_span(newlines, start, end)
    utf16_offsets = _utf16_offsets(text, raw)
    descriptor_blocks = _offset_vue_blocks(text, raw, response.get('blocks', []), utf16_offsets)
    top_level_blocks = response.get('top_level_blocks')
    if not isinstance(top_level_blocks, list) or len(top_level_blocks) > 512:
        raise RuntimeError('SFC parser returned invalid top-level blocks')
    mapped_top_level_blocks = _offset_vue_blocks(text, raw, top_level_blocks, utf16_offsets)
    descriptor_by_key = {(block['type'].lower(), block['start_utf16']): block
                         for block in descriptor_blocks}
    blocks = []
    matched_descriptor_keys = set()
    for block in mapped_top_level_blocks:
        key = (block['type'].lower(), block['start_utf16'])
        descriptor = descriptor_by_key.get(key)
        if (descriptor is not None and descriptor['end_utf16'] == block['end_utf16'] and
                descriptor['content_sha256'] == block['content_sha256']):
            blocks.append(descriptor)
            matched_descriptor_keys.add(key)
            continue
        if descriptor is not None:
            block.update(lang=descriptor.get('lang', ''), src=descriptor.get('src', ''),
                         setup=descriptor.get('setup', False))
            matched_descriptor_keys.add(key)
        language = block.get('lang', '')
        if not isinstance(language, str) or len(language) > 64:
            raise RuntimeError('SFC parser returned an invalid recovered block language')
        block.update(lang=language, src=block.get('src', ''),
                     setup=block.get('setup', False), recovered=True)
        blocks.append(block)
    blocks.extend(descriptor for key, descriptor in descriptor_by_key.items()
                  if key not in matched_descriptor_keys)
    blocks.sort(key=lambda item: (item['start_byte'], item['end_byte']))
    verified_block_end = 0
    for block in blocks:
        if block['start_byte'] < verified_block_end:
            raise RuntimeError('SFC parser returned overlapping descriptor and recovered blocks')
        verified_block_end = max(verified_block_end, block['end_byte'])
    chunks, symbols, block_infos = [], [], []
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
                                    ':' in external or any(mark in external for mark in ('\x00', '?', '#'))) else 'unchecked'
        return {'kind': kind, 'language': language, 'quality': quality,
                'external_source': external, 'external_status': status}

    known_raw_languages = {
        'template': {'', 'html', 'vue'},
        'script': {'', 'js', 'javascript', 'jsx', 'ts', 'typescript', 'tsx'},
        'style': {'', 'css', 'scss', 'sass', 'less', 'stylus', 'postcss'},
        'custom': {'', 'json', 'yaml', 'yml'},
    }
    for index, block in enumerate(blocks):
        start, end = block['start_byte'], block['end_byte']
        block_type, language = block['type'], block.get('lang', '').lower()
        kind = block_type if block_type in ('template', 'script', 'style') else 'custom'
        script_language = {'': 'javascript', 'js': 'javascript', 'javascript': 'javascript', 'jsx': 'javascript',
                           'ts': 'typescript', 'typescript': 'typescript', 'tsx': 'tsx'}.get(language)
        body = raw[start:end]
        recovered = bool(block.get('recovered'))
        block_symbols = []
        body_chunks = []
        external_script = kind == 'script' and bool(block.get('src'))
        unsupported_script = kind == 'script' and not external_script and script_language not in script_languages
        unsupported_preprocess = unsupported_script or language not in known_raw_languages[kind]
        block_quality = ('unknown_preprocess' if recovered and unsupported_preprocess else
                         'degraded' if recovered else 'structural')
        wrapper_quality = ('unknown_preprocess' if unsupported_preprocess else
                           'degraded' if recovered or external_script else '')
        wrapper_region = region_for(block, wrapper_quality) if wrapper_quality else None
        if recovered:
            degraded = True
        tag_start = block.get('tag_start_byte')
        if wrapper_quality and tag_start is None:
            raise RuntimeError('SFC parser omitted a required block wrapper range')
        if tag_start is not None and tag_start < cursor:
            raise RuntimeError('SFC parser returned an overlapping block wrapper range')
        split_empty_wrappers = not body and tag_start is not None
        opening_start = tag_start if wrapper_quality or split_empty_wrappers else start
        if opening_start > cursor:
            chunks.extend(_split_raw(raw, cursor, opening_start, max_bytes, span, 'structural', None))
        if wrapper_quality:
            opening_chunks = _split_raw(raw, tag_start, start, max_bytes, span, wrapper_quality, wrapper_region)
            chunks.extend(opening_chunks)
        elif split_empty_wrappers:
            empty_region = region_for(block, block_quality)
            opening_chunks = _split_raw(raw, tag_start, start, max_bytes, span, block_quality, empty_region)
            chunks.extend(opening_chunks)
        is_script = (not recovered and kind == 'script' and not block.get('src') and
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
            body_chunks = block_chunks
        else:
            if external_script:
                block_quality = 'unknown_preprocess' if unsupported_preprocess else 'degraded'
                degraded = True
                unknown_preprocess = unknown_preprocess or unsupported_preprocess
            elif unsupported_preprocess:
                block_quality = 'unknown_preprocess'
                degraded = True
                unknown_preprocess = True
            if not body and end > start:
                raise RuntimeError('SFC empty body range mismatch')
            body_chunks = _split_raw(raw, start, end, max_bytes, span, block_quality,
                                     region_for(block, block_quality))
            chunks.extend(body_chunks)
        region = region_for(block, block_quality)
        region_range = span(start, end)
        marker_signature_range = span(start, start)
        region_symbol = {'kind': 'sfc_region', 'name': kind, 'qualified_name': path + '#' + kind + '[' + str(index) + ']',
                         'signature': '', 'signature_range': marker_signature_range, 'range': region_range,
                         'annotations': [], 'region': region}
        symbols.append(region_symbol)
        for symbol in block_symbols:
            symbol['region'] = region
            symbols.append(symbol)
        cursor = end
        if (wrapper_quality or split_empty_wrappers) and block.get('close_end_byte') is not None:
            close_start, close_end = block['close_start_byte'], block['close_end_byte']
            if close_start != cursor or close_end <= close_start:
                raise RuntimeError('SFC parser returned a non-contiguous closing wrapper range')
            closing_quality = wrapper_quality or block_quality
            closing_region = wrapper_region or region_for(block, block_quality)
            closing_chunks = _split_raw(raw, close_start, close_end, max_bytes, span,
                                        closing_quality, closing_region)
            chunks.extend(closing_chunks)
            cursor = close_end
        block_infos.append({
            'region': region,
            'start_byte': tag_start if tag_start is not None else start,
            'end_byte': block.get('close_end_byte', end),
            'body_start_byte': start,
            'body_end_byte': end,
            'body_chunks': body_chunks,
        })
    if cursor < len(raw):
        chunks.extend(_split_raw(raw, cursor, len(raw), max_bytes, span, 'structural', None))
    safe_diagnostic_codes = {'vue_sfc_parse_warning', 'vue_sfc_duplicate_block'}
    if not isinstance(diagnostics, list) or len(diagnostics) > 128:
        raise RuntimeError('SFC parser returned invalid diagnostics')
    for diagnostic in diagnostics:
        if not isinstance(diagnostic, dict) or diagnostic.get('code') not in safe_diagnostic_codes:
            raise RuntimeError('SFC parser returned an invalid diagnostic code')
        start_utf16 = diagnostic.get('start_utf16')
        end_utf16 = diagnostic.get('end_utf16')
        start_byte = utf16_offsets.get(start_utf16) if type(start_utf16) is int else None
        if start_byte is None:
            degraded = True
            continue
        if end_utf16 is None:
            end_byte = start_byte
        elif type(end_utf16) is int and end_utf16 >= start_utf16:
            end_byte = utf16_offsets.get(end_utf16)
            if end_byte is None:
                degraded = True
                continue
        else:
            degraded = True
            continue
        diagnostic_range = span(start_byte, end_byte)
        diagnostic_evidence = {'code': diagnostic['code'], 'range': diagnostic_range}
        block_info = next((item for item in block_infos
                           if item['start_byte'] <= start_byte < item['end_byte']), None)
        if block_info is None:
            block_info = next((item for item in reversed(block_infos)
                               if item['end_byte'] == start_byte), None)
        affected_chunk = None
        if block_info and block_info['body_chunks']:
            if start_byte < block_info['body_start_byte']:
                affected_chunk = block_info['body_chunks'][0]
            elif start_byte >= block_info['body_end_byte']:
                affected_chunk = block_info['body_chunks'][-1]
            else:
                affected_chunk = next((chunk for chunk in block_info['body_chunks']
                                       if chunk['range']['start_byte'] <= start_byte < chunk['range']['end_byte']), None)
        if affected_chunk is None and block_info is not None and not block_info['body_chunks']:
            affected_chunk = next((chunk for chunk in reversed(chunks)
                                   if block_info['start_byte'] <= chunk['range']['start_byte'] <
                                   chunk['range']['end_byte'] <= block_info['end_byte']), None)
        if affected_chunk is None:
            affected_chunk = next((chunk for chunk in chunks
                                   if chunk['range']['start_byte'] <= start_byte < chunk['range']['end_byte']), None)
        if affected_chunk is None:
            degraded = True
            continue
        if block_info is not None:
            region = block_info['region']
            if region['quality'] == 'structural':
                region['quality'] = 'degraded'
            diagnostic_region = region
        else:
            diagnostic_region = affected_chunk.get('region') or {'kind': 'custom', 'quality': 'degraded'}
        if affected_chunk['quality'] == 'structural':
            affected_chunk['quality'] = 'degraded'
        if affected_chunk.get('region') and affected_chunk['region']['quality'] == 'structural':
            affected_chunk['region']['quality'] = 'degraded'
        elif block_info is not None and affected_chunk.get('region') is None:
            affected_chunk['region'] = dict(diagnostic_region)
        affected_chunk.setdefault('diagnostics', []).append(diagnostic_evidence)
        point = span(start_byte, start_byte)
        symbols.append({'kind': 'sfc_diagnostic', 'name': 'SFC descriptor warning',
                        'qualified_name': path + '#diagnostic[' + str(len(symbols)) + ']',
                        'signature': '', 'signature_range': point, 'range': diagnostic_range,
                        'annotations': [], 'region': diagnostic_region})
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
def is_text_fallback_path(path):
    basename = Path(path).name.lower()
    return (basename in ('dockerfile', 'containerfile') or basename.startswith(('dockerfile.', 'containerfile.'))
            or Path(path).suffix.lower() in TEXT_EXTENSIONS)


def _bounded_text_ranges(raw, maximum):
    ranges = []
    start = 0
    while start < len(raw):
        cut = min(len(raw), start + maximum)
        if cut < len(raw):
            while cut > start and raw[cut] & 0xC0 == 0x80:
                cut -= 1
            if cut <= start:
                cut = min(len(raw), start + maximum)
                while cut < len(raw) and raw[cut] & 0xC0 == 0x80:
                    cut += 1
            newline = raw.rfind(b'\n', start, cut)
            if newline >= start + maximum // 2:
                cut = newline + 1
        if cut <= start:
            raise RuntimeError('text fallback could not make forward progress')
        ranges.append((start, cut))
        start = cut
    return ranges


def _xml_markup_end(raw, start, end):
    if raw.startswith(b'<!--', start):
        close = raw.find(b'-->', start + 4, end)
        return close + 3 if close >= 0 else end
    if raw.startswith(b'<?', start):
        close = raw.find(b'?>', start + 2, end)
        return close + 2 if close >= 0 else end
    quote = None
    for index in range(start, end):
        byte = raw[index]
        if quote is not None:
            if byte == quote:
                quote = None
        elif byte in (ord("'"), ord('"')):
            quote = byte
        elif byte == ord('>'):
            return index + 1
    return end


def _sql_text_boundaries(raw, start, end):
    """Yield safe source cuts between SQL tokens in one XML text region."""
    index, quote, block_comment, line_comment = start, None, False, False
    while index < end:
        byte = raw[index]
        if line_comment:
            if byte == 10:
                line_comment = False
                yield index + 1
            index += 1
            continue
        if block_comment:
            if raw.startswith(b'*/', index, end):
                block_comment = False
                index += 2
                yield index
            else:
                index += 1
            continue
        if quote is not None:
            if byte == ord('\\') and index + 1 < end:
                index += 2
            elif byte == quote:
                if index + 1 < end and raw[index + 1] == quote:
                    index += 2
                else:
                    quote = None
                    index += 1
            else:
                index += 1
            continue
        if raw.startswith(b'/*', index, end):
            block_comment = True
            index += 2
        elif raw.startswith(b'--', index, end):
            line_comment = True
            index += 2
        elif raw.startswith((b'#{', b'${'), index, end):
            close = raw.find(b'}', index + 2, end)
            index = close + 1 if close >= 0 else end
        elif byte == ord('#'):
            line_comment = True
            index += 1
        elif byte in (ord("'"), ord('"'), ord('`')):
            quote = byte
            index += 1
        elif byte == ord('&'):
            close = raw.find(b';', index + 1, end)
            index = close + 1 if close >= 0 else index + 1
        elif byte in b' \t\r\n\f\v':
            index += 1
            yield index
        else:
            index += 1


def _xml_sql_boundaries(raw, start, end):
    """Yield cuts between complete XML markup and SQL lexical tokens."""
    yield start
    index, in_cdata = start, False
    while index < end:
        if not in_cdata and raw.startswith(b'<![CDATA[', index, end):
            yield index
            index += len(b'<![CDATA[')
            yield index
            in_cdata = True
            continue
        if in_cdata and raw.startswith(b']]>', index, end):
            yield index
            index += len(b']]>')
            yield index
            in_cdata = False
            continue
        if not in_cdata and raw[index] == ord('<'):
            yield index
            index = _xml_markup_end(raw, index, end)
            yield index
            continue
        text_end = index
        while text_end < end:
            if in_cdata and raw.startswith(b']]>', text_end, end):
                break
            if not in_cdata and raw[text_end] == ord('<'):
                break
            text_end += 1
        yield from _sql_text_boundaries(raw, index, text_end)
        index = text_end
    yield end


def _split_xml_sql_region(raw, start, end, maximum):
    if maximum <= 0:
        raise ValueError('XML/SQL chunk budget must be positive')
    boundaries = iter(_xml_sql_boundaries(raw, start, end))
    next_boundary = next(boundaries, None)
    chunks, forced_cuts, cursor, last_boundary = [], [], start, start
    while cursor < end:
        limit = min(end, cursor + maximum)
        while limit > cursor and limit < end and raw[limit] & 0xC0 == 0x80:
            limit -= 1
        if limit <= cursor:
            limit = min(end, cursor + maximum)
            while limit < end and raw[limit] & 0xC0 == 0x80:
                limit += 1
        last_safe = None
        while next_boundary is not None and next_boundary <= limit:
            if next_boundary > cursor:
                last_safe = next_boundary
            if next_boundary > last_boundary:
                last_boundary = next_boundary
            next_boundary = next(boundaries, None)
        if last_safe is None:
            cut = limit
            forced_cuts.append((last_boundary, next_boundary if next_boundary is not None else end))
        else:
            cut = last_safe
        if cut <= cursor:
            raise RuntimeError('XML/SQL chunker could not make forward progress')
        chunks.append((cursor, cut))
        cursor = cut
    return chunks, forced_cuts


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
    if language == 'text':
        if not is_text_fallback_path(path):
            raise ValueError('text fallback path is not supported')
        chunks = [{'content': raw[start:end].decode('utf-8'), 'range': span(start, end),
                   'quality': 'text_fallback', 'symbols': [], 'context': []}
                  for start, end in _bounded_text_ranges(raw, max_bytes)]
        return {'parser_version': parser_version, 'sha256': hashlib.sha256(raw).hexdigest(),
                'byte_length': len(raw), 'encoding': 'utf-8-bom' if raw.startswith(b'\xef\xbb\xbf') else 'utf-8',
                'quality': 'text_fallback', 'symbols': [], 'chunks': chunks, 'facts': [],
                'diagnostics': [{'code': 'text_fallback',
                                 'message': 'No structural grammar is applied; source is indexed as bounded text'}]}
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
    expected_chunk_metadata = {(chunk.start_byte, chunk.end_byte): chunk.metadata for chunk in parsed.chunks}
    oversized_xml_comments = []
    if xml_result is not None and xml_result['quality'] == 'structural':
        comment_cursor = 0
        while True:
            comment_start = raw.find(b'<!--', comment_cursor)
            if comment_start < 0:
                break
            comment_close = raw.find(b'-->', comment_start + 4)
            if comment_close < 0:
                break
            comment_end = comment_close + 3
            if comment_end - comment_start > max_bytes:
                oversized_xml_comments.append((comment_start, comment_end))
                diagnostics.append({
                    'code': 'oversized_comment_region',
                    'message': 'An oversized XML comment is retained as bounded original text',
                    'range': span(comment_start, comment_end),
                })
            comment_cursor = comment_end
    if xml_result is not None and xml_result['quality'] == 'structural':
        regions = sorted((fact['range']['start_byte'], fact['range']['end_byte'], fact['kind']) for fact in facts
                         if fact['kind'] in ('mybatis_statement', 'mybatis_sql_fragment', 'mybatis_result_map'))
        non_overlapping = []
        for start, end, kind in regions:
            if non_overlapping and start < non_overlapping[-1][1]:
                previous_start, previous_end, previous_kinds = non_overlapping[-1]
                non_overlapping[-1] = (previous_start, max(end, previous_end), previous_kinds | {kind})
            else:
                non_overlapping.append((start, end, {kind}))
        chunk_ranges = []
        oversized_mybatis_chunks = set()
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
        for region_start, region_end, kinds in non_overlapping:
            append_bounded(region_cursor, region_start)
            if region_end - region_start > max_bytes:
                region_chunks, forced_cuts = _split_xml_sql_region(raw, region_start, region_end, max_bytes)
                chunk_ranges.extend(region_chunks)
                oversized_mybatis_chunks.update(region_chunks)
                code = 'oversized_mybatis_statement' if 'mybatis_statement' in kinds else 'oversized_mybatis_region'
                diagnostics.append({
                    'code': code,
                    'message': 'An oversized MyBatis structure was split at XML and SQL boundaries; its bounded chunks are partial',
                    'range': span(region_start, region_end),
                })
                for token_start, token_end in set(forced_cuts):
                    while token_start < token_end and raw[token_start] in b' \t\r\n\f\v':
                        token_start += 1
                    while token_end > token_start and raw[token_end - 1] in b' \t\r\n\f\v':
                        token_end -= 1
                    diagnostics.append({
                        'code': 'oversized_mybatis_token',
                        'message': 'A single XML or SQL lexical unit exceeded the chunk budget and required a bounded partial split',
                        'range': span(token_start, token_end),
                    })
            else:
                append_bounded(region_start, region_end)
            region_cursor = region_end
        append_bounded(region_cursor, len(raw))
    else:
        oversized_mybatis_chunks = set()

    fact_starts = [fact['range']['start_byte'] for fact in facts]
    fact_ends = [fact['range']['end_byte'] for fact in facts]
    fact_cursor, active_facts, active_fact_ends = 0, set(), []
    partial_region, degraded, xml_comment_cursor = None, False, 0
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
        chunk_metadata = expected_chunk_metadata.get((start, end))
        chunk_quality = quality
        oversized_unstructured = (chunk_metadata is not None and not chunk_metadata.node_types
                                  and end - start >= max_bytes)
        while (xml_comment_cursor < len(oversized_xml_comments)
               and oversized_xml_comments[xml_comment_cursor][1] <= start):
            xml_comment_cursor += 1
        oversized_xml_comment = (xml_comment_cursor < len(oversized_xml_comments)
                                 and oversized_xml_comments[xml_comment_cursor][0] < end
                                 and oversized_xml_comments[xml_comment_cursor][1] > start)
        oversized_mybatis_region = (start, end) in oversized_mybatis_chunks
        if oversized_unstructured or oversized_xml_comment or oversized_mybatis_region:
            chunk_quality = 'partial'
            degraded = True
        if oversized_unstructured:
            if partial_region is not None and partial_region['range']['end_byte'] == start:
                partial_region['range']['end_byte'] = end
                partial_region['range']['end_line'] = span(start, end)['end_line']
            else:
                partial_region = {
                    'code': 'oversized_unstructured_region',
                    'message': 'An oversized source region could not be divided at a recognized syntax boundary; retained as bounded original text',
                    'range': span(start, end),
                }
                diagnostics.append(partial_region)
        else:
            partial_region = None
        chunks.append({
            'content': content, 'range': span(start, end), 'quality': chunk_quality,
            'symbols': ([s['qualified_name'] for s in symbols if s['range']['start_byte'] < end and s['range']['end_byte'] > start]
                        + [((fact.get('namespace', '') + '.') if fact.get('namespace') else '')
                           + fact.get('name', fact.get('statement_type', fact['kind']))
                           for fact in overlapping_facts]),
            'context': [context(s) for s in symbols
                        if s['range']['start_byte'] <= start and s['range']['end_byte'] >= end][-2:],
        })
    if degraded and quality == 'structural':
        quality = 'partial'
    if cursor != len(raw):
        raise RuntimeError('chunker did not cover the entire original file')
    return {'parser_version': parser_version, 'sha256': hashlib.sha256(raw).hexdigest(),
            'byte_length': len(raw), 'encoding': 'utf-8-bom' if raw.startswith(b'\xef\xbb\xbf') else 'utf-8',
            'quality': quality, 'symbols': symbols, 'chunks': chunks, 'facts': facts, 'diagnostics': diagnostics}
