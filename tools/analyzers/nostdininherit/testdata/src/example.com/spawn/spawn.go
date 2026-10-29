package spawn

import (
	"os"
	"os/exec"
)

// BadFiles inherits the transport stdin.
func BadFiles() *os.ProcAttr {
	return &os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}} // want "child must not inherit os.Stdin"
}

// BadCmd forwards the process stdin.
func BadCmd() *exec.Cmd {
	return &exec.Cmd{Stdin: os.Stdin} // want "child must not inherit os.Stdin"
}

// GoodDevNull gives the child an explicit null device.
func GoodDevNull(devnull *os.File) *os.ProcAttr {
	return &os.ProcAttr{Files: []*os.File{devnull, nil, nil}}
}

// GoodStdinField only passes stdout/stderr explicitly.
func GoodStdinField() *exec.Cmd {
	return &exec.Cmd{Stdout: os.Stdout, Stderr: os.Stderr}
}
