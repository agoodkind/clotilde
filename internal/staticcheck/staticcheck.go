// Package staticcheck exposes Clyde's Staticcheck analyzer set.
package staticcheck

import (
	"golang.org/x/tools/go/analysis"
	"honnef.co/go/tools/analysis/lint"
	"honnef.co/go/tools/quickfix"
	"honnef.co/go/tools/simple"
	upstreamstaticcheck "honnef.co/go/tools/staticcheck"
	"honnef.co/go/tools/stylecheck"
)

func Analyzers() []*analysis.Analyzer {
	out := make([]*analysis.Analyzer, 0, 256)
	out = appendLintAnalyzers(out, upstreamstaticcheck.Analyzers)
	out = appendLintAnalyzers(out, simple.Analyzers)
	out = appendLintAnalyzers(out, stylecheck.Analyzers)
	out = appendLintAnalyzers(out, quickfix.Analyzers)
	out = append(out,
		SlogErrorWithoutErrAnalyzer,
		BannedDirectOutputAnalyzer,
		HotLoopInfoLogAnalyzer,
		MissingBoundaryLogAnalyzer,
		NoAnyOrEmptyInterfaceAnalyzer,
	)
	return out
}

func appendLintAnalyzers(out []*analysis.Analyzer, analyzers []*lint.Analyzer) []*analysis.Analyzer {
	for _, analyzer := range analyzers {
		out = append(out, analyzer.Analyzer)
	}
	return out
}
