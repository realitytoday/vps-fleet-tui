package vps

import (
	"strconv"
	"strings"
)

type Metrics struct {
	CPUUsage  float64
	MemUsed   uint64
	MemTotal  uint64
	DiskUsed  uint64
	DiskTotal uint64
	Uptime    string
	OS        string

	Load1, Load5, Load15 float64
	Cores                int
	FailedUnits          int  // failed systemd units
	RebootRequired       bool // /var/run/reboot-required exists (Debian/Ubuntu)

	// I/O rates in bytes per second, measured over the same 0.5s window as CPU.
	// HasIO is false if the counters couldn't be read.
	DiskReadBps, DiskWriteBps float64
	NetRxBps, NetTxBps        float64
	HasIO                     bool
}

// metricsCmd prints one prefixed line per metric, so parsing is robust regardless of
// line order or extra output.
//   - CPU: two /proc/stat samples 0.5s apart (top's first frame is unreliable)
//   - df -Pk: POSIX output in 1K blocks (used and size counted from the right, in case
//     the filesystem name has spaces); works with GNU and busybox df
//   - os-release is sourced in a subshell so its variables don't leak
//   - disk I/O: sectors read/written of whole disks only (no partitions, loop, dm-*),
//     so nothing is counted twice; net I/O: all interfaces except loopback and
//     container/VM bridges. Both are sampled around the CPU sleep, with /proc/uptime
//     as the clock.
const metricsCmd = `echo "U:$(uptime -p)"
free -b | awk '/Mem:/ {print "M:" $3 "," $2}'
df -Pk / | tail -1 | awk '{printf "D:%.0f,%.0f\n", $(NF-3)*1024, $(NF-4)*1024}'
dio() { awk '$3 ~ /^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme[0-9]+n[0-9]+|mmcblk[0-9]+)$/ {r+=$6; w+=$10} END {printf "%.0f,%.0f", r*512, w*512}' /proc/diskstats 2>/dev/null; }
nio() { awk 'NR>2 {sub(/^ +/, ""); split($0, f, /[: ]+/); if (f[1] !~ /^(lo|veth|docker|br-|virbr)/) {r+=f[2]; t+=f[10]}} END {printf "%.0f,%.0f", r, t}' /proc/net/dev 2>/dev/null; }
echo "S1:$(head -1 /proc/stat)"; echo "IO1:$(dio)"; echo "NET1:$(nio)"; echo "T1:$(cut -d' ' -f1 /proc/uptime)"
sleep 0.5
echo "S2:$(head -1 /proc/stat)"; echo "IO2:$(dio)"; echo "NET2:$(nio)"; echo "T2:$(cut -d' ' -f1 /proc/uptime)"
echo "L:$(cut -d' ' -f1-3 /proc/loadavg)"
echo "N:$(nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo)"
echo "F:$(systemctl --failed --no-legend --plain 2>/dev/null | grep -c .)"
[ -f /var/run/reboot-required ] && echo "R:1"
echo "O:$( (. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME") || echo Unknown)"`

