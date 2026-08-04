//go:build linux

package sandbox

import (
	"os"
	"strconv"
	"strings"
)

// processStartToken returns a value that distinguishes a process from a
// later one that inherits its pid: the start time in /proc/<pid>/stat,
// counted in clock ticks since boot. Two processes with the same pid cannot
// share it, because the second one started after the first exited.
//
// Field 22 of that line is the start time. The comm field before it may
// contain spaces and parentheses, so the fields are counted from the last
// ')' rather than by splitting the whole line — a process named
// "(evil) 1 2 3" would otherwise shift every index after it.
func processStartToken(pid int) (string, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return "", false
	}
	// After comm come state (field 3) and the rest; start time is field 22,
	// so it is the 20th field of what follows.
	fields := strings.Fields(string(raw)[end+1:])
	const startTimeOffset = 19
	if len(fields) <= startTimeOffset {
		return "", false
	}
	return fields[startTimeOffset], true
}
