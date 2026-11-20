package builtin

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"strings"

	"elbot/internal/fileops"
	"elbot/internal/operationresult"
	toolshare "elbot/internal/tool/share"
)

const (
	readFileModeRead = "read"
	readFileModeGrep = "grep"
	readFileModeAST  = "ast"
)

type fileIndexEntry struct {
	Section   string   // 骨架分组，决定顶层条目的编号顺序。
	Text      string   // 不包含函数体或初始化值的声明摘要。
	StartLine int      // 声明起始行，包含关联的前置文档注释。
	EndLine   int      // 声明结束行，闭区间。
	Members   []string // 类型的字段或接口成员，展示时按需限制数量。
}

func normalizeReadFileMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		return readFileModeRead, nil
	}
	switch mode {
	case readFileModeRead, readFileModeGrep, readFileModeAST:
		return mode, nil
	default:
		return "", fmt.Errorf("read_file mode must be read, grep, or ast")
	}
}

func readFileASTResult(file fileops.File, index readFileIndex, warnings []string) (operationresult.Result[*toolshare.Result], error) {
	if !strings.EqualFold(filepath.Ext(file.Path), ".go") {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidTarget, Message: "ast mode only supports Go files; use read or grep mode"}, nil
	}
	entries, warning, err := buildGoFileIndex(file.Path, file.Text)
	if err != nil {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidTarget, Message: err.Error()}, nil
	}
	if warning != "" {
		warnings = append(warnings, warning)
	}
	if index.outOfRange(len(entries)) {
		return operationresult.Result[*toolshare.Result]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("index %d is out of range; found %d entries", index.start, len(entries))}, nil
	}
	start, end := index.start-1, min(index.end, len(entries))
	var b strings.Builder
	fmt.Fprintf(&b, "%s | matches: %s\n", readFileHeader(file), index.summary(len(entries), true))
	section := ""
	for i := start; i < end; i++ {
		entry := entries[i]
		if section != entry.Section {
			section = entry.Section
			fmt.Fprintf(&b, "%s:\n", section)
		}
		fmt.Fprintf(&b, "  %d. %s [%d-%d]\n", i+1, entry.Text, entry.StartLine, entry.EndLine)
		memberCount := len(entry.Members)
		if !index.single {
			memberCount = min(memberCount, 8)
		}
		for _, member := range entry.Members[:memberCount] {
			fmt.Fprintf(&b, "    %s\n", member)
		}
		if memberCount < len(entry.Members) {
			fmt.Fprintf(&b, "    [%d more members; use index=%d to expand]\n", len(entry.Members)-memberCount, i+1)
		}
	}
	return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: b.String(), Warnings: warnings}}, nil
}

