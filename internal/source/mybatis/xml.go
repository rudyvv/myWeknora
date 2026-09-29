package mybatis

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

var entityPattern = regexp.MustCompile(`(?is)<!ENTITY`)
var doctypePattern = regexp.MustCompile(`(?is)<!DOCTYPE\s+[^>]*>`)
var safeMyBatisDoctype = regexp.MustCompile(`(?is)^<!DOCTYPE\s+mapper\s+PUBLIC\s+["']-//mybatis\.org//DTD Mapper 3\.0//EN["']\s+["']https?://mybatis\.org/dtd/mybatis-3-mapper\.dtd["']\s*>$`)

var dynamicElements = map[string]bool{
	"bind": true, "choose": true, "foreach": true, "if": true,
	"otherwise": true, "set": true, "trim": true, "when": true,
	"where": true,
}

type xmlNode struct {
	name         string
	start        int
	contentStart int
	endStart     int
	end          int
	attrs        map[string]string
	parent       *xmlNode
	children     []*xmlNode
	dynamic      bool
}

// IsMapperPath is deliberately conservative. XML becomes a MyBatis document
// only when its filename is a mapper XML or its contents have a mapper root;
// arbitrary XML remains a normal text file for later source routes.
func IsMapperPath(path string, raw []byte) bool {
	if !strings.EqualFold(filepath.Ext(path), ".xml") {
		return false
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}))
	return bytes.Contains(bytes.ToLower(trimmed), []byte("<mapper"))
}

func ParseXML(path string, raw []byte) (XMLDocument, error) {
	if path == "" {
		return XMLDocument{}, fmt.Errorf("mapper XML path is required")
	}
	if entityPattern.Match(raw) {
		return XMLDocument{}, fmt.Errorf("mapper XML external entities are not allowed")
	}
	for _, declaration := range doctypePattern.FindAll(raw, -1) {
		upper := strings.ToUpper(string(declaration))
		if strings.Contains(upper, "SYSTEM") || strings.Contains(upper, "PUBLIC") {
			if !safeMyBatisDoctype.Match(declaration) {
				return XMLDocument{}, fmt.Errorf("mapper XML external doctype is not allowed")
			}
			// The standard MyBatis declaration is treated as inert text by
			// encoding/xml; it is never dereferenced and cannot enable a
			// network fetch. Entity declarations are rejected above.
		}
	}
	doc := XMLDocument{Path: path, Quality: "structural"}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = true
	var stack []*xmlNode
	var root *xmlNode
	previousEnd := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return XMLDocument{}, fmt.Errorf("parse mapper XML: %w", err)
		}
		offset := int(decoder.InputOffset())
		switch value := token.(type) {
		case xml.StartElement:
			start := tokenStart(raw, previousEnd, offset, []byte("<"))
			attrs := make(map[string]string, len(value.Attr))
			for _, attr := range value.Attr {
				attrs[attr.Name.Local] = attr.Value
			}
			current := &xmlNode{name: value.Name.Local, start: start, contentStart: offset, attrs: attrs}
			if len(stack) > 0 {
				current.parent = stack[len(stack)-1]
				current.parent.children = append(current.parent.children, current)
				if dynamicElements[strings.ToLower(current.name)] {
					for parent := current.parent; parent != nil; parent = parent.parent {
						parent.dynamic = true
					}
				}
			} else if root == nil {
				root = current
			}
			stack = append(stack, current)
		case xml.EndElement:
			if len(stack) == 0 {
				return XMLDocument{}, fmt.Errorf("unexpected mapper XML closing element %q", value.Name.Local)
			}
			current := stack[len(stack)-1]
			if current.name != value.Name.Local {
				return XMLDocument{}, fmt.Errorf("mapper XML closing element %q does not match %q", value.Name.Local, current.name)
			}
			current.end = offset
			current.endStart = tokenStart(raw, current.contentStart, offset, []byte("</"))
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if bytes.Contains(value, []byte("${")) || bytes.Contains(value, []byte("#{")) {
				for _, parent := range stack {
					parent.dynamic = true
				}
			}
		}
		previousEnd = offset
	}
	if root == nil || len(stack) != 0 || !strings.EqualFold(root.name, "mapper") {
		return XMLDocument{}, fmt.Errorf("mapper XML must have one complete mapper root")
	}
	doc.Range = sourceRange(raw, root.start, root.end)
	doc.Namespace = root.attrs["namespace"]
	if doc.Namespace == "" {
		doc.Quality = "uncertain"
		doc.Warnings = append(doc.Warnings, "mapper namespace is missing")
	}

	var walk func(*xmlNode)
	walk = func(current *xmlNode) {
		name := strings.ToLower(current.name)
		switch name {
		case "resultmap":
			id := current.attrs["id"]
			if id == "" {
				doc.Warnings = append(doc.Warnings, "resultMap without id")
			} else {
				doc.ResultMaps = append(doc.ResultMaps, ResultMap{ID: id, Range: sourceRange(raw, current.start, current.end)})
			}
		case "sql":
			if id := current.attrs["id"]; id != "" {
				doc.Includes = append(doc.Includes, Include{ID: id, Range: sourceRange(raw, current.start, current.end)})
			}
		case "select", "insert", "update", "delete":
			contentStart := current.contentStart
			contentEnd := current.endStart
			if contentEnd < contentStart {
				contentEnd = contentStart
			}
			statement := Statement{
				ID: current.attrs["id"], Kind: name, Range: sourceRange(raw, current.start, current.end),
				SQLRange: sourceRange(raw, contentStart, contentEnd), SQL: string(raw[contentStart:contentEnd]),
				Dynamic: current.dynamic, Quality: "structural",
			}
			if statement.ID == "" {
				statement.Quality = "uncertain"
				doc.Warnings = append(doc.Warnings, fmt.Sprintf("%s without statement id", name))
			}
			statement.ResultMapRefs = splitRefs(current.attrs["resultMap"])
			statement.IncludeRefs = includeRefs(current)
			statement.Tables = mergeTableRanges(raw, contentStart, string(raw[contentStart:contentEnd]), extractTables(raw[contentStart:contentEnd], statement.Dynamic))
			statement.Context = contextFor(raw, current, doc.Namespace)
			doc.Statements = append(doc.Statements, statement)
		}
		for _, child := range current.children {
			walk(child)
		}
	}
	walk(root)
	checkDuplicateXMLKeys(&doc)
	return doc, nil
}

