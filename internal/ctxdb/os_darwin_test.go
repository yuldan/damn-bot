//go:build darwin

package ctxdb

// darwinSStop is SSTOP of <sys/proc.h> (stopped by a job-control signal).
const darwinSStop = 4

// procState maps kinfo_proc p_stat onto the Linux state letters the
// scenarios check: "T" = stopped, "" = absent or zombie, "R" otherwise.
func procState(pid int) string {
	k, err := procKinfo(pid)
	switch {
	case err != nil:
		return ""
	case k.Proc.P_stat == darwinSStop:
		return "T"
	default:
		return "R"
	}
}
