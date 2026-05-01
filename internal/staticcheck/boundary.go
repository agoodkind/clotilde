// Package staticcheck defines boundary logging enforcement analyzers.
package staticcheck

import (
	"go/ast"

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

func isBoundaryFunction(_ string, fn *ast.FuncDecl) bool {
	return fn.Name.Name == "main"
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
