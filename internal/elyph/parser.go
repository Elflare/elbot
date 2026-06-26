package elyph

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type blockKind string

const (
	blockIf   blockKind = "if"
	blockElse blockKind = "else"
	blockEach blockKind = "each"
)

type parser struct {
	expectedKind string
	expectedName string
	doc          Document
	diagnostics  []Diagnostic
	seenHeader   bool
	blocks       []blockKind
	lastClosed   blockKind
}

// ParseSkill 解析并校验 ELyph skill 文档的基础结构；不执行或理解自然语言条件。
func ParseSkill(raw string, expectedName string) (Document, error) {
	return parseKind(raw, "skill", expectedName)
}

// ParseTask 解析并校验 ELyph task 文档的基础结构；用于 LLM cron 等任务纸条。
func ParseTask(raw string, expectedName string) (Document, error) {
	return parseKind(raw, "task", expectedName)
}

func parseKind(raw string, expectedKind string, expectedName string) (Document, error) {
	doc, diagnostics := parseDocument(raw, expectedKind, expectedName)
	if len(diagnostics) > 0 {
		return Document{}, diagnosticsError(diagnostics)
	}
	return doc, nil
}

func parseDocument(raw string, expectedKind string, expectedName string) (Document, []Diagnostic) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return Document{}, []Diagnostic{{Message: "elyph is required"}}
	}
	p := &parser{expectedKind: strings.TrimSpace(expectedKind), expectedName: strings.TrimSpace(expectedName)}
	for idx, line := range strings.Split(text, "\n") {
		p.parseLine(strings.TrimSpace(line), idx+1)
	}
	p.finish()
	return p.doc, p.diagnostics
}

func (p *parser) parseLine(trimmed string, lineNo int) {
	if trimmed == "" || strings.HasPrefix(trimmed, "//") {
		return
	}
	if !p.seenHeader {
		p.parseHeader(trimmed, lineNo)
		return
	}
	if strings.HasPrefix(trimmed, "#") {
		p.add(lineNo, "header must appear only once")
		return
	}
	if trimmed == "}" {
		p.closeBlock(lineNo)
		return
	}
	if !strings.HasPrefix(trimmed, "?else") {
		p.lastClosed = ""
	}
	switch {
	case strings.HasPrefix(trimmed, "<-"):
		p.parseIO(trimmed, lineNo, true)
	case strings.HasPrefix(trimmed, "->"):
		p.parseIO(trimmed, lineNo, false)
	case strings.HasPrefix(trimmed, "=>"):
		p.parseDerive(trimmed, lineNo)
	case strings.HasPrefix(trimmed, "$"):
		p.parseAssignment(trimmed, lineNo)
	case strings.HasPrefix(trimmed, "?if"):
		p.parseIf(trimmed, lineNo)
	case strings.HasPrefix(trimmed, "?else"):
		p.parseElse(trimmed, lineNo)
	case strings.HasPrefix(trimmed, "? else") || strings.HasPrefix(trimmed, "else"):
		p.add(lineNo, "else must be ?else{")
	case strings.HasPrefix(trimmed, "each"):
		p.parseEach(trimmed, lineNo)
	case strings.HasPrefix(trimmed, ">"):
		p.parseOutput(trimmed, lineNo)
	case strings.HasPrefix(trimmed, "@tool"):
		p.parseCall(trimmed, lineNo, "@tool")
	case strings.HasPrefix(trimmed, "@skill"):
		p.parseCall(trimmed, lineNo, "@skill")
	case strings.HasPrefix(trimmed, "**"):
		p.parseTextSlot(trimmed, lineNo, "**")
	case strings.HasPrefix(trimmed, "~"):
		p.parseTextSlot(trimmed, lineNo, "~")
	default:
		p.add(lineNo, "line must start with a valid ELyph token")
	}
}

func (p *parser) parseHeader(line string, lineNo int) {
	fields := strings.Fields(line)
	if len(fields) < 2 || (fields[0] != "#skill" && fields[0] != "#task") || !validName(fields[1]) {
		p.add(lineNo, fmt.Sprintf("first statement must be #%s <name>", p.expectedKind))
		return
	}
	if len(fields) > 2 {
		rest := strings.TrimSpace(strings.TrimPrefix(line, fields[0]+" "+fields[1]))
		if !strings.HasPrefix(rest, "- ") || strings.TrimSpace(strings.TrimPrefix(rest, "- ")) == "" {
			p.add(lineNo, fmt.Sprintf("first statement must be #%s <name>", p.expectedKind))
			return
		}
	}
	p.doc.Kind = strings.TrimPrefix(fields[0], "#")
	p.doc.Name = fields[1]
	p.seenHeader = true
}

