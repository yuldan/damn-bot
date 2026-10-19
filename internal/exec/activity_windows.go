package exec

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// basicAndIOAccounting mirrors JOBOBJECT_BASIC_AND_IO_ACCOUNTING_INFORMATION.
type basicAndIOAccounting struct {
	Basic basicAccounting
	IO    windows.IO_COUNTERS
}

// pidUsage is cumulative CPU time and I/O bytes (here: of the whole job).
type pidUsage struct {
	cpu time.Duration
	io  uint64
}

// treeSampler holds the job totals of the previous sample.
type treeSampler struct{ total pidUsage }

// sampleTree returns the job's CPU time and I/O bytes since the previous
// sample (job totals include exited members).
func (t *treeSampler) sample(p *osProc, _ string) (time.Duration, uint64, bool) {
	var info basicAndIOAccounting
	err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAndIoAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	if err != nil {
		return 0, 0, false
	}
	// 100 ns units.
	cur := pidUsage{
		cpu: time.Duration(info.Basic.TotalUserTime+info.Basic.TotalKernelTime) * 100,
		io:  info.IO.ReadTransferCount + info.IO.WriteTransferCount + info.IO.OtherTransferCount,
	}
	cpu, io := cur.cpu-t.total.cpu, cur.io-t.total.io
	t.total = cur
	return max(cpu, 0), io, true
}

// treeWaitsInput: no wchan on Windows; detection relies on prompt lines.
func treeWaitsInput(string, bool) bool { return false }