func buildGoFileIndex(path, source string) ([]fileIndexEntry, string, error) {
	if strings.TrimSpace(source) == "" {
		return nil, "", nil
	}
	fset := token.NewFileSet()
	file, parseErr := parser.ParseFile(fset, path, source, parser.AllErrors|parser.ParseComments)
	if file == nil || file.Name == nil || file.Name.Name == "" {
		return nil, "", fmt.Errorf("parse Go source: %w", parseErr)
	}
	groups := make(map[string][]fileIndexEntry)
	var formatErr error
	text := func(node ast.Node) (string, bool) {
		node, omitted := projectGoIndexNode(node)
		var b bytes.Buffer
		if err := format.Node(&b, fset, node); err != nil {
			formatErr = err
		}
		formatted := b.String()
		var scan scanner.Scanner
		positions := token.NewFileSet()
		scanFile := positions.AddFile("", -1, len(formatted))
		scan.Init(scanFile, []byte(formatted), nil, 0)
		var compact strings.Builder
		gap := func(value string) {
			if strings.TrimLeft(value, " \t\r\n") != value {
				compact.WriteByte(' ')
			}
			compact.WriteString(strings.Join(strings.Fields(value), " "))
			if strings.TrimSpace(value) != "" && strings.TrimRight(value, " \t\r\n") != value {
				compact.WriteByte(' ')
			}
		}
		start := 0
		for {
			pos, kind, literal := scan.Scan()
			if kind == token.EOF {
				break
			}
			if kind != token.STRING && kind != token.CHAR {
				continue
			}
			offset := scanFile.Offset(pos)
			gap(formatted[start:offset])
			compact.WriteString(literal)
			start = offset + len(literal)
		}
		gap(formatted[start:])
		return strings.TrimSpace(compact.String()), omitted
	}
	names := func(ids []*ast.Ident) string {
		values := make([]string, len(ids))
		for i, id := range ids {
			values[i] = id.Name
		}
		return strings.Join(values, ", ")
	}
	members := func(fields *ast.FieldList, isInterface bool) []string {
		var result []string
		for _, field := range fields.List {
			value, omitted := text(field.Type)
			if len(field.Names) > 0 {
				if _, method := field.Type.(*ast.FuncType); method && isInterface {
					value = names(field.Names) + strings.TrimPrefix(value, "func")
				} else {
					value = names(field.Names) + " " + value
				}
			}
			if omitted {
				value += " [nested details omitted]"
			}
			result = append(result, value)
		}
		return result
	}
	add := func(section, value string, omitted bool, node ast.Node, doc *ast.CommentGroup, children []string) {
		if omitted {
			value += " [nested details omitted]"
		}
		start := node.Pos()
		if doc != nil {
			start = doc.Pos()
		}
		groups[section] = append(groups[section], fileIndexEntry{Section: section, Text: value, StartLine: fset.PositionFor(start, false).Line, EndLine: fset.PositionFor(node.End(), false).Line, Members: children})
	}
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			for i, spec := range decl.Specs {
				doc := decl.Doc
				if i > 0 {
					doc = nil
				}
				switch spec := spec.(type) {
				case *ast.ImportSpec:
					value := spec.Path.Value
					if spec.Name != nil {
						value = spec.Name.Name + " " + value
					}
					if spec.Doc != nil {
						doc = spec.Doc
					}
					add("imports", value, false, spec, doc, nil)
				case *ast.ValueSpec:
					value := decl.Tok.String() + " " + names(spec.Names)
					omitted := false
					if spec.Type != nil {
						var typ string
						typ, omitted = text(spec.Type)
						value += " " + typ
					}
					if spec.Doc != nil {
						doc = spec.Doc
					}
					add("consts", value, omitted, spec, doc, nil)
				case *ast.TypeSpec:
					projection := *spec
					projection.Doc, projection.Comment = nil, nil
					var children []string
					switch typ := spec.Type.(type) {
					case *ast.StructType:
						projection.Type = ast.NewIdent("struct")
						children = members(typ.Fields, false)
					case *ast.InterfaceType:
						projection.Type = ast.NewIdent("interface")
						children = members(typ.Methods, true)
					}
					value, omitted := text(&ast.GenDecl{Tok: token.TYPE, Specs: []ast.Spec{&projection}})
					value = strings.TrimPrefix(value, "type ")
					if spec.Doc != nil {
						doc = spec.Doc
					}
					add("types", value, omitted, spec, doc, children)
				}
			}
		case *ast.FuncDecl:
			section := "fns"
			if decl.Recv != nil {
				section = "impls"
			}
			signature := *decl
			signature.Doc, signature.Body = nil, nil
			value, omitted := text(&signature)
			add(section, value, omitted, decl, decl.Doc, nil)
		}
	}
	if formatErr != nil {
		return nil, "", fmt.Errorf("format Go index: %w", formatErr)
	}
	var entries []fileIndexEntry
	for _, section := range []string{"imports", "consts", "types", "impls", "fns"} {
		entries = append(entries, groups[section]...)
	}
	warning := ""
	if parseErr != nil {
		warning = "Go AST 解析存在错误，结果可能不完整：" + parseErr.Error()
	}
	return entries, warning, nil
}