func (p *parser) parseIO(line string, lineNo int, input bool) {
	prefix := "<-"
	message := "input must be <- $name:type, <- $name:type! or <- $name:type?"
	if !input {
		prefix = "->"
		message = "output must be -> $name:type"
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	name, typ, marker, ok := parseTypedVariable(rest)
	if !ok || name == "" || typ == "" || (!input && marker != "") {
		p.add(lineNo, message)
	}
}

func (p *parser) parseAssignment(line string, lineNo int) {
	name, _, _, rhs, ok := parseAssignmentParts(line)
	if !ok || strings.TrimSpace(rhs) == "" {
		p.add(lineNo, "assignment must be $name[:type] = value")
		return
	}
	p.checkReservedVariable(name, lineNo)
}

func (p *parser) parseDerive(line string, lineNo int) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "=>"))
	name, _, _, rhs, ok := parseAssignmentParts(rest)
	if !ok || strings.TrimSpace(rhs) == "" {
		p.add(lineNo, "derive must be => $name[:type] = expression")
		return
	}
	p.checkReservedVariable(name, lineNo)
	p.checkDeriveExpression(rhs, lineNo)
}

func (p *parser) parseIf(line string, lineNo int) {
	if !strings.HasSuffix(line, "{") {
		p.add(lineNo, "?if must be ?if(condition) {")
		return
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "?if"), "{"))
	if !strings.HasPrefix(body, "(") || !strings.HasSuffix(body, ")") || strings.TrimSpace(body[1:len(body)-1]) == "" || strings.ContainsAny(body[1:len(body)-1], "{}") {
		p.add(lineNo, "?if must be ?if(condition) {")
		p.openBlock(blockIf)
		return
	}
	p.openBlock(blockIf)
}

func (p *parser) parseElse(line string, lineNo int) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "?else"))
	if rest != "{" {
		p.add(lineNo, "?else must be ?else{")
		return
	}
	if p.lastClosed != blockIf {
		p.add(lineNo, "?else must immediately follow a closed ?if block")
		return
	}
	p.openBlock(blockElse)
}

func (p *parser) parseEach(line string, lineNo int) {
	if !strings.HasPrefix(line, "each(") || !strings.HasSuffix(line, "{") {
		p.add(lineNo, "each must be each($item in $items, limit=N) {")
		if strings.HasSuffix(line, "{") {
			p.openBlock(blockEach)
		}
		return
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "each("), "{"))
	if !strings.HasSuffix(body, ")") {
		p.add(lineNo, "each must be each($item in $items, limit=N) {")
		p.openBlock(blockEach)
		return
	}
	body = strings.TrimSpace(strings.TrimSuffix(body, ")"))
	parts := splitTopLevel(body, ',')
	if len(parts) != 2 {
		p.add(lineNo, "each must be each($item in $items, limit=N) {")
		p.openBlock(blockEach)
		return
	}
	loop := strings.Fields(parts[0])
	if len(loop) != 3 || !validVariable(loop[0]) || loop[1] != "in" || !validVariable(loop[2]) {
		p.add(lineNo, "each must be each($item in $items, limit=N) {")
		p.openBlock(blockEach)
		return
	}
	limitKey, limitValue, ok := strings.Cut(strings.TrimSpace(parts[1]), "=")
	if !ok || strings.TrimSpace(limitKey) != "limit" {
		p.add(lineNo, "each must be each($item in $items, limit=N) {")
		p.openBlock(blockEach)
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(limitValue))
	if err != nil {
		p.add(lineNo, "each must be each($item in $items, limit=N) {")
		p.openBlock(blockEach)
		return
	}
	if n <= 0 {
		p.add(lineNo, "each limit must be positive")
		return
	}
	p.openBlock(blockEach)
}

func (p *parser) parseOutput(line string, lineNo int) {
	if line != ">" && !strings.HasPrefix(line, "> ") {
		p.add(lineNo, "output must be > text")
	}
}

func (p *parser) parseCall(line string, lineNo int, prefix string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	open := strings.Index(rest, "(")
	if open <= 0 || !strings.HasSuffix(rest, ")") {
		p.add(lineNo, "call must be @tool/@skill name(k=v)")
		return
	}
	name := strings.TrimSpace(rest[:open])
	args := strings.TrimSpace(rest[open+1 : len(rest)-1])
	if !validName(name) || strings.ContainsAny(args, "()") || !validCallArgs(args) {
		p.add(lineNo, "call must be @tool/@skill name(k=v)")
	}
}

func (p *parser) parseTextSlot(line string, lineNo int, prefix string) {
	if strings.TrimSpace(strings.TrimPrefix(line, prefix)) == "" {
		p.add(lineNo, prefix+" must have text")
	}
}

func (p *parser) closeBlock(lineNo int) {
	if len(p.blocks) == 0 {
		p.add(lineNo, "unexpected }")
		p.lastClosed = ""
		return
	}
	p.lastClosed = p.blocks[len(p.blocks)-1]
	p.blocks = p.blocks[:len(p.blocks)-1]
}

