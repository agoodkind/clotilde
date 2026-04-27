// Package staticcheck wires Clyde's Staticcheck bundle and repo-local architecture analyzers.
package staticcheck

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
)

func fileName(pass *analysis.Pass, pos token.Pos) string {
	return filepath.ToSlash(pass.Fset.Position(pos).Filename)
}

func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}

func isGeneratedFile(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			text := strings.ToLower(comment.Text)
			if strings.Contains(text, "code generated") || strings.Contains(text, "do not edit") {
				return true
			}
		}
	}
	return false
}

func packagePath(pass *analysis.Pass) string {
	if pass.Pkg == nil {
		return ""
	}
	return pass.Pkg.Path()
}

func isStaticcheckPackage(pass *analysis.Pass) bool {
	return strings.HasSuffix(packagePath(pass), "/internal/staticcheck")
}

func isStaticcheckPath(path string) bool {
	return strings.Contains(path, "/internal/staticcheck/") || strings.Contains(path, "/cmd/clyde-staticcheck/")
}

func isProtobufGeneratedPath(path string) bool {
	return strings.HasSuffix(path, ".pb.go") || strings.Contains(path, "/api/")
}

func selectorName(expr ast.Expr) (receiver string, name string, ok bool) {
	call, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	name = call.Sel.Name
	switch recv := call.X.(type) {
	case *ast.Ident:
		receiver = recv.Name
	case *ast.SelectorExpr:
		_, recvName, recvOK := selectorName(recv)
		if recvOK {
			receiver = recvName
		}
	}
	return receiver, name, true
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value := strings.Trim(lit.Value, "`\"")
	return value, true
}

func inspectFunc(fn *ast.FuncDecl, visit func(ast.Node) bool) {
	if fn == nil || fn.Body == nil {
		return
	}
	ast.Inspect(fn.Body, visit)
}