func tokenStart(raw []byte, from, offset int, marker []byte) int {
	if from < 0 {
		from = 0
	}
	if offset > len(raw) {
		offset = len(raw)
	}
	if from > offset {
		from = offset
	}
	if index := bytes.LastIndex(raw[from:offset], marker); index >= 0 {
		return from + index
	}
	return from
}

func sourceRange(raw []byte, start, end int) types.SourceRange {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if start > len(raw) {
		start = len(raw)
	}
	if end > len(raw) {
		end = len(raw)
	}
	line := func(offset int) int { return 1 + bytes.Count(raw[:offset], []byte{'\n'}) }
	last := end - 1
	if last < start {
		last = start
	}
	return types.SourceRange{StartByte: start, EndByte: end, StartLine: line(start), EndLine: line(last)}
}

func splitRefs(value string) []string {
	var refs []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			refs = append(refs, item)
		}
	}
	return refs
}

func includeRefs(current *xmlNode) []string {
	refs := []string{}
	var walk func(*xmlNode)
	walk = func(node *xmlNode) {
		if strings.EqualFold(node.name, "include") {
			if ref := node.attrs["refid"]; ref != "" {
				refs = append(refs, ref)
			}
		}
		for _, child := range node.children {
			walk(child)
		}
	}
	for _, child := range current.children {
		walk(child)
	}
	return refs
}

func contextFor(raw []byte, current *xmlNode, namespace string) []types.SourceContext {
	text := strings.TrimSpace(string(raw[current.start:current.contentStart]))
	if text == "" && namespace == "" {
		return nil
	}
	return []types.SourceContext{{Text: text, Range: sourceRange(raw, current.start, current.contentStart)}}
}

func checkDuplicateXMLKeys(doc *XMLDocument) {
	counts := map[string]int{}
	for _, statement := range doc.Statements {
		counts[statement.ID]++
	}
	for id, count := range counts {
		if id != "" && count > 1 {
			doc.Quality = "uncertain"
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("duplicate statement id %q (%d occurrences)", id, count))
		}
	}
	resultMapCounts := map[string]int{}
	for _, item := range doc.ResultMaps {
		resultMapCounts[item.ID]++
	}
	for id, count := range resultMapCounts {
		if id != "" && count > 1 {
			doc.Quality = "uncertain"
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("duplicate resultMap id %q (%d occurrences)", id, count))
		}
	}
	includeCounts := map[string]int{}
	for _, item := range doc.Includes {
		includeCounts[item.ID]++
	}
	for id, count := range includeCounts {
		if id != "" && count > 1 {
			doc.Quality = "uncertain"
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("duplicate sql include id %q (%d occurrences)", id, count))
		}
	}
	for _, warning := range doc.Warnings {
		if strings.Contains(warning, "without") || strings.Contains(warning, "duplicate") {
			doc.Quality = "uncertain"
		}
	}
	sort.Strings(doc.Warnings)
}
