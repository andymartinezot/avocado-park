package sessions

import (
	"bufio"
	"bytes"
	"os/exec"
	"strconv"
	"strings"
)

// ProcessRSS snapshots the process table once (one `ps` call) and returns a
// function giving the resident memory of a pid plus all its descendants, such
// as MCP servers and shells the agent spawned.
func ProcessRSS() func(pid int) int64 {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=").Output()
	if err != nil {
		return func(int) int64 { return 0 }
	}
	return rssFromPS(out)
}

func rssFromPS(out []byte) func(pid int) int64 {
	children := map[int][]int{}
	rssKB := map[int]int64{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		kb, err3 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		rssKB[pid] = kb
		children[ppid] = append(children[ppid], pid)
	}
	return func(root int) int64 {
		var total int64
		seen := map[int]bool{}
		stack := []int{root}
		for len(stack) > 0 {
			pid := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[pid] {
				continue
			}
			seen[pid] = true
			total += rssKB[pid]
			stack = append(stack, children[pid]...)
		}
		return total * 1024
	}
}
