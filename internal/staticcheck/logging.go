// Package staticcheck defines structured logging enforcement analyzers.
package staticcheck

import (
	"go/ast"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var SlogErrorWithoutErrAnalyzer = &analysis.Analyzer{
	Name: "slog_error_without_err",
	Doc:  "requires structured slog error events to carry an err field",
	Run:  runSlogErrorWithoutErr,
}

var BannedDirectOutputAnalyzer = &analysis.Analyzer{
	Name: "banned_direct_output",
	Doc:  "rejects fmt.Print* and stdlib log diagnostics in production code",
	Run:  runBannedDirectOutput,
}

var HotLoopInfoLogAnalyzer = &analysis.Analyzer{
	Name: "hot_loop_info_log",
	Doc:  "rejects info-level structured logs directly inside loops",
	Run:  runHotLoopInfoLog,
}

func runSlogErrorWithoutErr(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		path := fileName(pass, file.Pos())
		if isTestFile(path) || isGeneratedFile(file) || isProtobufGeneratedPath(path) {
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isErrorSlogCall(call) || hasErrAttr(call) {
				return true
			}
			pass.Reportf(call.Pos(), "error-level slog event must include an err field")
			return true
		})
	}
	return nil, nil
}

func runBannedDirectOutput(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		path := fileName(pass, file.Pos())
		if isTestFile(path) || isGeneratedFile(file) || isProtobufGeneratedPath(path) {
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			receiver, name, ok := selectorName(call.Fun)
			if !ok {
				return true
			}
			if receiver == "fmt" && (name == "Print" || name == "Printf" || name == "Println") {
				pass.Reportf(call.Pos(), "do not use fmt.%s for production diagnostics; use slog or write to an explicit user-facing writer", name)
			}
			if receiver == "log" && (strings.HasPrefix(name, "Print") || strings.HasPrefix(name, "Fatal") || strings.HasPrefix(name, "Panic")) {
				pass.Reportf(call.Pos(), "do not use log.%s for production diagnostics; use structured slog", name)
			}
			return true
		})
	}
	return nil, nil
}

func runHotLoopInfoLog(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		path := fileName(pass, file.Pos())
		if isTestFile(path) || isGeneratedFile(file) || isProtobufGeneratedPath(path) {
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch loop := node.(type) {
			case *ast.ForStmt:
				reportInfoLogsInLoop(pass, loop.Body)
			case *ast.RangeStmt:
				reportInfoLogsInLoop(pass, loop.Body)
			}
			return true
		})
	}
	return nil, nil
}

func isErrorSlogCall(call *ast.CallExpr) bool {
	receiver, name, ok := selectorName(call.Fun)
	if !ok {
		return false
	}
	if (name == "Error" || name == "ErrorContext") && isLikelyLoggerReceiver(receiver) {
		return true
	}
	if name == "LogAttrs" && isLikelyLoggerReceiver(receiver) && len(call.Args) >= 2 {
		return exprContains(call.Args[1], "LevelError")
	}
	return receiver == "slog" && name == "Log" && len(call.Args) >= 2 && exprContains(call.Args[1], "LevelError")
}

func hasErrAttr(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		found := false
		ast.Inspect(arg, func(node ast.Node) bool {
			if found {
				return false
			}
			if expr, ok := node.(ast.Expr); ok {
				if value, ok := stringLiteral(expr); ok && isErrKey(value) {
					found = true
					return false
				}
			}
			nested, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			_, name, selectorOK := selectorName(nested.Fun)
			if !selectorOK || len(nested.Args) == 0 {
				return true
			}
			if name == "Any" || name == "String" || name == "Attr" {
				if value, ok := stringLiteral(nested.Args[0]); ok && isErrKey(value) {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

func reportInfoLogsInLoop(pass *analysis.Pass, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isInfoSlogCall(call) {
			return true
		}
		pass.Reportf(call.Pos(), "do not emit info-level slog events directly inside loops; log state transitions or summaries")
		return true
	})
}

func isInfoSlogCall(call *ast.CallExpr) bool {
	receiver, name, ok := selectorName(call.Fun)
	if !ok {
		return false
	}
	if (name == "Info" || name == "InfoContext") && isLikelyLoggerReceiver(receiver) {
		return true
	}
	if name == "LogAttrs" && isLikelyLoggerReceiver(receiver) && len(call.Args) >= 2 {
		return exprContains(call.Args[1], "LevelInfo")
	}
	return receiver == "slog" && name == "Log" && len(call.Args) >= 2 && exprContains(call.Args[1], "LevelInfo")
}

func isLikelyLoggerReceiver(receiver string) bool {
	if receiver == "" {
		return false
	}
	lower := strings.ToLower(receiver)
	if lower == "slog" {
		return true
	}
	return strings.Contains(lower, "log")
}

func isErrKey(k string) bool {
	return k == "err" || k == "error"
}

func exprContains(expr ast.Expr, needle string) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && ident.Name == needle {
			found = true
			return false
		}
		if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == needle {
			found = true
			return false
		}
		return true
	})
	return found
}
