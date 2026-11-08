package interfaces

import "strconv"

// GoMemoryLimit is the soft memory limit for the Go runtime (a target for
// the garbage collector, not a cap on live memory): 90 % of the
// container's memory limit, which the chart passes in bytes as
// KUBEGLANCE_MEMORY_LIMIT (downward API). Without it the garbage collector
// only looks at the live heap and lets garbage grow to twice that, past the
// container's limit when a peer copy or a snapshot write allocates fast. The
// 10 % left is for what the soft limit does not count tightly (stacks,
// fragmentation). ok is false when GOMEMLIMIT is set (the runtime already
// applied it) or no limit is known.
func GoMemoryLimit(getenv func(string) string) (limit int64, ok bool) {
	if getenv("GOMEMLIMIT") != "" {
		return 0, false
	}
	n, err := strconv.ParseInt(getenv("KUBEGLANCE_MEMORY_LIMIT"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n/10*9 + n%10*9/10, true // n * 9 / 10 without overflow
}
