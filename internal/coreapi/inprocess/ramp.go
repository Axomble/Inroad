package inprocess

// effectiveCap returns today's allowed daily send count for a mailbox given its
// ramp schedule and age in days. Linear from startCap to dailyCap over rampDays.
func effectiveCap(dailyCap, startCap, rampDays int, rampEnabled bool, ageDays int) int {
	if !rampEnabled || ageDays >= rampDays || rampDays <= 0 {
		return dailyCap
	}
	if ageDays <= 0 {
		return startCap
	}
	return startCap + (dailyCap-startCap)*ageDays/rampDays
}
