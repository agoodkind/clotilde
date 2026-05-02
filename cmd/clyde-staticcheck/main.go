package main

import (
	"log/slog"
	"os"

	"golang.org/x/tools/go/analysis/multichecker"

	clydestaticcheck "goodkind.io/clyde/internal/staticcheck"
)

func main() {
	if os.Getenv("CLYDE_STATICCHECK_LOG") != "" {
		slog.Info("clyde.staticcheck.started", "component", "staticcheck")
	}
	multichecker.Main(clydestaticcheck.Analyzers()...)
}
