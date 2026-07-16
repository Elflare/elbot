package builtin

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"

	"elbot/internal/tool"
	"elbot/internal/utils/fileops"
	"mvdan.cc/sh/v3/syntax"
)

const (
	readFileModeRead        = "read"
	readFileModeGrep        = "grep"
	readFileModeAST         = "ast"
	readFileModeASTFunction = "ast_function"
)

type astMatch struct {
	Line      int
	Column    int
	Kind      string
	Container string
}

func normalizeReadFileMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		return readFileModeRead, nil
	}
	switch mode {
	case readFileModeRead, readFileModeGrep, readFileModeAST, readFileModeASTFunction:
		return mode, nil
	default:
		return "", fmt.Errorf("read_file mode must be read, grep, ast, or ast_function")
	}
}

func readFileASTResult(file fileops.File, query string, contextLines, maxMatches, index int, warnings []string) (*tool.Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required when mode is ast")
	}
	language, variant, err := detectASTLanguage(file.Path, file.Text)
	if err != nil {
		return nil, err
	}
	var matches []astMatch
	var parseWarning string
	if language == "go" {
		matches, parseWarning, err = findGoASTMatches(file.Path, file.Text, query)
	} else {
		matches, parseWarning, err = findShellASTMatches(file.Text, query, variant)
	}
	if err != nil {
		return nil, err
	}
	if parseWarning != "" {
		warnings = append(warnings, parseWarning)
	}
	return formatASTMatches(file, query, language, matches, contextLines, maxMatches, index, warnings), nil
}

type astFunctionMatch struct {
	StartLine int
	EndLine   int
	Kind      string
	Name      string
}

func readFileASTFunctionResult(file fileops.File, query string, maxMatches, index int, warnings []string) (*tool.Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query is required when mode is ast_function")
	}
	language, variant, err := detectASTLanguage(file.Path, file.Text)
	if err != nil {
		return nil, err
	}
	var matches []astFunctionMatch
	var parseWarning string
	if language == "go" {
		matches, parseWarning, err = findGoASTFunctions(file.Path, file.Text, query)
	} else {
		matches, parseWarning, err = findShellASTFunctions(file.Text, query, variant)
	}
	if err != nil {
		return nil, err
	}
	if parseWarning != "" {
		warnings = append(warnings, parseWarning)
	}
	return formatASTFunctionMatches(file, query, language, matches, maxMatches, index, warnings), nil
}

func findGoASTFunctions(path, source, query string) ([]astFunctionMatch, string, error) {
	if strings.TrimSpace(source) == "" {
		return nil, "", nil
	}
	fset := token.NewFileSet()
	file, parseErr := parser.ParseFile(fset, path, source, parser.AllErrors)
	if file == nil {
		return nil, "", fmt.Errorf("parse Go source: %w", parseErr)
	}
	matches := make([]astFunctionMatch, 0)
	for _, decl := range file.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok || function.Name == nil || function.Name.Name != query {
			continue
		}
		start := fset.Position(function.Pos()).Line
		end := fset.Position(function.End()).Line
		kind, name := "function", function.Name.Name
		if function.Recv != nil {
			kind, name = "method", goASTFunctionName(fset, function)
		}
		matches = append(matches, astFunctionMatch{StartLine: start, EndLine: end, Kind: kind, Name: name})
	}
	if parseErr != nil {
		return matches, "Go AST 解析存在错误，结果可能不完整：" + parseErr.Error(), nil
	}
	return matches, "", nil
}

func goASTFunctionName(fset *token.FileSet, function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return function.Name.Name
	}
	var receiver bytes.Buffer
	if err := format.Node(&receiver, fset, function.Recv.List[0].Type); err != nil {
		return function.Name.Name
	}
	return "(" + receiver.String() + ")." + function.Name.Name
}

