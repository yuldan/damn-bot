//go:build windows && !minimal

package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// BackgroundSupported is true: startBackground owns the tree with a Job
// Object and KILL_ON_JOB_CLOSE, so registry Close kills every descendant
// instead of detaching a process it cannot reap (EXEC-AND-CODING §A.4).
const BackgroundSupported = true

// bgJob owns one background process tree. terminate is idempotent and
// closeJob runs once, after the leader exits, so a surviving child is
// killed by KILL_ON_JOB_CLOSE before the handle disappears.
type bgJob struct {
	mu  sync.Mutex
	job windows.Handle
}

func (j *bgJob) terminate() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job == 0 {
		return nil
	}
	if err := windows.TerminateJobObject(j.job, 1); err != nil {
		return fmt.Errorf("exec: terminate job: %w", err)
	}
	return nil
}

func (j *bgJob) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.job == 0 {
		return nil
	}
	err := windows.CloseHandle(j.job)
	j.job = 0
	if err != nil {
		return fmt.Errorf("exec: close job: %w", err)
	}
	return nil
}

// startBackground starts argv suspended, joins it to a job, resumes it and
// copies combined output into the registry ring.
func startBackground(ctx context.Context, sp spawnSpec) (*cmdHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("exec: background start: %w", err)
	}
	job, err := newJob()
	if err != nil {
		return nil, err
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("exec: background stdin: %w", err), windows.CloseHandle(job))
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("exec: background output: %w", err), closeAll(stdinR, stdinW), windows.CloseHandle(job))
	}
	attr := &os.ProcAttr{
		Dir:   sp.dir,
		Env:   sp.env,
		Files: []*os.File{stdinR, outW, outW},
		Sys: &syscall.SysProcAttr{
			CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP,
			HideWindow:    true,
		},
	}
	p, err := os.StartProcess(sp.argv[0], sp.argv, attr)
	cerr := closeAll(stdinR, outW)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("exec: background start: %w", err), cerr, closeAll(stdinW, outR), windows.CloseHandle(job))
	}
	if err := assignAndResume(job, p.Pid); err != nil {
		werr := p.Kill()
		_, werr2 := p.Wait()
		return nil, errors.Join(err, werr, werr2, cerr, closeAll(stdinW, outR), windows.CloseHandle(job))
	}
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		defer outR.Close()
		_, _ = io.Copy(sp.out, outR)
	}()
	owner := &bgJob{job: job}
	return &cmdHandle{
		pid:   p.Pid,
		stdin: stdinW,
		wait: func() (int, string, error) {
			st, werr := p.Wait()
			_ = stdinW.Close()
			// KILL_ON_JOB_CLOSE removes any descendant the leader left
			// behind, and EOF arrives once every write handle is closed.
			jerr := owner.close()
			<-copied
			if werr != nil {
				return 0, "", errors.Join(fmt.Errorf("exec: background wait: %w", werr), jerr)
			}
			return st.ExitCode(), "", jerr
		},
		kill: func(string) error {
			// Windows has no SIGTERM for console trees: terminate at once.
			return owner.terminate()
		},
	}, nil
}
