"""Bounded MyBatis/XML and SQLGlot facts over original source coordinates."""
from xml.parsers import expat
from bisect import bisect_left
import ast
import re

from sqlglot import exp, parse as parse_sql
from sqlglot.optimizer.scope import traverse_scope


MAX_XML_DEPTH = 128
MAX_XML_ELEMENTS = 200_000
MAX_FACTS = 50_000
DYNAMIC_TAGS = {"if", "choose", "when", "otherwise", "foreach", "where", "set", "trim", "bind"}
MAPPER_PUBLIC_ID = "-//mybatis.org//DTD Mapper 3.0//EN"
MAPPER_SYSTEM_ID = "http://mybatis.org/dtd/mybatis-3-mapper.dtd"
JAVA_TYPES = {"class_declaration", "interface_declaration", "enum_declaration", "record_declaration", "annotation_type_declaration"}
MYBATIS_SQL_ANNOTATIONS = {"Select", "Insert", "Update", "Delete"}
MYBATIS_PROVIDER_ANNOTATIONS = {"SelectProvider", "InsertProvider", "UpdateProvider", "DeleteProvider"}
MYBATIS_ANNOTATION_PREFIX = "org.apache.ibatis.annotations."


def _source_range(raw, start, end, newlines):
    start = max(0, min(start, len(raw)))
    end = max(start, min(end, len(raw)))
    last = max(start, end - 1)
    return {
        "start_byte": start,
        "end_byte": end,
        "start_line": bisect_left(newlines, start) + 1,
        "end_line": bisect_left(newlines, last) + 1,
    }


def _tag_end(raw, start):
    quote = None
    for index in range(start, len(raw)):
        byte = raw[index]
        if quote is not None:
            if byte == quote:
                quote = None
        elif byte in (ord("'"), ord('"')):
            quote = byte
        elif byte == ord(">"):
            return index + 1
    raise ValueError("unterminated XML tag")