func findShellASTFunctions(source, query string, variant syntax.LangVariant) ([]astFunctionMatch, string, error) {
	parser := syntax.NewParser(syntax.Variant(variant))
	file, parseErr := parser.Parse(strings.NewReader(source), "")
	warning := ""
	if parseErr != nil {
		file, parseErr = syntax.NewParser(syntax.Variant(variant), syntax.RecoverErrors(10)).Parse(strings.NewReader(source), "")
		if parseErr != nil || file == nil {
			return nil, "", fmt.Errorf("parse Shell source: %w", parseErr)
		}
		warning = "Shell AST 解析已恢复错误，结果可能不完整。"
	}
	matches := make([]astFunctionMatch, 0)
	syntax.Walk(file, func(node syntax.Node) bool {
		function, ok := node.(*syntax.FuncDecl)
		if !ok || function.Name == nil || function.Name.Value != query {
			return true
		}
		matches = append(matches, astFunctionMatch{StartLine: int(function.Pos().Line()), EndLine: int(function.End().Line()), Kind: "function", Name: function.Name.Value})
		return true
	})
	return matches, warning, nil
}

func detectASTLanguage(path, text string) (string, syntax.LangVariant, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".go" {
		return "go", 0, nil
	}
	firstLine := strings.ToLower(strings.TrimSpace(strings.SplitN(text, "\n", 2)[0]))
	if ext == ".mksh" || strings.Contains(firstLine, "mksh") {
		return "shell", syntax.LangMirBSDKorn, nil
	}
	if ext == ".sh" || ext == ".bash" || ext == ".bats" || strings.HasPrefix(firstLine, "#!") && (strings.Contains(firstLine, "bash") || strings.Contains(firstLine, "/sh") || strings.Contains(firstLine, " dash") || strings.Contains(firstLine, " ash")) {
		return "shell", syntax.LangBash, nil
	}
	return "", 0, fmt.Errorf("AST search only supports Go and Shell files")
}

func findGoASTMatches(path, source, query string) ([]astMatch, string, error) {
	if strings.TrimSpace(source) == "" {
		return nil, "", nil
	}
	fset := token.NewFileSet()
	file, parseErr := parser.ParseFile(fset, path, source, parser.AllErrors)
	if file == nil {
		return nil, "", fmt.Errorf("parse Go source: %w", parseErr)
	}
	matches := []astMatch{}
	stack := []ast.Node{}
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, node)
		ident, ok := node.(*ast.Ident)
		if !ok || ident.Name != query {
			return true
		}
		position := fset.Position(ident.Pos())
		matches = append(matches, astMatch{Line: position.Line, Column: position.Column, Kind: "identifier", Container: goASTContainer(stack)})
		return true
	})
	if parseErr != nil {
		return matches, "Go AST 解析存在错误，结果可能不完整：" + parseErr.Error(), nil
	}
	return matches, "", nil
}

func goASTContainer(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		switch node := stack[i].(type) {
		case *ast.FuncDecl:
			return "function " + node.Name.Name
		case *ast.TypeSpec:
			return "type " + node.Name.Name
		}
	}
	return "file"
}

func findShellASTMatches(source, query string, variant syntax.LangVariant) ([]astMatch, string, error) {
	parser := syntax.NewParser(syntax.Variant(variant))
	file, parseErr := parser.Parse(strings.NewReader(source), "")
	warning := ""
	if parseErr != nil {
		file, parseErr = syntax.NewParser(syntax.Variant(variant), syntax.RecoverErrors(10)).Parse(strings.NewReader(source), "")
		if parseErr != nil || file == nil {
			return nil, "", fmt.Errorf("parse Shell source: %w", parseErr)
		}
		warning = "Shell AST 解析已恢复错误，结果可能不完整。"
	}
	matches := []astMatch{}
	stack := []syntax.Node{}
	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, node)
		lit, ok := node.(*syntax.Lit)
		if !ok || lit.Value != query || shellQuotedLiteral(stack, lit) {
			return true
		}
		kind := "word"
		if shellParameterLiteral(stack, lit) {
			kind = "parameter"
		}
		matches = append(matches, astMatch{Line: int(lit.Pos().Line()), Column: int(lit.Pos().Col()), Kind: kind, Container: shellASTContainer(stack)})
		return true
	})
	return matches, warning, nil
}

func shellQuotedLiteral(stack []syntax.Node, lit *syntax.Lit) bool {
	if shellParameterLiteral(stack, lit) {
		return false
	}
	for _, node := range stack {
		if _, ok := node.(*syntax.DblQuoted); ok {
			return true
		}
	}
	return false
}