func projectGoIndexNode(node ast.Node) (ast.Node, bool) {
	omitted := false
	expr := func(value ast.Expr) ast.Expr {
		if value == nil {
			return nil
		}
		projected, hidden := projectGoIndexNode(value)
		omitted = omitted || hidden
		return projected.(ast.Expr)
	}
	exprs := func(values []ast.Expr) []ast.Expr {
		result := make([]ast.Expr, len(values))
		for i, value := range values {
			result[i] = expr(value)
		}
		return result
	}
	fields := func(value *ast.FieldList) *ast.FieldList {
		if value == nil {
			return nil
		}
		projected, hidden := projectGoIndexNode(value)
		omitted = omitted || hidden
		return projected.(*ast.FieldList)
	}
	switch node := node.(type) {
	case *ast.StructType:
		copy := *node
		copy.Fields = &ast.FieldList{}
		return &copy, len(node.Fields.List) > 0
	case *ast.InterfaceType:
		copy := *node
		copy.Methods = &ast.FieldList{}
		return &copy, len(node.Methods.List) > 0
	case *ast.FuncLit:
		copy := *node
		copy.Type = &ast.FuncType{Func: node.Type.Func, TypeParams: fields(node.Type.TypeParams), Params: fields(node.Type.Params), Results: fields(node.Type.Results)}
		copy.Body = &ast.BlockStmt{}
		return &copy, true
	case *ast.FieldList:
		copy := *node
		copy.List = make([]*ast.Field, len(node.List))
		for i, field := range node.List {
			member := *field
			member.Doc, member.Comment = nil, nil
			member.Type = expr(field.Type)
			copy.List[i] = &member
		}
		return &copy, omitted
	case *ast.FuncType:
		copy := *node
		copy.TypeParams, copy.Params, copy.Results = fields(node.TypeParams), fields(node.Params), fields(node.Results)
		return &copy, omitted
	case *ast.FuncDecl:
		copy := *node
		copy.Doc, copy.Body = nil, nil
		copy.Recv = fields(node.Recv)
		copy.Type = expr(node.Type).(*ast.FuncType)
		return &copy, omitted
	case *ast.GenDecl:
		copy := *node
		copy.Doc = nil
		copy.Specs = make([]ast.Spec, len(node.Specs))
		for i, spec := range node.Specs {
			projected, hidden := projectGoIndexNode(spec)
			omitted = omitted || hidden
			copy.Specs[i] = projected.(ast.Spec)
		}
		return &copy, omitted
	case *ast.TypeSpec:
		copy := *node
		copy.Doc, copy.Comment = nil, nil
		copy.TypeParams, copy.Type = fields(node.TypeParams), expr(node.Type)
		return &copy, omitted
	case *ast.ArrayType:
		copy := *node
		copy.Len, copy.Elt = expr(node.Len), expr(node.Elt)
		return &copy, omitted
	case *ast.MapType:
		copy := *node
		copy.Key, copy.Value = expr(node.Key), expr(node.Value)
		return &copy, omitted
	case *ast.ChanType:
		copy := *node
		copy.Value = expr(node.Value)
		return &copy, omitted
	case *ast.StarExpr:
		copy := *node
		copy.X = expr(node.X)
		return &copy, omitted
	case *ast.Ellipsis:
		copy := *node
		copy.Elt = expr(node.Elt)
		return &copy, omitted
	case *ast.ParenExpr:
		copy := *node
		copy.X = expr(node.X)
		return &copy, omitted
	case *ast.UnaryExpr:
		copy := *node
		copy.X = expr(node.X)
		return &copy, omitted
	case *ast.BinaryExpr:
		copy := *node
		copy.X, copy.Y = expr(node.X), expr(node.Y)
		return &copy, omitted
	case *ast.CallExpr:
		copy := *node
		copy.Fun, copy.Args = expr(node.Fun), exprs(node.Args)
		return &copy, omitted
	case *ast.IndexExpr:
		copy := *node
		copy.X, copy.Index = expr(node.X), expr(node.Index)
		return &copy, omitted
	case *ast.IndexListExpr:
		copy := *node
		copy.X, copy.Indices = expr(node.X), exprs(node.Indices)
		return &copy, omitted
	case *ast.SelectorExpr:
		copy := *node
		copy.X = expr(node.X)
		return &copy, omitted
	case *ast.SliceExpr:
		copy := *node
		copy.X, copy.Low, copy.High, copy.Max = expr(node.X), expr(node.Low), expr(node.High), expr(node.Max)
		return &copy, omitted
	case *ast.TypeAssertExpr:
		copy := *node
		copy.X, copy.Type = expr(node.X), expr(node.Type)
		return &copy, omitted
	case *ast.CompositeLit:
		copy := *node
		copy.Type, copy.Elts = expr(node.Type), exprs(node.Elts)
		return &copy, omitted
	case *ast.KeyValueExpr:
		copy := *node
		copy.Key, copy.Value = expr(node.Key), expr(node.Value)
		return &copy, omitted
	default:
		return node, false
	}
}
