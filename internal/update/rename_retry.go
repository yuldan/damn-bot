package update

import "time"

// renameWithRetry retries a transient rename failure on Windows (a running or
// freshly spawned binary may still hold the image when `update --repair`
// restores .prev; Windows reports ERROR_SHARING_VIOLATION). Other platforms
// rename once.
func retryRename(attempts int, delay time.Duration, op func() error, retryable func(error) bool) error {
	var err error
	for i := range attempts {
		if err = op(); err == nil || !retryable(err) {
			return err
		}
		if i+1 < attempts {
			timer := time.NewTimer(delay)
			<-timer.C
		}
	}
	return err
}
