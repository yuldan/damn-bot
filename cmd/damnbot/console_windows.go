//go:build windows

package main

import "golang.org/x/sys/windows"

// cpUTF8 is code page 65001.
const cpUTF8 = 65001

// utf8Console switches the attached console to UTF-8 (EC-WIN-017): cmd and
// PowerShell children inherit the code page and unicode output renders
// instead of being replaced by the ANSI glyph. Without a console both calls
// fail with ERROR_INVALID_HANDLE and are ignored; redirected output is
// already UTF-8.
func utf8Console() {
	_ = windows.SetConsoleOutputCP(cpUTF8)
	_ = windows.SetConsoleCP(cpUTF8)
}