def extract_java_facts(raw, tree, path):
    """Extract Java declarations/calls from Tree-sitter nodes, never source regexes."""
    facts, diagnostics = [], []
    newlines = [index for index, byte in enumerate(raw) if byte == 10]
    root = tree.root_node
    def text(node):
        return raw[node.start_byte:node.end_byte].decode("utf-8")

    def enclosing_type(node):
        current = node.parent
        while current is not None and current.type not in JAVA_TYPES:
            current = current.parent
        return current

    def binary_type_name(node):
        names = []
        current = node
        while current is not None:
            if current.type in ("method_declaration", "constructor_declaration", "static_initializer", "block"):
                diagnostics.append({"code": "java_nested_mapper_namespace_uncertain",
                                    "message": "Local or anonymous mapper type has no stable source-derived MyBatis namespace",
                                    "range": _source_range(raw, node.start_byte, node.end_byte, newlines)})
                return ""
            if current.type in JAVA_TYPES:
                name_node = current.child_by_field_name("name")
                if name_node is None:
                    return ""
                names.append(text(name_node))
            current = current.parent
        if not names:
            return ""
        names.reverse()
        return (package + "." if package else "") + "$".join(names)

    package = ""
    stack = [root]
    while stack:
        node = stack.pop()
        if node.type == "package_declaration":
            package_node = next((child for child in node.named_children
                                 if child.type in ("identifier", "scoped_identifier")), None)
            if package_node is not None:
                package = raw[package_node.start_byte:package_node.end_byte].decode("utf-8")
            break
        stack.extend(reversed(node.named_children))

    imports, declared_types = {}, {}
    stack = [root]
    while stack:
        node = stack.pop()
        if node.type in JAVA_TYPES:
            name_node = node.child_by_field_name("name")
            if name_node is not None:
                declared_types.setdefault(text(name_node), []).append(node)
        if node.type == "import_declaration":
            name_node = next((child for child in node.named_children
                              if child.type in ("identifier", "scoped_identifier")), None)
            if name_node is not None:
                imported = text(name_node)
                short = imported.rsplit(".", 1)[-1]
                imports.setdefault(short, []).append(imported)
            elif any(child.type == "asterisk" for child in node.children):
                prefix = next((text(child) for child in node.named_children if child.type in ("scoped_identifier", "identifier")), "")
                imports.setdefault("*", []).append(prefix)
        stack.extend(reversed(node.named_children))

    constants = {}
    stack = [root]
    while stack:
        node = stack.pop()
        if node.type in JAVA_TYPES:
            owner_constants = {}
            body = node.child_by_field_name("body")
            if body is not None:
                for declaration in body.named_children:
                    if declaration.type not in ("field_declaration", "constant_declaration"):
                        continue
                    modifiers = next((child for child in declaration.named_children if child.type == "modifiers"), None)
                    modifier_text = text(modifiers) if modifiers is not None else ""
                    is_interface = node.type in ("interface_declaration", "annotation_type_declaration")
                    if not is_interface and not ("static" in modifier_text and "final" in modifier_text):
                        continue
                    for declarator in declaration.named_children:
                        if declarator.type != "variable_declarator":
                            continue
                        name_node, value_node = declarator.child_by_field_name("name"), declarator.child_by_field_name("value")
                        value = _java_constant_value(raw, value_node, owner_constants) if value_node is not None else None
                        if name_node is not None and isinstance(value, str):
                            owner_constants[text(name_node)] = value
            constants[node.id] = owner_constants
        stack.extend(reversed(node.named_children))

    def annotation_identity(annotation, output_range):
        name_node = annotation.child_by_field_name("name")
        if name_node is None:
            return "unresolved"
        name = text(name_node)
        if name.startswith(MYBATIS_ANNOTATION_PREFIX):
            return name[len(MYBATIS_ANNOTATION_PREFIX):]
        if "." in name:
            return "unrelated"
        current_owner_types = []
        current_type = enclosing_type(annotation)
        while current_type is not None:
            current_name = current_type.child_by_field_name("name")
            if current_name is not None:
                current_owner_types.append(text(current_name))
            current_type = enclosing_type(current_type)
        def declaration_is_visible(candidate):
            candidate_container = enclosing_type(candidate)
            if candidate_container is None:
                return True
            container_name = candidate_container.child_by_field_name("name")
            return container_name is not None and text(container_name) in current_owner_types

        if any(declaration_is_visible(candidate) for candidate in declared_types.get(name, [])):
            diagnostics.append({"code": "java_mapper_annotation_identity_shadowed",
                                "message": "A source-declared type shadows the short mapper annotation name",
                                "range": output_range})
            return "unrelated"
        imports_for_name = imports.get(name, [])
        canonical = MYBATIS_ANNOTATION_PREFIX + name
        if imports_for_name:
            return name if imports_for_name == [canonical] else "unrelated"
        if MYBATIS_ANNOTATION_PREFIX.rstrip(".") in imports.get("*", []):
            diagnostics.append({"code": "java_mapper_annotation_identity_unknown",
                                "message": "Wildcard imports do not uniquely identify a short mapper annotation",
                                "range": output_range})
            return "unresolved"
        if name in MYBATIS_SQL_ANNOTATIONS | MYBATIS_PROVIDER_ANNOTATIONS:
            diagnostics.append({"code": "java_mapper_annotation_identity_unknown",
                                "message": "Mapper annotation identity is not resolved by an explicit MyBatis import or qualified name",
                                "range": output_range})
        return "unrelated"

    interfaces = {}
    stack = [root]
    while stack:
        node = stack.pop()
        if node.type == "interface_declaration":
            name_node = node.child_by_field_name("name")
            if name_node is not None:
                namespace = binary_type_name(node)
                if namespace:
                    interfaces[node.id] = (node, namespace)
        stack.extend(reversed(node.named_children))

    stack = [root]
    while stack:
        node = stack.pop()
        if node.type in ("method_declaration", "method_signature"):
            owner = node.parent
            while owner is not None and owner.type not in ("interface_body", "class_body", "enum_body"):
                owner = owner.parent
            interface = owner.parent if owner is not None and owner.type == "interface_body" else None
            if interface is not None and interface.id in interfaces:
                method = node.child_by_field_name("name")
                if method is not None:
                    name = raw[method.start_byte:method.end_byte].decode("utf-8")
                    start, end = node.start_byte, node.end_byte
                    facts.append({
                        "kind": "java_mapper_method", "name": name,
                        "namespace": interfaces[interface.id][1],
                        "range": _source_range(raw, start, end, newlines),
                        "text": raw[start:end].decode("utf-8"), "quality": "structural",
                    })
            method_node = node.child_by_field_name("name")
            method_name = raw[method_node.start_byte:method_node.end_byte].decode("utf-8") if method_node is not None else ""
            modifiers = next((child for child in node.named_children if child.type == "modifiers"), None)
            annotations = [] if modifiers is None else [child for child in modifiers.named_children if child.type == "annotation"]
            for annotation in annotations:
                annotation_range = _source_range(raw, annotation.start_byte, annotation.end_byte, newlines)
                annotation_name = annotation_identity(annotation, annotation_range)
                if annotation_name == "unresolved":
                    continue
                if annotation_name not in MYBATIS_SQL_ANNOTATIONS:
                    if annotation_name in MYBATIS_PROVIDER_ANNOTATIONS:
                        diagnostics.append({"code": "java_mapper_provider_dynamic", "message": "Provider SQL is not statically resolved", "range": annotation_range})
                    continue
                args = annotation.child_by_field_name("arguments")
                values = [] if args is None else list(args.named_children)
                if len(values) == 1 and values[0].type == "element_value_array_initializer":
                    values = list(values[0].named_children)
                parts, certain = [], bool(values)
                for value in values:
                    if value.type == "element_value_pair":
                        value = value.child_by_field_name("value")
                    owner = enclosing_type(node)
                    owner_constants = constants.get(owner.id, {}) if owner is not None else {}
                    constant = _java_constant_value(raw, value, owner_constants) if value is not None else None
                    if constant is None:
                        certain = False
                        break
                    parts.extend(constant if isinstance(constant, list) else [constant])
                start, end = annotation.start_byte, annotation.end_byte
                annotation_range = _source_range(raw, start, end, newlines)
                annotation_text = raw[start:end].decode("utf-8")
                if not certain:
                    diagnostics.append({"code": "java_mapper_sql_dynamic", "message": "Mapper SQL annotation is not a literal or supported constant expression", "range": annotation_range})
                    continue
                sql = " ".join(parts)
                mapper_namespace = ""
                owner_type = enclosing_type(node)
                if owner_type is not None and owner_type.type == "interface_declaration" and owner_type.id in interfaces:
                    mapper_namespace = interfaces[owner_type.id][1]
                facts.append({"kind": "java_annotation_sql", "name": annotation_name,
                              "method_name": method_name, "namespace": mapper_namespace, "sql": sql,
                              "certainty": "certain", "range": annotation_range,
                              "text": annotation_text, "quality": "structural"})
                try:
                    table_names, sql_dynamic = _sql_tables_and_diagnostics(sql)
                except Exception:
                    table_names, sql_dynamic = [], True
                    diagnostics.append({"code": "java_annotation_sql_parse_uncertain", "message": "SQLGlot could not parse mapper annotation SQL as MySQL", "range": annotation_range})
                facts.extend({"kind": "sql_table", **values, "range": annotation_range, "quality": "structural"}
                             for values in _sql_table_fact_values(table_names, mapper_namespace, method_name,
                                                                  "java_annotation", sql_dynamic))
        stack.extend(reversed(node.named_children))
        if len(facts) > MAX_FACTS:
            raise ValueError("Java fact limit exceeded")
    facts.sort(key=lambda fact: (fact["range"]["start_byte"], fact["kind"], fact["name"]))
    return facts, diagnostics


