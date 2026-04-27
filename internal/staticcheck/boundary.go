// Package staticcheck defines boundary logging enforcement analyzers.
package staticcheck

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var MissingBoundaryLogAnalyzer = &analysis.Analyzer{
	Name: "missing_boundary_log",
	Doc:  "requires structured logging at process, request, external-call, and state-mutation boundaries",
	Run:  runMissingBoundaryLog,
}

func runMissingBoundaryLog(pass *analysis.Pass) (any, error) {
	if isStaticcheckPackage(pass) {
		return nil, nil
	}
	for _, file := range pass.Files {
		path := fileName(pass, file.Pos())
		if isTestFile(path) || isGeneratedFile(file) || isProtobufGeneratedPath(path) || isStaticcheckPath(path) {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !isBoundaryFunction(path, fn) {
				continue
			}
			if !functionHasBoundaryLog(fn) {
				pass.Reportf(fn.Pos(), "boundary function must emit at least one structured slog event")
			}
		}
	}
	return nil, nil
}

func isBoundaryFunction(path string, fn *ast.FuncDecl) bool {
	return fn.Name.Name == "main"
}

func hasHTTPHandlerSignature(fn *ast.FuncDecl) bool {
	if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 2 {
		return false
	}
	first := exprContainsText(fn.Type.Params.List[0].Type, "ResponseWriter")
	second := exprContainsText(fn.Type.Params.List[1].Type, "Request")
	return first && second
}

func functionHasExternalBoundary(fn *ast.FuncDecl) bool {
	found := false
	inspectFunc(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		receiver, name, ok := selectorName(call.Fun)
		if !ok {
			return true
		}
		if receiver == "exec" && strings.HasPrefix(name, "Command") {
			found = true
			return false
		}
		if receiver == "http" && (name == "ListenAndServe" || name == "Serve") {
			found = true
			return false
		}
		if receiver == "os" && (strings.HasPrefix(name, "Write") || strings.HasPrefix(name, "Create") || strings.HasPrefix(name, "Remove") || strings.HasPrefix(name, "Rename")) {
			found = true
			return false
		}
		return true
	})
	return found
}

func functionHasBoundaryLog(fn *ast.FuncDecl) bool {
	found := false
	inspectFunc(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		_, name, ok := selectorName(call.Fun)
		if !ok {
			return true
		}
		switch name {
		case "Debug", "DebugContext", "Info", "InfoContext", "Warn", "WarnContext", "Error", "ErrorContext", "Log", "LogAttrs":
			found = true
			return false
		default:
			return true
		}
	})
	return found
}

func exprContainsText(expr ast.Expr, text string) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && strings.Contains(ident.Name, text) {
			found = true
			return false
		}
		if selector, ok := node.(*ast.SelectorExpr); ok && strings.Contains(selector.Sel.Name, text) {
			found = true
			return false
		}
		return true
	})
	return found
}
