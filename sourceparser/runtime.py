"""Locked Java runtime and a small provenance adapter over the mature chunker."""
import hashlib
from bisect import bisect_left
from importlib.metadata import version
import json
from pathlib import Path

import tree_sitter_language_pack as pack

PACK_VERSION = '1.19.0'
BUNDLES = {
    'linux-x86_64': '86995c25a95d59a1235276c8bdfc5156f7ffb1db1d53653c9e58a60e92d4346e',
    'linux-aarch64': '4e0cf38459547f10fd2c0f33c783a9c92e744baf2c591453d59233d15d0ab2de',
    'windows-x86_64': '1fd72fbba863c57445f0c570804561a4c569584edf79067d0651e450b6222609',
}


def load_runtime(cache):
    """Never prefetch here. A missing or changed grammar keeps this worker unready."""
    if version('tree-sitter-language-pack') != PACK_VERSION or version('tree-sitter') != '0.26.0':
        raise RuntimeError('parser dependency version differs from requirements.lock')
    cache = Path(cache).resolve()
    lock = json.loads((cache / 'grammar.lock.json').read_text(encoding='utf-8'))
    if lock['pack_version'] != PACK_VERSION or lock['bundle_sha256'] not in BUNDLES.values():
        raise RuntimeError('grammar release differs from the locked release')
    relative = Path(lock['grammar'])
    expected = Path('tree-sitter-language-pack') / ('v' + PACK_VERSION) / 'libs'
    if relative.parent != expected or relative.name not in ('tree_sitter_java.dll', 'libtree_sitter_java.so', 'tree_sitter_java.so'):
        raise RuntimeError('invalid locked grammar path')
    grammar = cache / relative
    if grammar.resolve().parent != (cache / expected).resolve() or any(p.is_symlink() for p in [grammar, grammar.parent, grammar.parent.parent, grammar.parent.parent.parent]) or hashlib.sha256(grammar.read_bytes()).hexdigest() != lock['grammar_sha256']:
        raise RuntimeError('grammar checksum mismatch')
    pack.configure(pack.PackConfig(cache_dir=str(cache)))
    # Only an already verified Java library can reach the language registry.
    pack.get_parser('java')
    return 'java-pack-' + PACK_VERSION + '-' + lock['grammar_sha256'][:16]


def parse_java(raw, max_bytes, parser_version):
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
        language='java', structure=True, symbols=True, diagnostics=True,
        chunk_max_size=max_bytes, max_source_bytes=16 << 20, parse_timeout_ms=4000,
    ))
    tree = pack.get_parser('java').parse(raw)
    symbols = []
    declarations = {
        'class_declaration': 'class', 'interface_declaration': 'interface',
        'enum_declaration': 'enum', 'annotation_type_declaration': 'annotation',
        'method_declaration': 'method', 'constructor_declaration': 'constructor',
    }
    # Extract named declarations and annotation ranges, not a handwritten grammar.
    stack = [(tree.root_node, [])]
    while stack:
        node, parents = stack.pop()
        lineage = parents
        if node.type in declarations:
            name = node.child_by_field_name('name')
            if name is not None:
                name_text = raw[name.start_byte:name.end_byte].decode('utf-8')
                lineage = parents + [name_text]
                body = node.child_by_field_name('body')
                signature_end = body.start_byte if body is not None else node.end_byte
                signature = raw[node.start_byte:signature_end].decode('utf-8').rstrip()
                signature_end = node.start_byte + len(signature.encode('utf-8'))
                annotations = []
                for child in node.named_children:
                    if child.type == 'modifiers':
                        for modifier in child.named_children:
                            if modifier.type in ('annotation', 'marker_annotation'):
                                annotations.append({'text': raw[modifier.start_byte:modifier.end_byte].decode('utf-8'),
                                                    'range': span(modifier.start_byte, modifier.end_byte)})
                symbols.append({
                    'kind': declarations[node.type], 'name': name_text, 'qualified_name': '.'.join(lineage),
                    'signature': signature, 'signature_range': span(node.start_byte, signature_end),
                    'annotations': annotations, 'range': span(node.start_byte, node.end_byte),
                })
                if len(symbols) > 10000:
                    raise RuntimeError('source declaration limit exceeded')
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
            # Context is separate from the contiguous raw fragment and carries its own range.
            'context': [context(s) for s in symbols
                        if s['range']['start_byte'] <= start and s['range']['end_byte'] >= end][-2:],
        })
    if cursor != len(raw):
        raise RuntimeError('chunker did not cover the entire original file')
    return {'parser_version': parser_version, 'sha256': hashlib.sha256(raw).hexdigest(),
            'byte_length': len(raw), 'encoding': 'utf-8-bom' if raw.startswith(b'\xef\xbb\xbf') else 'utf-8',
            'quality': quality, 'symbols': symbols, 'chunks': chunks}
