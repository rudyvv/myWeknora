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
SPRING_MAPPING_PREFIX = "org.springframework.web.bind.annotation."
SPRING_MAPPING_METHODS = {
    "GetMapping": "GET", "PostMapping": "POST", "PutMapping": "PUT",
    "PatchMapping": "PATCH", "DeleteMapping": "DELETE",
}
INJECTION_ANNOTATIONS = {
    "org.springframework.beans.factory.annotation.Autowired",
    "javax.annotation.Resource", "jakarta.annotation.Resource",
    "javax.inject.Inject", "jakarta.inject.Inject",
}
JAVA_LANG_TYPES = {
    "Appendable", "AutoCloseable", "Boolean", "Byte", "Character", "CharSequence", "Class", "ClassLoader",
    "Cloneable", "Comparable", "Deprecated", "Double", "Enum", "Error", "Exception", "Float", "Integer",
    "Iterable", "Long", "Math", "Number", "Object", "Override", "Process", "Record", "Runnable", "Runtime",
    "RuntimeException", "Short", "String", "StringBuffer", "StringBuilder", "System", "Thread", "Throwable",
    "Void",
}


class UnsafeMyBatisXML(ValueError):
    """An explicit DTD/entity policy rejection, never eligible for text fallback."""


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


def _xml_error_range(raw, error_byte, newlines):
    """Point at the offending markup token or UTF-8 code point in original bytes."""
    position = max(0, min(error_byte, len(raw)))
    opening = raw.rfind(b"<", 0, position + 1)
    closing = raw.rfind(b">", 0, position + 1)
    token_end = raw.find(b">", position)
    if opening > closing and token_end >= position:
        return _source_range(raw, opening, token_end + 1, newlines)
    if position == len(raw):
        return _source_range(raw, position, position, newlines)

    start = position
    while start > 0 and raw[start] & 0xC0 == 0x80:
        start -= 1
    lead = raw[start]
    width = 1 if lead < 0x80 else 2 if lead < 0xE0 else 3 if lead < 0xF0 else 4
    return _source_range(raw, start, min(len(raw), start + width), newlines)


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
            if any(child.type == "asterisk" for child in node.children):
                prefix = next((text(child) for child in node.named_children
                               if child.type in ("scoped_identifier", "identifier")), "")
                if prefix:
                    imports.setdefault("*", []).append(prefix)
            elif name_node is not None:
                imported = text(name_node)
                short = imported.rsplit(".", 1)[-1]
                imports.setdefault(short, []).append(imported)
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

    def annotation_fqn(annotation, allowed_prefix):
        """Resolve a framework annotation only through its qualified name or import."""
        name_node = annotation.child_by_field_name("name")
        if name_node is None:
            return ""
        name = text(name_node)
        if name.startswith(allowed_prefix):
            return name
        if "." in name:
            return ""
        if declared_types.get(name):
            return ""
        imported = imports.get(name, [])
        if len(imported) == 1:
            return imported[0]
        if imported:
            return ""
        wildcard_matches = [prefix for prefix in imports.get("*", []) if prefix == allowed_prefix.rstrip(".")]
        return allowed_prefix + name if len(wildcard_matches) == 1 else ""

    def enclosing_java_type(node):
        current = node
        while current is not None and current.type not in JAVA_TYPES:
            current = current.parent
        return current

    def enclosing_java_method(node):
        current = node
        while current is not None and current.type not in ("method_declaration", "method_signature", "constructor_declaration"):
            current = current.parent
        return current

    def base_type_node(node):
        if node is None:
            return None
        if node.type in ("identifier", "type_identifier", "scoped_identifier", "scoped_type_identifier",
                         "integral_type", "floating_point_type", "boolean_type"):
            return node
        # Generic and array types wrap the declared base type. Never use a
        # generic argument as the injected receiver's identity.
        for child in node.named_children:
            found = base_type_node(child)
            if found is not None:
                return found
        return None

    declared_fqns = set()
    type_nodes = []
    stack = [root]
    while stack:
        node = stack.pop()
        if node.type in JAVA_TYPES:
            qualified = binary_type_name(node)
            name_node = node.child_by_field_name("name")
            if qualified and name_node is not None:
                declared_fqns.add(qualified)
                type_nodes.append((node, qualified, text(name_node)))
        stack.extend(reversed(node.named_children))

    def resolve_type_node(node):
        base = base_type_node(node)
        if base is None:
            return "", False
        value = text(base)
        if not value or value in {"void", "boolean", "byte", "char", "short", "int", "long", "float", "double"}:
            return "", False
        if "." in value:
            return value, True
        explicit = imports.get(value, [])
        if len(explicit) == 1:
            return explicit[0], True
        if explicit:
            return "", False
        same_package = (package + "." if package else "") + value
        if same_package in declared_fqns:
            return same_package, True
        wildcard = sorted(set(imports.get("*", [])))
        candidates = [(prefix + "." + value) for prefix in wildcard]
        source_candidates = [candidate for candidate in candidates if candidate in declared_fqns]
        if len(source_candidates) == 1:
            return source_candidates[0], True
        if len(wildcard) == 1:
            return candidates[0], True
        if not wildcard and package:
            # Same-package resolution is deterministic in Java. The Go
            # correlator still requires this exact type to exist in the snapshot.
            return same_package, True
        return "", False

    def parameter_signature(method_node):
        parameters = method_node.child_by_field_name("parameters")
        if parameters is None:
            return [], False
        parameter_types, signature_certain = [], True
        primitives = {"boolean", "byte", "char", "short", "int", "long", "float", "double"}
        for parameter in parameters.named_children:
            type_node = parameter.child_by_field_name("type")
            if type_node is None and parameter.type == "spread_parameter":
                type_node = next((child for child in parameter.named_children if child.type != "variable_declarator"), None)
            if type_node is None:
                parameter_types.append("")
                signature_certain = False
                continue

            raw_type = "".join(text(type_node).split())
            base = base_type_node(type_node)
            if base is None and type_node.type in ("integral_type", "floating_point_type", "boolean_type"):
                base = type_node
            if base is None:
                parameter_types.append(raw_type)
                signature_certain = False
                continue

            base_name = text(base)
            if base.type in ("integral_type", "floating_point_type", "boolean_type") or base_name in primitives:
                resolved, certain = base_name, True
            elif "." in base_name:
                resolved, certain = base_name, True
            elif len(imports.get(base_name, [])) == 1:
                resolved, certain = imports[base_name][0], True
            elif imports.get(base_name):
                resolved, certain = base_name, False
            elif base_name in JAVA_LANG_TYPES:
                resolved, certain = "java.lang." + base_name, True
            else:
                same_package = (package + "." if package else "") + base_name
                if same_package in declared_fqns:
                    resolved, certain = same_package, True
                else:
                    wildcard = sorted(set(imports.get("*", [])))
                    source_candidates = [prefix + "." + base_name for prefix in wildcard
                                         if prefix + "." + base_name in declared_fqns]
                    if len(source_candidates) == 1:
                        resolved, certain = source_candidates[0], True
                    elif len(wildcard) == 1:
                        resolved, certain = wildcard[0] + "." + base_name, True
                    else:
                        resolved, certain = base_name, False

            suffix = raw_type[len(base_name):] if raw_type.startswith(base_name) else ""
            if parameter.type == "spread_parameter":
                suffix = "[]"
            if "<" in suffix or "?" in suffix:
                certain = False
            parameter_types.append(resolved + suffix)
            signature_certain = signature_certain and certain
        return parameter_types, signature_certain

    def static_string_values(node, owner_type):
        if node is None:
            return None
        if node.type in ("string_literal", "element_value_array_initializer", "array_initializer"):
            return _java_constant_value(raw, node, constants.get(owner_type.id, {}) if owner_type else {})
        if node.type == "element_value_pair":
            return static_string_values(node.child_by_field_name("value"), owner_type)
        return None

    def mapping_values(annotation, owner_type):
        args = annotation.child_by_field_name("arguments")
        path_nodes, method_nodes = [], []
        if args is not None:
            for argument in args.named_children:
                if argument.type == "element_value_pair":
                    key_node = argument.child_by_field_name("key")
                    key = text(key_node) if key_node is not None else ""
                    if key in ("value", "path"):
                        path_nodes.append(argument)
                    elif key == "method":
                        method_nodes.append(argument)
                else:
                    path_nodes.append(argument)
        if not path_nodes:
            paths = [""]
            paths_certain = True
        else:
            values = [static_string_values(item, owner_type) for item in path_nodes]
            paths_certain = all(isinstance(value, (str, list)) for value in values)
            flattened = []
            for value in values:
                if isinstance(value, str):
                    flattened.append(value)
                elif isinstance(value, list) and all(isinstance(part, str) for part in value):
                    flattened.extend(value)
                else:
                    paths_certain = False
            paths = flattened or [""]
        annotation_name = text(annotation.child_by_field_name("name")) if annotation.child_by_field_name("name") else ""
        simple_name = annotation_name.rsplit(".", 1)[-1]
        fixed_method = SPRING_MAPPING_METHODS.get(simple_name)
        methods = [fixed_method] if fixed_method else []
        method_declared = fixed_method is not None
        methods_certain = True
        for item in method_nodes:
            method_declared = True
            value_node = item.child_by_field_name("value")
            if value_node is None:
                methods_certain = False
                continue
            raw_method = text(value_node).strip()
            if raw_method.startswith("{") or raw_method.endswith("}"):
                if not (raw_method.startswith("{") and raw_method.endswith("}")):
                    methods_certain = False
                    continue
                raw_method = raw_method[1:-1].strip()
            allowed_methods = {"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE"}
            found = []
            for token in raw_method.split(","):
                match = re.fullmatch(r"(?:RequestMethod\s*\.\s*)?(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE)", token.strip())
                if match is None:
                    found = []
                    break
                found.append(match.group(1))
            if not found or any(value not in allowed_methods for value in found):
                methods_certain = False
            else:
                methods.extend(found)
        return paths, sorted(set(methods)), paths_certain, methods_certain, method_declared

    # Preserve explicit Java imports and declared type hierarchy as syntax facts.
    stack = [root]
    while stack:
        node = stack.pop()
        if node.type == "import_declaration" and not any(child.type == "static" for child in node.children):
            name_node = next((child for child in node.named_children if child.type in ("identifier", "scoped_identifier")), None)
            if name_node is not None:
                import_name = text(name_node)
                if any(child.type == "asterisk" for child in node.children):
                    import_name += ".*"
                facts.append({"kind": "java_import", "name": import_name, "namespace": package,
                              "range": _source_range(raw, node.start_byte, node.end_byte, newlines),
                              "text": text(node), "quality": "structural"})
        stack.extend(reversed(node.named_children))

    type_fact_by_node = {}
    for node, qualified, simple in type_nodes:
        super_nodes = []
        superclass = node.child_by_field_name("superclass")
        if superclass is not None:
            base = base_type_node(superclass)
            if base is not None:
                super_nodes.append(base)
        interfaces_node = node.child_by_field_name("interfaces") or node.child_by_field_name("super_interfaces")
        if interfaces_node is not None:
            for child in interfaces_node.named_children:
                base = base_type_node(child)
                if base is not None:
                    super_nodes.append(base)
        super_types = []
        hierarchy_certain = True
        for super_node in super_nodes:
            name, certain = resolve_type_node(super_node)
            if name:
                super_types.append(name)
            hierarchy_certain = hierarchy_certain and certain
        name_node = node.child_by_field_name("name")
        range_node = name_node or node
        fact = {"kind": "java_type", "name": simple, "namespace": qualified,
                "owner_kind": "interface" if node.type == "interface_declaration" else "class",
                "super_types": sorted(set(super_types)), "certainty": "certain" if hierarchy_certain else "uncertain",
                "range": _source_range(raw, range_node.start_byte, range_node.end_byte, newlines),
                "text": text(range_node), "quality": "structural"}
        facts.append(fact)
        type_fact_by_node[node.id] = fact

    injection_facts = []
    field_injections = {}
    method_nodes = []
    for node, _, _ in type_nodes:
        body = node.child_by_field_name("body")
        if body is None:
            continue
        for member in body.named_children:
            if member.type == "field_declaration":
                modifiers = next((child for child in member.named_children if child.type == "modifiers"), None)
                annotations = [] if modifiers is None else [child for child in modifiers.named_children if child.type in ("annotation", "marker_annotation")]
                recognized = [annotation for annotation in annotations
                              if (annotation_fqn(annotation, "org.springframework.beans.factory.annotation.") == "org.springframework.beans.factory.annotation.Autowired"
                                  or annotation_fqn(annotation, "javax.annotation.") == "javax.annotation.Resource"
                                  or annotation_fqn(annotation, "jakarta.annotation.") == "jakarta.annotation.Resource"
                                  or annotation_fqn(annotation, "javax.inject.") == "javax.inject.Inject"
                                  or annotation_fqn(annotation, "jakarta.inject.") == "jakarta.inject.Inject")]
                if not recognized:
                    continue
                owner_name = binary_type_name(node)
                type_node = member.child_by_field_name("type")
                type_name, type_certain = resolve_type_node(type_node)
                type_base = base_type_node(type_node)
                target_name = text(type_base) if type_base is not None else ""
                for declarator in member.named_children:
                    if declarator.type != "variable_declarator":
                        continue
                    name_node = declarator.child_by_field_name("name")
                    if name_node is None:
                        continue
                    field_name = text(name_node)
                    fact = {"kind": "java_injection", "name": field_name, "namespace": owner_name,
                            "type_name": type_name, "target_name": target_name, "owner_kind": "field",
                            "certainty": "certain" if type_certain else "uncertain",
                            "dynamic": not type_certain,
                            "range": _source_range(raw, member.start_byte, member.end_byte, newlines),
                            "text": text(member), "quality": "structural"}
                    injection_facts.append(fact)
                    field_injections.setdefault((owner_name, field_name), []).append(fact)
            elif member.type in ("method_declaration", "method_signature", "constructor_declaration"):
                method_nodes.append(member)

    # Mapping, method declaration, injection and call facts are syntax-only.
    # Calls without a single injected receiver type stay explicitly unresolved.
    for node, owner_type, simple in type_nodes:
        owner_name = binary_type_name(node)
        class_constants = constants.get(node.id, {})
        body = node.child_by_field_name("body")
        if body is None:
            continue
        for child in node.named_children:
            if child.type not in ("class_body", "interface_body", "enum_body"):
                continue
            for member in child.named_children:
                if member.type not in ("method_declaration", "method_signature", "constructor_declaration"):
                    continue
                method_node = member.child_by_field_name("name")
                method_name = text(method_node) if method_node is not None else ""
                modifier_node = next((item for item in member.named_children if item.type == "modifiers"), None)
                modifier_text = text(modifier_node) if modifier_node is not None else ""
                annotations = [] if modifier_node is None else [item for item in modifier_node.named_children if item.type in ("annotation", "marker_annotation")]
                if method_name:
                    parameter_types, signature_certain = parameter_signature(member)
                    facts.append({"kind": "java_method", "name": method_name, "namespace": owner_name,
                                  "type_name": resolve_type_node(member.child_by_field_name("type"))[0],
                                  "parameter_types": parameter_types, "signature_certain": signature_certain,
                                  "is_abstract": bool(re.search(r"\babstract\b", modifier_text)),
                                  "is_default": bool(re.search(r"\bdefault\b", modifier_text)),
                                  "owner_kind": member.type, "range": _source_range(raw, member.start_byte, member.end_byte, newlines),
                                  "text": "", "quality": "structural"})
                for annotation in annotations:
                    annotation_node = annotation.child_by_field_name("name")
                    annotation_name = text(annotation_node) if annotation_node is not None else ""
                    simple_annotation = annotation_name.rsplit(".", 1)[-1]
                    canonical = annotation_fqn(annotation, SPRING_MAPPING_PREFIX)
                    if simple_annotation not in set(SPRING_MAPPING_METHODS) | {"RequestMapping"}:
                        continue
                    paths, methods, paths_certain, methods_certain, method_declared = mapping_values(annotation, node)
                    known = canonical == SPRING_MAPPING_PREFIX + simple_annotation
                    if not known:
                        diagnostics.append({"code": "spring_mapping_identity_unresolved",
                                            "message": "Spring mapping annotation is not tied to a unique framework import",
                                            "range": _source_range(raw, annotation.start_byte, annotation.end_byte, newlines)})
                    for route in paths:
                        facts.append({"kind": "spring_mapping", "name": method_name, "namespace": owner_name,
                                      "route_path": route if paths_certain else "", "statement_type": "method" if method_name else "type",
                                      "owner_kind": "method" if method_name else "type", "owner_name": method_name,
                                      "http_method": methods[0] if len(methods) == 1 and methods_certain else "",
                                      "http_methods": methods if method_declared else [],
                                      "http_methods_specified": method_declared,
                                      "http_methods_certain": methods_certain,
                                      "dynamic": not (paths_certain and methods_certain and known),
                                      "certainty": "certain" if paths_certain and methods_certain and known else "uncertain",
                                      "range": _source_range(raw, annotation.start_byte, annotation.end_byte, newlines),
                                      "text": text(annotation), "quality": "structural"})
        # Type-level mappings use the annotation on the class declaration.
        modifiers = next((item for item in node.named_children if item.type == "modifiers"), None)
        annotations = [] if modifiers is None else [item for item in modifiers.named_children if item.type in ("annotation", "marker_annotation")]
        for annotation in annotations:
            annotation_node = annotation.child_by_field_name("name")
            annotation_name = text(annotation_node) if annotation_node is not None else ""
            simple_annotation = annotation_name.rsplit(".", 1)[-1]
            if simple_annotation not in set(SPRING_MAPPING_METHODS) | {"RequestMapping"}:
                continue
            canonical = annotation_fqn(annotation, SPRING_MAPPING_PREFIX)
            paths, methods, paths_certain, methods_certain, method_declared = mapping_values(annotation, node)
            known = canonical == SPRING_MAPPING_PREFIX + simple_annotation
            if not known:
                diagnostics.append({"code": "spring_mapping_identity_unresolved",
                                    "message": "Spring mapping annotation is not tied to a unique framework import",
                                    "range": _source_range(raw, annotation.start_byte, annotation.end_byte, newlines)})
            for route in paths:
                facts.append({"kind": "spring_mapping", "name": simple, "namespace": owner_name,
                              "route_path": route if paths_certain else "", "statement_type": "type", "owner_kind": "type",
                              "http_method": methods[0] if len(methods) == 1 and methods_certain else "",
                              "http_methods": methods if method_declared else [],
                              "http_methods_specified": method_declared, "http_methods_certain": methods_certain,
                              "dynamic": not (paths_certain and methods_certain and known),
                              "certainty": "certain" if paths_certain and methods_certain and known else "uncertain",
                              "range": _source_range(raw, annotation.start_byte, annotation.end_byte, newlines),
                              "text": text(annotation), "quality": "structural"})

    facts.extend(injection_facts)
    method_index = {}
    for fact in facts:
        if fact.get("kind") == "java_method":
            method_index.setdefault((fact.get("namespace", ""), fact.get("name", "")), []).append(fact)
    for method in method_nodes:
        owner_type = enclosing_java_type(method)
        if owner_type is None:
            continue
        owner_name = binary_type_name(owner_type)
        method_name_node = method.child_by_field_name("name")
        caller_name = text(method_name_node) if method_name_node is not None else ""
        method_parameters = method.child_by_field_name("parameters")
        shadowed_names = set()
        local_stack = [method_parameters] if method_parameters is not None else []
        local_stack.append(method.child_by_field_name("body"))
        while local_stack:
            current = local_stack.pop()
            if current is None:
                continue
            if current.type in ("formal_parameter", "spread_parameter", "catch_formal_parameter"):
                name_node = current.child_by_field_name("name")
                if name_node is not None:
                    shadowed_names.add(text(name_node))
            elif current.type == "variable_declarator":
                name_node = current.child_by_field_name("name")
                if name_node is not None:
                    shadowed_names.add(text(name_node))
            local_stack.extend(current.named_children)
        stack = [method]
        while stack:
            call = stack.pop()
            if call.type == "method_invocation":
                call_name_node = call.child_by_field_name("name")
                object_node = call.child_by_field_name("object")
                call_name = text(call_name_node) if call_name_node is not None else ""
                receiver = text(object_node) if object_node is not None else ""
                receiver_field = ""
                if object_node is None:
                    receiver_field = ""
                elif object_node.type in ("identifier", "field_identifier"):
                    receiver_field = receiver
                elif object_node.type == "field_access" and text(object_node).replace(" ", "").startswith("this."):
                    candidate = text(object_node).replace(" ", "").split(".")[-1]
                    if candidate.isidentifier():
                        receiver_field = candidate
                binding = field_injections.get((owner_name, receiver_field), []) if receiver_field and receiver_field not in shadowed_names else []
                type_name = binding[0].get("type_name", "") if len(binding) == 1 and binding[0].get("certainty") == "certain" else ""
                target_name = binding[0].get("target_name", "") if len(binding) == 1 else ""
                receiver_certain = bool(type_name)
                call_start = call.start_byte
                call_end = call.end_byte
                dynamic = not receiver_certain
                facts.append({"kind": "java_method_call", "name": call_name, "namespace": owner_name,
                              "method_name": caller_name, "receiver": receiver, "type_name": type_name,
                              "target_name": target_name,
                              "owner_name": caller_name, "dynamic": dynamic,
                              "certainty": "certain" if receiver_certain else "uncertain",
                              "range": _source_range(raw, call_start, call_end, newlines),
                              "text": text(call), "quality": "structural"})
                if call_name in {"forName", "getMethod", "getDeclaredMethod", "invoke", "newInstance"}:
                    facts.append({"kind": "java_dynamic_dispatch", "name": call_name, "namespace": owner_name,
                                  "method_name": caller_name, "dynamic": True, "certainty": "uncertain",
                                  "range": _source_range(raw, call_start, call_end, newlines),
                                  "quality": "structural"})
            stack.extend(reversed(call.named_children))

    facts.sort(key=lambda fact: (fact["range"]["start_byte"], fact["range"]["end_byte"], fact["kind"], fact.get("name", "")))

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
    unsafe_document = False

    def start_doctype(name, system_id, public_id, has_internal_subset):
        nonlocal unsafe_document
        if (name != "mapper" or public_id != MAPPER_PUBLIC_ID or system_id != MAPPER_SYSTEM_ID
                or has_internal_subset):
            unsafe_document = True
            raise UnsafeMyBatisXML("unsupported or unsafe XML document type")

    def reject_external(*_args):
        nonlocal unsafe_document
        unsafe_document = True
        raise UnsafeMyBatisXML("external XML entities are not allowed")

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
    except UnsafeMyBatisXML:
        raise ValueError("invalid or unsafe MyBatis XML") from None
    except expat.ExpatError:
        if unsafe_document:
            raise ValueError("invalid or unsafe MyBatis XML") from None
        return {"quality": "text_fallback", "facts": [], "diagnostics": [{
            "code": "mybatis_xml_syntax_fallback",
            "message": "MyBatis XML is malformed; source is retained as text",
            "range": _xml_error_range(raw, parser.ErrorByteIndex, newlines),
        }]}
    except (ValueError, IndexError):
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