func shellParameterLiteral(stack []syntax.Node, lit *syntax.Lit) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		param, ok := stack[i].(*syntax.ParamExp)
		if ok {
			return param.Param == lit
		}
	}
	return false
}

func shellASTContainer(stack []syntax.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if function, ok := stack[i].(*syntax.FuncDecl); ok && function.Name != nil {
			return "function " + function.Name.Value
		}
	}
	return "file"
}

func formatASTMatches(file fileops.File, query, language string, matches []astMatch, contextLines, maxMatches, index int, warnings []string) *tool.Result {
	contextLines = fileops.NormalizeGrepContextLines(contextLines)
	maxMatches = fileops.NormalizeMaxMatches(maxMatches)
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Line == matches[j].Line {
			return matches[i].Column < matches[j].Column
		}
		return matches[i].Line < matches[j].Line
	})
	matchCount := len(matches)
	truncated := matchCount > maxMatches
	if index > 0 {
		if index > matchCount {
			return &tool.Result{Content: fmt.Sprintf("index %d is out of range; found %d matches", index, matchCount), Warnings: warnings}
		}
		matches = matches[index-1 : index]
		truncated = false
	} else if truncated {
		matches = matches[:maxMatches]
	}
	lines := fileops.SplitLines(file.Text)
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\nencoding: %s\nrevision: %s\nast: %q\nlanguage: %s\nmatches: %d\ntruncated: %t\n", file.Path, file.Encoding, fileops.ContentRevision(file.Bytes), query, language, len(matches), truncated)
	if index > 0 {
		fmt.Fprintf(&b, "index: %d\n", index)
	}
	b.WriteString("content:\n")
	width := len(fmt.Sprintf("%d", len(lines)))
	for index, match := range matches {
		if index > 0 {
			b.WriteString("--\n")
		}
		fmt.Fprintf(&b, "%d. match: %d:%d [%s] in %s\n", index+1, match.Line, match.Column, match.Kind, match.Container)
		start := max(1, match.Line-contextLines)
		end := min(len(lines), match.Line+contextLines)
		for line := start; line <= end; line++ {
			marker := " "
			if line == match.Line {
				marker = ">"
			}
			fmt.Fprintf(&b, "%s %*d | %s\n", marker, width, line, lines[line-1])
		}
	}
	return &tool.Result{Content: b.String(), Warnings: warnings}
}

func formatASTFunctionMatches(file fileops.File, query, language string, matches []astFunctionMatch, maxMatches, index int, warnings []string) *tool.Result {
	maxMatches = fileops.NormalizeMaxMatches(maxMatches)
	matchCount := len(matches)
	truncated := matchCount > maxMatches
	if index > 0 {
		if index > matchCount {
			return &tool.Result{Content: fmt.Sprintf("index %d is out of range; found %d matches", index, matchCount), Warnings: warnings}
		}
		matches = matches[index-1 : index]
		truncated = false
	} else if truncated {
		matches = matches[:maxMatches]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "file: %s\nencoding: %s\nrevision: %s\nast_function: %q\nlanguage: %s\nmatches: %d\ntruncated: %t\n", file.Path, file.Encoding, fileops.ContentRevision(file.Bytes), query, language, len(matches), truncated)
	if len(matches) > 1 && index == 0 {
		b.WriteString("selection_required: true\ncontent:\n")
		for matchIndex, match := range matches {
			fmt.Fprintf(&b, "%d. %s - %s:%d-%d\n", matchIndex+1, match.Name, file.Path, match.StartLine, match.EndLine)
		}
		return &tool.Result{Content: b.String(), Warnings: warnings}
	}
	if index > 0 {
		fmt.Fprintf(&b, "index: %d\n", index)
	}
	b.WriteString("content:\n")
	lines := fileops.SplitLines(file.Text)
	width := len(fmt.Sprintf("%d", len(lines)))
	for _, match := range matches {
		fmt.Fprintf(&b, "match: %d-%d [%s] %s\n", match.StartLine, match.EndLine, match.Kind, match.Name)
		for line := match.StartLine; line <= match.EndLine; line++ {
			fmt.Fprintf(&b, "  %*d | %s\n", width, line, lines[line-1])
		}
	}
	return &tool.Result{Content: b.String(), Warnings: warnings}
}
