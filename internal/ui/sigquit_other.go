//go:build !unix

package ui

func installSIGQUITDumpHandler() func() {
	return func() {}
}
