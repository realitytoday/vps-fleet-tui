package vps

import (
	"fmt"
	"strconv"
	"strings"
)

// Process is one row of a top-like process list.
type Process struct {
	PID     int
	User    string
	CPU     float64 // percent of one core, like top
	Mem     float64 // percent of RAM
	RES     string  // resident memory, as reported (e.g. "30.5m", "812")
	Time    string  // CPU time, e.g. "1:23.45"
	Command string
}

// ProcessList is a snapshot of the busiest processes.
type ProcessList struct {
	Summary string // e.g. "123 total, 1 running, 122 sleeping"
	Procs   []Process
}

// topCmd prints only top's second frame: the first frame's %CPU is averaged since
// the process started, the second covers the 0.5s between the two frames.
const topCmd = `top -b -n 2 -d 0.5 -c -w 512 -o %s 2>/dev/null | awk '/^top -/{n++} n==2'`

// psCmd is the fallback where procps top is missing (e.g. busybox). Its %CPU is a
// lifetime average, so it's less accurate.
const psCmd = `ps -eo pid=,user=,pcpu=,pmem=,rss=,time=,args= --sort=-%s 2>/dev/null | head -n 200`

// FetchProcesses returns the busiest processes, sorted by CPU or by memory.
func (c *Client) FetchProcesses(byMem bool) (*ProcessList, error) {
	topKey, psKey := "%CPU", "pcpu"
	if byMem {
		topKey, psKey = "%MEM", "pmem"
	}
	out, err := c.Run(fmt.Sprintf(topCmd, topKey))
	if list := parseTop(out); len(list.Procs) > 0 {
		return list, nil
	}
	out, err2 := c.Run(fmt.Sprintf(psCmd, psKey))
	if list := parsePS(out); len(list.Procs) > 0 {
		return list, nil
	}
	if err2 != nil {
		return nil, err2
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no process list (top and ps unavailable)")
}

// parseTop parses one frame of `top -b`:
//
//	Tasks: 123 total,   1 running, 122 sleeping, ...
//	    PID USER      PR  NI    VIRT    RES    SHR S  %CPU  %MEM     TIME+ COMMAND
//	   1234 root      20   0  123456  30512   9876 S  45.2   3.1   1:23.45 /usr/bin/python3 x.py
func parseTop(out string) *ProcessList {
	list := &ProcessList{}
	inTable := false
	for _, line := range strings.Split(out, "\n") {
		if s, ok := strings.CutPrefix(line, "Tasks:"); ok {
			list.Summary = strings.Join(strings.Fields(s), " ")
			continue
		}
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "PID" {
			inTable = true
			continue
		}
		if !inTable || len(f) < 12 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(strings.ReplaceAll(f[8], ",", "."), 64)
		mem, _ := strconv.ParseFloat(strings.ReplaceAll(f[9], ",", "."), 64)
		list.Procs = append(list.Procs, Process{
			PID: pid, User: f[1], CPU: cpu, Mem: mem, RES: f[5], Time: f[10],
			Command: strings.Join(f[11:], " "),
		})
	}
	return list
}

// parsePS parses the psCmd output (pid user pcpu pmem rss time args...).
func parsePS(out string) *ProcessList {
	list := &ProcessList{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[2], 64)
		mem, _ := strconv.ParseFloat(f[3], 64)
		rssKB, _ := strconv.ParseFloat(f[4], 64)
		list.Procs = append(list.Procs, Process{
			PID: pid, User: f[1], CPU: cpu, Mem: mem, RES: compactKB(rssKB), Time: f[5],
			Command: strings.Join(f[6:], " "),
		})
	}
	list.Summary = fmt.Sprintf("%d total", len(list.Procs))
	return list
}

// compactKB formats KiB like top does: "812", "30.5m", "1.2g".
func compactKB(kb float64) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1fg", kb/1024/1024)
	case kb >= 10*1024:
		return fmt.Sprintf("%.1fm", kb/1024)
	}
	return fmt.Sprintf("%.0f", kb)
}
