//go:build windows && !minimal

package exec

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procAlive reports whether pid is a live process.
func procAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// childrenOf returns the live child PIDs of parent with the given image name.
func childrenOf(t *testing.T, parent uint32, image string) []uint32 {
	t.Helper()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer windows.CloseHandle(snap)
	var out []uint32
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		if e.ParentProcessID == parent && strings.EqualFold(name, image) {
			out = append(out, e.ProcessID)
		}
	}
	return out
}

// TestF5BackgroundWindowsJobObject closes the F5 carryover: `shell
// background:true` starts a process tree on Windows, the registry exposes
// the process_* surface, and Close kills the leader and every descendant
// through the Job Object (KILL_ON_JOB_CLOSE) with no orphan left. The
// negative control is a witness process started outside the registry: the
// same liveness probe reports it alive, so a green check is not vacuous.
func TestF5BackgroundWindowsJobObject(t *testing.T) {
	if !BackgroundSupported {
		t.Fatal("BackgroundSupported = false on Windows")
	}
	cmdPath, err := osexec.LookPath("cmd")
	if err != nil {
		t.Fatalf("cmd.exe: %v", err)
	}
	b := NewBackground()
	env := []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot")}
	info, err := b.Start(context.Background(), "s",
		[]string{cmdPath, "/d", "/c", "echo up& ping -n 60 127.0.0.1 >NUL"}, "", env)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var child uint32
	for range 200 {
		if got := childrenOf(t, uint32(info.PID), "PING.EXE"); len(got) > 0 {
			child = got[0]
		}
		if pr, perr := b.Poll("s", info.ID, 0, 0, ""); perr == nil && strings.Contains(string(pr.Output), "up") && child != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 || !procAlive(child) {
		t.Fatalf("no live descendant under pid %d", info.PID)
	}
	if got := b.List("s"); len(got) != 1 || got[0].Status != ProcessRunning {
		t.Fatalf("registry = %+v", got)
	}

	// Negative control: a process outside the registry stays alive across
	// Close, proving the liveness probe and the kill window are meaningful.
	witness := osexec.Command("ping", "-n", "60", "127.0.0.1")
	if err := witness.Start(); err != nil {
		t.Fatalf("witness: %v", err)
	}
	witnessPID := uint32(witness.Process.Pid)
	defer func() {
		_ = witness.Process.Kill()
		_ = witness.Wait()
	}()

	if err := b.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for (procAlive(uint32(info.PID)) || procAlive(child)) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if procAlive(uint32(info.PID)) || procAlive(child) {
		t.Fatalf("job tree survived Close: leader=%v child=%v", procAlive(uint32(info.PID)), procAlive(child))
	}
	if !procAlive(witnessPID) {
		t.Fatal("witness died with the job: liveness probe or job limit leaks")
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := b.Start(context.Background(), "s", []string{cmdPath, "/c", "exit 0"}, "", env); !errors.Is(err, ErrProcessClosed) {
		t.Fatalf("Start after Close = %v", err)
	}
}