def _java_constant_value(raw, node, constants=None):
    if node is None:
        return None
    if node.type == "string_literal":
        text = raw[node.start_byte:node.end_byte].decode("utf-8")
        if text.startswith('"""'):
            return None
        try:
            return ast.literal_eval(text)
        except (ValueError, SyntaxError):
            return None
    if node.type in ("identifier", "type_identifier"):
        name = raw[node.start_byte:node.end_byte].decode("utf-8")
        return (constants or {}).get(name)
    if node.type in ("array_initializer", "element_value_array_initializer"):
        values = [_java_constant_value(raw, child, constants) for child in node.named_children]
        return values if values and all(isinstance(value, str) for value in values) else None
    if node.type == "binary_expression" and node.child_count == 3:
        operator = node.child(1)
        if operator is not None and raw[operator.start_byte:operator.end_byte] == b"+":
            left = _java_constant_value(raw, node.child(0), constants)
            right = _java_constant_value(raw, node.child(2), constants)
            if isinstance(left, str) and isinstance(right, str):
                return left + right
    return None


def _tables(sql):
    names = []
    def physical_table(table):
        # In SQLGlot 30.20 a table function stores an expression (rather than
        # an Identifier) in Table.this. This excludes JSON_TABLE/UNNEST-like
        # call sites without guessing from SQL text.
        return isinstance(table, exp.Table) and isinstance(table.this, exp.Identifier)

    def physical_name(table):
        parts = [table.catalog, table.db, table.name]
        return ".".join(part for part in parts if part)

    def is_cte_reference(table):
        if table.db or table.catalog:
            return False
        name = table.name.casefold()
        current = table.parent
        while current is not None:
            with_clause = current.args.get("with_") if hasattr(current, "args") else None
            if isinstance(with_clause, exp.With):
                for cte in with_clause.expressions:
                    if cte.alias_or_name.casefold() != name:
                        continue
                    ancestor = table
                    inside_own_definition = False
                    while ancestor is not None and ancestor is not current:
                        if ancestor is cte:
                            inside_own_definition = True
                            break
                        ancestor = ancestor.parent
                    if inside_own_definition and not with_clause.args.get("recursive"):
                        continue
                    return True
            current = current.parent
        return False

    for statement in parse_sql(sql, read="mysql"):
        if statement is None:
            continue
        if statement.key in ("insert", "update", "delete", "merge"):
            using_sources = statement.args.get("using")
            seen = set()
            delete_target_aliases = set()
            if statement.key == "delete":
                targets = statement.args.get("tables") or []
                delete_target_aliases.update(targets)
                if using_sources:
                    target = statement.args.get("this")
                    if isinstance(target, exp.Table):
                        delete_target_aliases.add(target)
            # DML roots are not consistently traversed by SQLGlot's Scope
            # walker (notably UPDATE ... JOIN (SELECT ...)). Walk real Table
            # AST nodes so derived-query sources are included, while applying
            # CTE visibility at each node's lexical ancestors.
            for table in statement.find_all(exp.Table):
                if table in delete_target_aliases or not physical_table(table) or is_cte_reference(table):
                    continue
                name = physical_name(table)
                if name and name not in seen:
                    seen.add(name)
                    names.append(name)
            continue
        for scope in traverse_scope(statement):
            for source in scope.sources.values():
                if physical_table(source):
                    name = physical_name(source)
                    if name:
                        names.append(name)
    return names


