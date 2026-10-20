package exec

// SweepStale kills the process trees of dead damnbot owners (§A.4). On
// Windows every tree is a Job Object with KILL_ON_JOB_CLOSE, which the
// kernel terminates when its owner dies, so nothing can be left.
func SweepStale() (int, error) { return 0, nil }
