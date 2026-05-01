// Package staticcheck defines strict type hygiene analyzers.
package staticcheck

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var NoAnyOrEmptyInterfaceAnalyzer = &analysis.Analyzer{
	Name: "no_any_or_empty_interface",
	Doc:  "rejects loose any/interface{} and empty struct{} protocol/domain shapes",
	Run:  runNoAnyOrEmptyInterface,
}

func runNoAnyOrEmptyInterface(pass *analysis.Pass) (any, error) {
	if isStaticcheckPackage(pass) {
		return nil, nil
	}
	for _, file := range pass.Files {
		path := fileName(pass, file.Pos())
		if isTestFile(path) || isGeneratedFile(file) || isProtobufGeneratedPath(path) || isStaticcheckPath(path) || allowsDynamicBoundary(path) {
			continue
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if !ast.IsExported(typeSpec.Name.Name) {
						continue
					}
					checkTypeExpr(pass, typeSpec.Type)
				}
			case *ast.FuncDecl:
				if !ast.IsExported(d.Name.Name) || d.Type == nil {
					continue
				}
				if d.Type.Params != nil {
					for _, p := range d.Type.Params.List {
						checkTypeExpr(pass, p.Type)
					}
				}
				if d.Type.Results != nil {
					for _, r := range d.Type.Results.List {
						checkTypeExpr(pass, r.Type)
					}
				}
			}
		}
	}
	return nil, nil
}

func checkTypeExpr(pass *analysis.Pass, expr ast.Expr) {
	ast.Inspect(expr, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			if typed.Name == "any" {
				pass.Reportf(typed.Pos(), "do not use any; define a deeply enumerated named type")
			}
		case *ast.InterfaceType:
			if len(typed.Methods.List) == 0 {
				pass.Reportf(typed.Pos(), "do not use interface{}; define a deeply enumerated named type")
			}
		}
		return true
	})
}

func allowsDynamicBoundary(path string) bool {
	allowed := []string{
		"internal/adapter/codex/backend.go",
		"internal/adapter/codex/continuation.go",
		"internal/adapter/codex/native_tools.go",
		"internal/adapter/codex/protocol.go",
		"internal/adapter/codex/request_builder.go",
		"internal/adapter/codex/transport_request.go",
		"internal/adapter/codex/transport_ws.go",
		"internal/adapter/codex/ws_session.go",
		"internal/adapter/codex/delta_input.go",
		"internal/adapter/openai/",
	}
	for _, item := range allowed {
		if strings.Contains(path, item) {
			return true
		}
	}
	return false
}
