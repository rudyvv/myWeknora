"""Deterministic semantic boundaries over verified source bytes.

Boundaries are suggestions, never rewritten text. Prefixes and separators are
owned by the next/previous unit instead of becoming standalone index chunks.
"""
from bisect import bisect_left, bisect_right


UNITS = frozenset({
    'expression_statement', 'lexical_declaration', 'variable_declaration',
    'local_variable_declaration', 'field_declaration', 'constant_declaration',
    'return_statement', 'throw_statement', 'break_statement', 'continue_statement',
    'assignment', 'augmented_assignment', 'pass_statement', 'raise_statement',
    'assert_statement', 'delete_statement', 'global_statement', 'nonlocal_statement',
    'if_statement', 'for_statement', 'for_in_statement', 'while_statement',
    'do_statement', 'try_statement', 'switch_statement', 'with_statement',
    'import_statement', 'import_from_statement', 'import_declaration',
    'package_declaration', 'export_statement', 'export_declaration',
    'function_declaration', 'function_definition', 'method_declaration',
    'constructor_declaration', 'method_definition', 'decorated_definition',
    'class_declaration', 'class_definition', 'interface_declaration',
    'type_alias_declaration', 'enum_declaration', 'internal_module',
    'pair', 'property_signature', 'public_field_definition', 'field_definition', 'enum_assignment',
    'jsx_element', 'jsx_self_closing_element', 'comment',
})
OPAQUE = frozenset({'string', 'string_literal', 'template_string', 'comment',
                    'character_literal', 'regex', 'raw_string_literal'})


def utf8_cut(raw, start, limit):
    cut = min(len(raw), limit)
    while cut < len(raw) and cut > start and raw[cut] & 0xC0 == 0x80:
        cut -= 1
    if cut <= start:
        raise RuntimeError('chunk budget cannot hold a UTF-8 codepoint')
    return cut


def pack_boundaries(raw, maximum, boundaries):
    """Pack adjacent semantic units, reserving the file's final delimiters.

    A forced split is reported separately so callers cannot claim a lexical
    fragment is structurally complete. Complete short units are never split
    merely to fill the preceding chunk.
    """
    semantic_ends = {b for b in boundaries if 0 < b <= len(raw)} | {len(raw)}
    ends = sorted(semantic_ends)
    ranges, forced = [], []
    start = 0
    while start < len(raw):
        limit = min(len(raw), start + maximum)
        i = bisect_right(ends, limit) - 1
        cut = ends[i] if i >= 0 and ends[i] > start else 0
        if not cut:
            cut = utf8_cut(raw, start, limit)
            newline = raw.rfind(b'\n', start, cut)
            if newline >= start + maximum // 2:
                cut = newline + 1
        # Prefer attaching a short suffix to this chunk. If it cannot fit,
        # leave space in the previous unit for the suffix in the next chunk.
        if (cut < len(raw) and len(raw) - cut < 48
                and not raw[cut:].strip(b' \t\r\n;)}]>')):
            if len(raw) - start <= maximum:
                cut = len(raw)
            elif i > 0 and ends[i - 1] > start:
                cut = ends[i - 1]
            elif limit - start > 48:
                # A long indivisible leaf may fill the entire budget. Reserve
                # a little payload with its suffix instead of emitting only
                # punctuation in the final chunk.
                cut = utf8_cut(raw, start, limit - 48)
        if cut not in semantic_ends:
            forced.append(cut)
        ranges.append((start, cut))
        start = cut
    return ranges, forced


def structural_ranges(raw, root, maximum):
    ends, opaque = [], []
    stack = [root]
    while stack:
        node = stack.pop()
        size = node.end_byte - node.start_byte
        if node.type in OPAQUE:
            opaque.append((node.start_byte, node.end_byte))
            if size <= maximum and (node.type == 'comment' or (node.parent is not None and
                    node.parent.type in ('array', 'list', 'tuple', 'set', 'arguments', 'argument_list'))):
                ends.append(node.end_byte)
            continue
        if size <= maximum and (node.type in UNITS or (node.parent is not None and
                node.parent.type in ('array', 'list', 'tuple', 'set', 'arguments', 'argument_list'))):
            ends.append(node.end_byte)
        if node.type in ('class_declaration', 'class_definition', 'interface_declaration',
                         'internal_module', 'function_definition', 'function_declaration',
                         'method_definition', 'method_declaration', 'constructor_declaration'):
            body = node.child_by_field_name('body')
            if body is not None:
                # A complete named signature is a useful boundary when it and
                # the first body unit cannot fit together. Never offer an
                # identifier, keyword or expression prefix as a boundary.
                ends.append(body.start_byte + (1 if raw[body.start_byte:body.start_byte + 1] == b'{' else 0))
        stack.extend(reversed(node.named_children))
    # Include whitespace and closing punctuation with a complete unit, up to
    # the next substantive unit. This absorbs commas/newlines without losing
    # exact coverage, and avoids treating a closing brace as a unit itself.
    adjusted = list(ends)
    previous_end = 0
    for end in sorted(set(ends)):
        if end <= previous_end:
            adjusted.append(previous_end)
            continue
        while end < len(raw) and raw[end] in b' \t\r\n,;)}]':
            end += 1
        adjusted.append(end)
        previous_end = end
    ranges, forced = pack_boundaries(raw, maximum, adjusted)
    forced.sort()
    opaque.sort()
    opaque_starts = [a for a, _ in opaque]
    def inside_opaque(point):
        index = bisect_left(opaque_starts, point) - 1
        return index >= 0 and point < opaque[index][1]
    partial = {(start, end) for start, end in ranges
               if bisect_right(forced, end) > bisect_right(forced, start)
               or inside_opaque(start) or inside_opaque(end)}
    return ranges, partial


def attach_whitespace(raw, ranges, maximum):
    """Absorb small gaps without combining independent MyBatis statements."""
    result = []
    pending = None
    for start, end in ranges:
        if pending is not None:
            if end - pending <= maximum:
                start = pending
            else:
                result.append((pending, start))
            pending = None
        if not raw[start:end].strip() or raw[start:end].strip() == b'</mapper>':
            if result and end - result[-1][0] <= maximum:
                result[-1] = (result[-1][0], end)
            else:
                pending = start
        else:
            result.append((start, end))
    if pending is not None:
        result.append((pending, len(raw)))
    return result


def text_ranges(raw, maximum):
    """Line-based fallback: prefer paragraphs, then complete source lines.

    No structural claim is made for unparsed formats. Long lines alone need
    bounded UTF-8 cuts; neighbouring normal lines retain their boundaries.
    """
    ends, cursor = [], 0
    for line in raw.splitlines(keepends=True):
        cursor += len(line)
        # Shell/Docker continuations belong with the following line.
        if not line.rstrip(b'\r\n').endswith(b'\\'):
            ends.append(cursor)
    return pack_boundaries(raw, maximum, ends)[0]