func (p *parser) openBlock(kind blockKind) {
	p.blocks = append(p.blocks, kind)
	p.lastClosed = ""
}

func (p *parser) finish() {
	if !p.seenHeader {
		p.add(0, fmt.Sprintf("missing #%s <name>", p.expectedKind))
	}
	if p.expectedKind != "" && p.doc.Kind != "" && p.doc.Kind != p.expectedKind {
		p.add(0, fmt.Sprintf("document kind %q does not match %q", p.doc.Kind, p.expectedKind))
	}
	if p.expectedName != "" && p.doc.Name != "" && p.doc.Name != p.expectedName {
		p.add(0, fmt.Sprintf("#%s name %q does not match %q", p.doc.Kind, p.doc.Name, p.expectedName))
	}
	if len(p.blocks) != 0 {
		p.add(0, "unclosed { block")
	}
}

func (p *parser) checkReservedVariable(name string, lineNo int) {
	if name == "$user" || name == "$assistant" {
		p.add(lineNo, fmt.Sprintf("reserved variable %s cannot be redefined", name))
	}
}

func (p *parser) checkDeriveExpression(expr string, lineNo int) {
	question := strings.Index(expr, "?")
	if question < 0 {
		return
	}
	colonOffset := strings.Index(expr[question+1:], ":")
	if colonOffset < 0 {
		p.add(lineNo, "ternary must be condition ? true : false")
		return
	}
	colon := question + 1 + colonOffset
	if strings.TrimSpace(expr[:question]) == "" || strings.TrimSpace(expr[question+1:colon]) == "" || strings.TrimSpace(expr[colon+1:]) == "" {
		p.add(lineNo, "ternary must be condition ? true : false")
	}
}

func (p *parser) add(line int, message string) {
	p.diagnostics = append(p.diagnostics, Diagnostic{Line: line, Message: message})
}

func parseAssignmentParts(text string) (name string, typ string, marker string, rhs string, ok bool) {
	left, right, ok := strings.Cut(text, "=")
	if !ok {
		return "", "", "", "", false
	}
	name, typ, marker, ok = parseVariableRef(strings.TrimSpace(left))
	if !ok || marker != "" {
		return "", "", "", "", false
	}
	return name, typ, marker, strings.TrimSpace(right), true
}

func parseVariableRef(text string) (name string, typ string, marker string, ok bool) {
	if strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") {
		marker = text[len(text)-1:]
		text = strings.TrimSpace(text[:len(text)-1])
	}
	if left, right, hasType := strings.Cut(text, ":"); hasType {
		name = strings.TrimSpace(left)
		typ = strings.TrimSpace(right)
		if !validVariable(name) || !validType(typ) {
			return "", "", "", false
		}
		return name, typ, marker, true
	}
	name = strings.TrimSpace(text)
	if !validVariable(name) {
		return "", "", "", false
	}
	return name, "", marker, true
}

func parseTypedVariable(text string) (name string, typ string, marker string, ok bool) {
	if strings.HasSuffix(text, "!") || strings.HasSuffix(text, "?") {
		marker = text[len(text)-1:]
		text = strings.TrimSpace(text[:len(text)-1])
	}
	name, typ, ok = strings.Cut(text, ":")
	if !ok {
		return "", "", "", false
	}
	name = strings.TrimSpace(name)
	typ = strings.TrimSpace(typ)
	if !validVariable(name) || !validType(typ) {
		return "", "", "", false
	}
	return name, typ, marker, true
}

func validCallArgs(args string) bool {
	if args == "" {
		return true
	}
	parts := splitTopLevel(args, ',')
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok || !validIdent(strings.TrimSpace(key)) || strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func splitTopLevel(text string, sep rune) []string {
	parts := []string{}
	start := 0
	for idx, r := range text {
		if r == sep {
			parts = append(parts, text[start:idx])
			start = idx + len(string(r))
		}
	}
	parts = append(parts, text[start:])
	return parts
}

func validVariable(value string) bool {
	return strings.HasPrefix(value, "$") && validIdent(strings.TrimPrefix(value, "$"))
}

func validName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for idx, r := range value {
		if idx == 0 {
			if r < 'a' || r > 'z' {
				return false
			}
			continue
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func validType(value string) bool {
	return validIdent(value)
}

func validIdent(value string) bool {
	if value == "" {
		return false
	}
	for idx, r := range value {
		if idx == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func diagnosticsError(diagnostics []Diagnostic) error {
	parts := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if diagnostic.Line > 0 {
			parts = append(parts, fmt.Sprintf("line %d: %s", diagnostic.Line, diagnostic.Message))
		} else {
			parts = append(parts, diagnostic.Message)
		}
	}
	return fmt.Errorf("invalid ELyph:\n- %s", strings.Join(parts, "\n- "))
}