func (c *Client) FetchMetrics() (*Metrics, error) {
	output, err := c.Run(metricsCmd)
	if err != nil && output == "" {
		return nil, err
	}

	m := &Metrics{OS: "Unknown", Uptime: "Unknown"}
	var stat1, stat2 string
	var io1, io2, net1, net2 string
	var t1, t2 float64
	for _, line := range strings.Split(output, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}

		switch key {
		case "U":
			m.Uptime = strings.TrimPrefix(val, "up ")
		case "M":
			m.MemUsed, m.MemTotal = parsePair(val)
		case "D":
			m.DiskUsed, m.DiskTotal = parsePair(val)
		case "S1":
			stat1 = val
		case "S2":
			stat2 = val
		case "IO1":
			io1 = val
		case "IO2":
			io2 = val
		case "NET1":
			net1 = val
		case "NET2":
			net2 = val
		case "T1":
			t1, _ = strconv.ParseFloat(strings.TrimSpace(val), 64)
		case "T2":
			t2, _ = strconv.ParseFloat(strings.TrimSpace(val), 64)
		case "L":
			if f := strings.Fields(val); len(f) == 3 {
				m.Load1, _ = strconv.ParseFloat(f[0], 64)
				m.Load5, _ = strconv.ParseFloat(f[1], 64)
				m.Load15, _ = strconv.ParseFloat(f[2], 64)
			}
		case "N":
			m.Cores, _ = strconv.Atoi(strings.TrimSpace(val))
		case "F":
			m.FailedUnits, _ = strconv.Atoi(strings.TrimSpace(val))
		case "R":
			m.RebootRequired = true
		case "O":
			m.OS = val
		}
	}
	m.CPUUsage = cpuUsage(stat1, stat2)

	// /proc/uptime has 10ms resolution; fall back to the nominal sleep if it's missing
	elapsed := t2 - t1
	if elapsed <= 0 {
		elapsed = 0.5
	}
	var okDisk, okNet bool
	m.DiskReadBps, m.DiskWriteBps, okDisk = rates(io1, io2, elapsed)
	m.NetRxBps, m.NetTxBps, okNet = rates(net1, net2, elapsed)
	m.HasIO = okDisk && okNet

	return m, nil
}

// rates turns two "a,b" byte counter samples into per-second rates.
func rates(s1, s2 string, seconds float64) (float64, float64, bool) {
	if s1 == "" || s2 == "" {
		return 0, 0, false
	}
	a1, b1 := parsePair(s1)
	a2, b2 := parsePair(s2)
	// counters can wrap or reset (e.g. an interface going away): report 0, not a huge value
	rate := func(x1, x2 uint64) float64 {
		if x2 < x1 {
			return 0
		}
		return float64(x2-x1) / seconds
	}
	return rate(a1, a2), rate(b1, b2), true
}

func parsePair(val string) (uint64, uint64) {
	a, b, ok := strings.Cut(val, ",")
	if !ok {
		return 0, 0
	}
	x, _ := strconv.ParseUint(strings.TrimSpace(a), 10, 64)
	y, _ := strconv.ParseUint(strings.TrimSpace(b), 10, 64)
	return x, y
}

// cpuUsage computes the busy percentage between two "cpu  user nice system idle iowait ..." lines.
func cpuUsage(s1, s2 string) float64 {
	idle1, total1, ok1 := parseCPUStat(s1)
	idle2, total2, ok2 := parseCPUStat(s2)
	if !ok1 || !ok2 || total2 <= total1 {
		return 0
	}
	busy := 1 - float64(idle2-idle1)/float64(total2-total1)
	return max(0, min(100, busy*100))
}

func parseCPUStat(s string) (idle, total uint64, ok bool) {
	f := strings.Fields(s)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	for i, v := range f[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		// guest time (fields 9, 10) is already included in user/nice
		if i < 8 {
			total += n
		}
		if i == 3 || i == 4 { // idle, iowait
			idle += n
		}
	}
	return idle, total, true
}

// updatesCmd counts pending package updates without touching the network
// (apt simulates, dnf uses its cache only). Prints -1 if no supported package manager.
const updatesCmd = `if command -v apt-get >/dev/null 2>&1; then apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep -c '^Inst'
elif command -v dnf >/dev/null 2>&1; then dnf -C -q check-update 2>/dev/null | grep -c '^[[:alnum:]]'
elif command -v apk >/dev/null 2>&1; then apk -u list 2>/dev/null | grep -c .
else echo -1; fi`

// FetchUpdates returns the number of pending package updates, or -1 if unknown.
// It is slower than FetchMetrics, so callers should run it rarely.
func (c *Client) FetchUpdates() (int, error) {
	out, err := c.Run(updatesCmd)
	// grep -c exits 1 when the count is 0, so only fail when there is no output
	n, perr := strconv.Atoi(strings.TrimSpace(out))
	if perr != nil {
		if err != nil {
			return -1, err
		}
		return -1, perr
	}
	return n, nil
}