def _sql_tables_and_diagnostics(sql):
    """Normalize only MyBatis bind syntax, then let SQLGlot own SQL semantics."""
    dynamic_identifier = re.compile(r"\$\{[^{}]*\}")
    bind_parameter = re.compile(r"#\{[^{}]*\}")
    dynamic = bool(dynamic_identifier.search(sql))
    normalized = dynamic_identifier.sub("__mybatis_dynamic_identifier__", sql)
    normalized = bind_parameter.sub("?", normalized)
    return _tables(normalized), dynamic


def _sql_table_fact_values(table_names, namespace, statement_id, owner_kind, dynamic):
    """Share table identity and certainty across Java, statements, and fragments."""
    return [{
        "name": name,
        "namespace": namespace,
        "statement_id": statement_id,
        "owner_kind": owner_kind,
        "dynamic": dynamic,
        "certainty": "uncertain" if dynamic else "certain",
    } for name in table_names if name != "__mybatis_dynamic_identifier__"]


def parse_mybatis_xml(raw):
    """Parse mapper structure with Expat, disallow all DTD fetch/expansion."""
    parser = expat.ParserCreate(namespace_separator="}")
    parser.SetParamEntityParsing(expat.XML_PARAM_ENTITY_PARSING_NEVER)
    nodes, facts, diagnostics, roots = [], [], [], []
    element_count = 0
    newlines = [index for index, byte in enumerate(raw) if byte == 10]
    root = None

    def start_doctype(name, system_id, public_id, has_internal_subset):
        if (name != "mapper" or public_id != MAPPER_PUBLIC_ID or system_id != MAPPER_SYSTEM_ID
                or has_internal_subset):
            raise ValueError("unsupported or unsafe XML document type")

    def reject_external(*_args):
        return 0

    def start_element(name, attrs):
        nonlocal root, element_count
        element_count += 1
        if element_count > MAX_XML_ELEMENTS or len(nodes) >= MAX_XML_DEPTH:
            raise ValueError("XML structural limit exceeded")
        start = parser.CurrentByteIndex
        node = {"name": name.split("}")[-1].lower(), "attrs": attrs, "start": start,
                "start_end": _tag_end(raw, start), "end": None, "children": [], "text": [], "content": []}
        if nodes:
            nodes[-1]["children"].append(node)
            nodes[-1]["content"].append(node)
        else:
            roots.append(node)
        nodes.append(node)
        root = root or node

    def end_element(_name):
        node = nodes.pop()
        current = parser.CurrentByteIndex
        # Expat may report either the opening boundary or the byte after `/>`
        # for an empty-element end event; the measured delimiter is decisive.
        if raw[node["start_end"] - 2:node["start_end"]] == b"/>":
            end = node["start_end"]
        else:
            end = _tag_end(raw, current)
        node["end"] = end

    def char_data(value):
        if nodes:
            nodes[-1]["text"].append(value)
            nodes[-1]["content"].append(value)

    parser.StartDoctypeDeclHandler = start_doctype
    parser.ExternalEntityRefHandler = reject_external
    parser.StartElementHandler = start_element
    parser.EndElementHandler = end_element
    parser.CharacterDataHandler = char_data
    try:
        parser.Parse(raw, True)
    except (expat.ExpatError, ValueError, IndexError):
        raise ValueError("invalid or unsafe MyBatis XML") from None
    if root is None or root["name"] != "mapper":
        return {"quality": "text_fallback", "facts": [], "diagnostics": [{
            "code": "xml_not_mybatis_mapper", "message": "XML is valid but is not a MyBatis mapper",
        }]}

    def node_range(node):
        return _source_range(raw, node["start"], node["end"] or node["start_end"], newlines)

    def node_fact(kind, node, **values):
        span = node_range(node)
        fact = {"kind": kind, **values, "range": span, "quality": "structural"}
        # Full raw text is redundant for table facts and can multiply a large
        # statement by its table count. Hash + exact range remain authoritative.
        if kind != "sql_table" and span["end_byte"] - span["start_byte"] <= (64 << 10):
            fact["text"] = raw[span["start_byte"]:span["end_byte"]].decode("utf-8")
        return fact

    def reference_fact(kind, node, owner_kind, owner_name, reference_kind, reference):
        reference = reference.strip()
        if "." in reference:
            target_namespace, target_name = reference.rsplit(".", 1)
        else:
            target_namespace, target_name = namespace, reference
        dynamic = bool(re.search(r"\$\{[^{}]*\}", reference))
        if dynamic:
            diagnostics.append({
                "code": "mybatis_reference_dynamic",
                "message": "MyBatis reference contains a dynamic identifier",
                "range": node_range(node),
            })
        fields = {
            "name": reference, "namespace": namespace,
            "target_namespace": target_namespace, "target_name": target_name,
            "owner_kind": owner_kind, "owner_name": owner_name,
            "reference_kind": reference_kind, "dynamic": dynamic,
        }
        if kind == "mybatis_include":
            fields["statement_id"] = owner_name if owner_kind == "mybatis_statement" else ""
        facts.append(node_fact(kind, node, **fields))

    def descendants(node):
        for child in node["children"]:
            yield child
            yield from descendants(child)

    def text_content(node):
        return "".join(text_content(part) if isinstance(part, dict) else part for part in node["content"])

    namespace = root["attrs"].get("namespace", "")
    facts.append(node_fact("mybatis_mapper", root, namespace=namespace))
    if not namespace:
        diagnostics.append({"code": "mapper_namespace_missing", "message": "Mapper has no namespace", "range": node_range(root)})

    statement_nodes = []
    sql_nodes = []
    for node in descendants(root):
        kind = node["name"]
        if kind in ("select", "insert", "update", "delete"):
            statement_nodes.append(node)
        elif kind == "resultmap":
            ident = node["attrs"].get("id", "")
            facts.append(node_fact("mybatis_result_map", node, name=ident, namespace=namespace))
            if not ident:
                diagnostics.append({"code": "result_map_id_missing", "message": "resultMap has no id", "range": node_range(node)})
            if node["attrs"].get("extends", "").strip():
                reference_fact("mybatis_result_map_reference", node, "mybatis_result_map", ident,
                               "extends", node["attrs"]["extends"])
            for child in descendants(node):
                if child["name"] in ("association", "collection", "case"):
                    reference = child["attrs"].get("resultMap", "").strip()
                    if reference:
                        reference_fact("mybatis_result_map_reference", child, "mybatis_result_map", ident,
                                       child["name"], reference)
        elif kind == "sql":
            sql_nodes.append(node)
            ident = node["attrs"].get("id", "")
            facts.append(node_fact("mybatis_sql_fragment", node, name=ident, namespace=namespace))
            if not ident:
                diagnostics.append({"code": "sql_fragment_id_missing", "message": "SQL fragment has no id", "range": node_range(node)})

    statement_counts = {}
    for node in statement_nodes:
        ident = node["attrs"].get("id", "")
        statement_counts[ident] = statement_counts.get(ident, 0) + 1
    for node in statement_nodes:
        ident = node["attrs"].get("id", "")
        descendants_of_statement = list(descendants(node))
        result_refs = [r.strip() for r in node["attrs"].get("resultMap", "").split(",") if r.strip()]
        includes = [child for child in descendants_of_statement if child["name"] == "include"]
        include_refs = [child["attrs"].get("refid", "") for child in includes if child["attrs"].get("refid")]
        dynamic = any(child["name"] in DYNAMIC_TAGS for child in descendants_of_statement)
        statement_fact = node_fact("mybatis_statement", node, name=ident, namespace=namespace,
                                   statement_type=node["name"], result_map_refs=result_refs,
                                   include_refs=include_refs, dynamic=dynamic)
        facts.append(statement_fact)
        if not ident:
            diagnostics.append({"code": "statement_id_missing", "message": "Mapper statement has no id", "range": node_range(node)})
        elif statement_counts[ident] > 1:
            diagnostics.append({"code": "statement_id_duplicate", "message": "Mapper statement id is duplicated", "range": node_range(node)})
        for child in includes:
            refid = child["attrs"].get("refid", "")
            if refid:
                reference_fact("mybatis_include", child, "mybatis_statement", ident, "include", refid)
        sql = text_content(node)
        try:
            table_names, sql_dynamic = _sql_tables_and_diagnostics(sql)
        except Exception:
            table_names = []
            sql_dynamic = True
            diagnostics.append({"code": "sql_parse_uncertain", "message": "SQLGlot could not parse mapper SQL as MySQL", "range": node_range(node)})
        facts.extend(node_fact("sql_table", node, **values)
                     for values in _sql_table_fact_values(table_names, namespace, ident, "mybatis_statement",
                                                          dynamic or sql_dynamic))

    for node in sql_nodes:
        ident = node["attrs"].get("id", "")
        for child in descendants(node):
            if child["name"] == "include":
                refid = child["attrs"].get("refid", "")
                if refid:
                    reference_fact("mybatis_include", child, "mybatis_sql_fragment", ident, "include", refid)
        sql = text_content(node)
        fragment_dynamic = any(child["name"] in DYNAMIC_TAGS for child in descendants(node))
        if fragment_dynamic:
            diagnostics.append({"code": "sql_fragment_dynamic", "message": "SQL fragment contains dynamic MyBatis branches", "range": node_range(node)})
        try:
            table_names, sql_dynamic = _sql_tables_and_diagnostics(sql)
        except Exception:
            table_names = []
            sql_dynamic = True
            diagnostics.append({"code": "sql_parse_uncertain", "message": "SQLGlot could not parse SQL fragment as MySQL", "range": node_range(node)})
        facts.extend(node_fact("sql_table", node, **values)
                     for values in _sql_table_fact_values(table_names, namespace, ident, "mybatis_sql_fragment",
                                                          fragment_dynamic or sql_dynamic))

    fragment_counts = {}
    for node in sql_nodes:
        ident = node["attrs"].get("id", "")
        fragment_counts[ident] = fragment_counts.get(ident, 0) + 1
    for ident, count in fragment_counts.items():
        if ident and count > 1:
            diagnostics.append({"code": "sql_fragment_id_duplicate", "message": "SQL fragment id is duplicated"})

    counts = {}
    for node in descendants(root):
        if node["name"] == "resultmap":
            ident = node["attrs"].get("id", "")
            counts[ident] = counts.get(ident, 0) + 1
    for ident, count in counts.items():
        if ident and count > 1:
            diagnostics.append({"code": "result_map_id_duplicate", "message": "resultMap id is duplicated"})
    if len(facts) > MAX_FACTS:
        raise ValueError("MyBatis fact limit exceeded")
    facts.sort(key=lambda fact: (fact["range"]["start_byte"], fact["kind"], fact.get("name", "")))
    return {"quality": "structural", "facts": facts, "diagnostics": diagnostics}
