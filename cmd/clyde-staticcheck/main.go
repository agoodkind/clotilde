package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	clydestaticcheck "goodkind.io/clyde/internal/staticcheck"
)

func main() {
	multichecker.Main(clydestaticcheck.Analyzers()...)
}
