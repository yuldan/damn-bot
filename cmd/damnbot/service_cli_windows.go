//go:build windows

package main

// OS effects of the Task Scheduler backend (service_schtasks.go): schtasks
// execution, the staged UTF-16 XML file, and the process probe that answers
// EC-WIN-003. Nothing here is stubbed on other platforms: on Linux/macOS
// runWindowsService is the explicit "Windows-only" refusal in
// service_cli_nowindows.go.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func runWindowsService(args []string, stdout, stderr io.Writer) int {
	d, err := windowsServiceDeps(activeProfile())
	if err != nil {
		sessionsPrintf(stderr, "service: %s\n", cleanLine(err.Error()))
		return 1
	}
	return winRun(context.Background(), d, args, stdout, stderr)
}

func windowsServiceDeps(profile string) (*winServiceDeps, error) {
	base, err := serviceDepsFrom()
	if err != nil {
		return nil, err
	}
	usr, uerr := user.Current()
	if uerr != nil {
		return nil, fmt.Errorf("resolve current user: %w", uerr)
	}
	return &winServiceDeps{
		base: base, profile: profile, user: usr.Username,
		run: windowsSchtasks, stage: stageTaskXML,
		leasePID: func(dataDir string) int {
			raw, rerr := os.ReadFile(filepath.Join(dataDir, "run", "serve.lock")) //nolint:gosec // operator data dir
			if rerr != nil {
				return 0
			}
			return leaseOwnerPID(raw)
		},
		alive:  windowsProcessAlive,
		hosted: windowsTaskHosted,
	}, nil
}

func windowsSchtasks(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "schtasks", args...).CombinedOutput() // #nosec G204 -- fixed binary, literal args
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return out, fmt.Errorf("%w: %v", errWinToolMissing, err)
		}
		return out, fmt.Errorf("schtasks: %w", err)
	}
	return out, nil
}

func stageTaskXML(content []byte) (string, func() error, error) {
	f, err := os.CreateTemp("", "damnbot-task-*.xml")
	if err != nil {
		return "", nil, fmt.Errorf("create task XML: %w", err)
	}
	path := f.Name()
	cleanup := func() error {
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return fmt.Errorf("remove task XML: %w", rerr)
		}
		return nil
	}
	_, werr := f.Write(content)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return "", cleanup, fmt.Errorf("write task XML: %w", errors.Join(werr, cerr))
	}
	return path, cleanup, nil
}

// windowsProcessAlive uses the wait state, not the exit code: a real exit code
// 259 would otherwise read as STILL_ACTIVE (EC-WIN-009).
func windowsProcessAlive(pid int) (alive bool, err error) {
	if pid <= 0 || int64(pid) > math.MaxUint32 {
		return false, nil
	}
	h, oerr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if oerr != nil {
		if errors.Is(oerr, windows.ERROR_INVALID_PARAMETER) || errors.Is(oerr, windows.ERROR_NOT_FOUND) {
			return false, nil
		}
		return false, fmt.Errorf("open process %d: %w", pid, oerr)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(h)) }()
	event, werr := windows.WaitForSingleObject(h, 0)
	if werr != nil {
		return false, fmt.Errorf("wait process %d: %w", pid, werr)
	}
	return event == uint32(windows.WAIT_TIMEOUT), nil
}

// windowsTaskHosted walks the parent chain for a scheduler host (taskhostw,
// taskeng, or the scheduling service svchost).
func windowsTaskHosted(pid int) (bool, error) {
	table, err := windowsProcessTable()
	if err != nil {
		return false, err
	}
	for depth := 0; depth < 8; depth++ {
		child, ok := table[uint32(pid)]
		if !ok || child.ParentProcessID == 0 || child.ParentProcessID == child.ProcessID {
			return false, nil
		}
		parent, ok := table[child.ParentProcessID]
		if !ok {
			return false, nil
		}
		switch strings.ToLower(windows.UTF16ToString(parent.ExeFile[:])) {
		case "taskhostw.exe", "taskhost.exe", "taskeng.exe", "svchost.exe":
			return true, nil
		}
		pid = int(parent.ProcessID)
	}
	return false, nil
}

func windowsProcessTable() (table map[uint32]windows.ProcessEntry32, err error) {
	snap, serr := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if serr != nil {
		return nil, fmt.Errorf("process snapshot: %w", serr)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(snap)) }()
	table = make(map[uint32]windows.ProcessEntry32, 256)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	serr = windows.Process32First(snap, &e)
	for serr == nil {
		table[e.ProcessID] = e
		serr = windows.Process32Next(snap, &e)
	}
	if !errors.Is(serr, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("process snapshot: %w", serr)
	}
	return table, nil
}
