package admission

import (
	"errors"
	"io/fs"
	"math"
	"path"
	"sort"
	"strings"
	"time"
)

type fileReader func(string) ([]byte, error)

// Pure parser shared with fixture tests on every OS. Native paths are fixed;
// mountinfo determines the process's visible cgroup-v2 hierarchy. Every visible
// ancestor limit is considered, not just the leaf's advertised quota.
func sampleLinux(read fileReader, cpus int, now time.Time) (Snapshot, error) {
	s := Snapshot{At: now, Source: "linux_host_psi", MemoryPressure: Normal}
	if cpus < 1 || cpus > 1e6 {
		return s, ErrSignals
	}
	s.CPUCapacityMilli = int64(cpus) * 1000
	stat, err := read("/proc/stat")
	if err != nil {
		return s, ErrSignals
	}
	hostCPUs, err := parseHostCPUs(stat)
	if err != nil {
		return s, err
	}
	s.LoadCapacityMilli = hostCPUs * 1000
	b, err := read("/proc/meminfo")
	if err != nil {
		return s, ErrSignals
	}
	seen := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "MemAvailable:" {
			if seen || len(f) != 3 || f[2] != "kB" {
				return s, ErrSignals
			}
			n, e := nonnegative(f[1])
			if e != nil || n > math.MaxInt64/1024 {
				return s, ErrSignals
			}
			s.AvailableMemoryBytes = n * 1024
			seen = true
		}
	}
	if !seen {
		return s, ErrSignals
	}
	load, err := read("/proc/loadavg")
	if err != nil {
		return s, ErrSignals
	}
	fields := strings.Fields(string(load))
	if len(fields) < 3 {
		return s, ErrSignals
	}
	s.LoadMilli, err = decimalMilli(fields[0], 1e9)
	if err != nil {
		return s, err
	}
	b, err = read("/proc/self/cgroup")
	if err != nil {
		return s, ErrSignals
	}
	member := ""
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "0::") {
			if member != "" {
				return s, ErrSignals
			}
			member = strings.TrimPrefix(line, "0::")
		}
	}
	if member == "" {
		return s, ErrSignals
	} // v1/unknown confinement is not guessed.
	if !strings.HasPrefix(member, "/") || path.Clean(member) != member {
		return s, ErrSignals
	}
	b, err = read("/proc/self/mountinfo")
	if err != nil {
		return s, ErrSignals
	}
	mount, root := "", ""
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		r := strings.Fields(right)
		l := strings.Fields(left)
		if len(r) < 3 || r[0] != "cgroup2" {
			continue
		}
		if len(l) < 6 || mount != "" {
			return s, ErrSignals
		}
		root, err = mountPath(l[3])
		if err != nil {
			return s, err
		}
		mount, err = mountPath(l[4])
		if err != nil {
			return s, err
		}
	}
	if mount == "" {
		return s, ErrSignals
	}
	rel := member
	if root != "/" {
		if member == "/" {
			rel = "/"
		} else if member == root {
			rel = "/"
		} else if strings.HasPrefix(member, root+"/") {
			rel = strings.TrimPrefix(member, root)
		} else {
			return s, ErrSignals
		}
	}
	leaf := path.Join(mount, rel)
	if leaf != mount && !strings.HasPrefix(leaf, mount+"/") {
		return s, ErrSignals
	}
	// PSI at the leaf includes its descendants. Host PSI is also checked so a
	// quiet cgroup cannot hide host memory pressure. Missing PSI pauses safely.
	for _, base := range []string{"/proc/pressure", leaf} {
		cpuFile, memFile := path.Join(base, "cpu"), path.Join(base, "memory")
		if base == leaf {
			cpuFile = path.Join(base, "cpu.pressure")
			memFile = path.Join(base, "memory.pressure")
		}
		b, e := read(cpuFile)
		if e != nil {
			return s, ErrSignals
		}
		v, e := parsePSI(b, false)
		if e != nil {
			return s, e
		}
		s.CPUStallMilliPercent = max(s.CPUStallMilliPercent, v)
		b, e = read(memFile)
		if e != nil {
			return s, ErrSignals
		}
		v, e = parsePSI(b, true)
		if e != nil {
			return s, e
		}
		s.MemoryStallMilliPercent = max(s.MemoryStallMilliPercent, v)
	}
	for current := leaf; ; current = path.Dir(current) {
		if b, e := read(path.Join(current, "cpu.max")); e == nil {
			n, e := parseCPUQuota(b, s.CPUCapacityMilli)
			if e != nil {
				return s, e
			}
			s.CPUCapacityMilli = min(s.CPUCapacityMilli, n)
		} else if !errors.Is(e, fs.ErrNotExist) {
			return s, ErrSignals
		}
		if b, e := read(path.Join(current, "cpuset.cpus.effective")); e == nil {
			n, e := parseCPUSet(b)
			if e != nil {
				return s, e
			}
			s.CPUCapacityMilli = min(s.CPUCapacityMilli, n*1000)
		} else if !errors.Is(e, fs.ErrNotExist) {
			return s, ErrSignals
		}
		for _, name := range []string{"memory.max", "memory.high"} {
			b, e := read(path.Join(current, name))
			if errors.Is(e, fs.ErrNotExist) {
				continue
			}
			if e != nil {
				return s, ErrSignals
			}
			if strings.TrimSpace(string(b)) == "max" {
				continue
			}
			limit, e := nonnegative(string(b))
			if e != nil {
				return s, e
			}
			used, e := read(path.Join(current, "memory.current"))
			if e != nil {
				return s, ErrSignals
			}
			usage, e := nonnegative(string(used))
			if e != nil {
				return s, e
			}
			s.AvailableMemoryBytes = min(s.AvailableMemoryBytes, max(int64(0), limit-usage))
		}
		if current == mount {
			break
		}
		if current == "/" {
			return s, ErrSignals
		}
	}
	s.Source = "linux_cgroup_v2_and_host_psi"
	s.Known = true
	return s, nil
}

func parseHostCPUs(b []byte) (int64, error) {
	seen := map[int64]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == "cpu" || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		id, e := nonnegative(strings.TrimPrefix(f[0], "cpu"))
		if e != nil || id > 1e6 || len(f) < 5 || seen[id] {
			return 0, ErrSignals
		}
		seen[id] = true
	}
	if len(seen) < 1 || len(seen) > 1e6 {
		return 0, ErrSignals
	}
	return int64(len(seen)), nil
}

func mountPath(s string) (string, error) {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	s = r.Replace(s)
	if !strings.HasPrefix(s, "/") || path.Clean(s) != s || strings.ContainsAny(s, "\x00\n\r") {
		return "", ErrSignals
	}
	return s, nil
}
func parseCPUQuota(b []byte, cap int64) (int64, error) {
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return 0, ErrSignals
	}
	period, e := nonnegative(f[1])
	if e != nil || period < 1 || period > 1e9 {
		return 0, ErrSignals
	}
	if f[0] == "max" {
		return cap, nil
	}
	q, e := nonnegative(f[0])
	if e != nil || q < 1 {
		return 0, ErrSignals
	}
	whole := q / period
	if whole > cap/1000 {
		return cap, nil
	}
	n := whole*1000 + (q%period)*1000/period
	return min(cap, n), nil
}
func parseCPUSet(b []byte) (int64, error) {
	type span struct{ a, b int64 }
	spans := []span{}
	for _, f := range strings.Split(strings.TrimSpace(string(b)), ",") {
		parts := strings.Split(f, "-")
		if len(parts) < 1 || len(parts) > 2 {
			return 0, ErrSignals
		}
		a, e := nonnegative(parts[0])
		if e != nil || a > 1e6 {
			return 0, ErrSignals
		}
		z := a
		if len(parts) == 2 {
			z, e = nonnegative(parts[1])
			if e != nil || z < a || z > 1e6 {
				return 0, ErrSignals
			}
		}
		spans = append(spans, span{a, z})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].a < spans[j].a })
	var n int64
	for i, s := range spans {
		if i > 0 && s.a <= spans[i-1].b {
			return 0, ErrSignals
		}
		n += s.b - s.a + 1
	}
	if n < 1 || n > 1e6 {
		return 0, ErrSignals
	}
	return n, nil
}

func parseDarwin(sysctl, vm []byte, now time.Time) (Snapshot, error) {
	s := Snapshot{At: now, Source: "darwin_sysctl_vm_stat_conservative_free"}
	lines := strings.Split(strings.TrimSpace(string(sysctl)), "\n")
	if len(lines) != 4 {
		return s, ErrSignals
	}
	cpus, e := nonnegative(lines[0])
	if e != nil || cpus < 1 || cpus > 1e6 {
		return s, ErrSignals
	}
	s.CPUCapacityMilli = cpus * 1000
	s.LoadCapacityMilli = s.CPUCapacityMilli
	total, e := nonnegative(lines[1])
	if e != nil || total < 1 {
		return s, ErrSignals
	}
	level, e := nonnegative(lines[2])
	if e != nil {
		return s, e
	}
	switch level {
	case 1:
		s.MemoryPressure = Normal
	case 2:
		s.MemoryPressure = High
	case 4:
		s.MemoryPressure = Critical
	default:
		return s, ErrSignals
	}
	load := strings.Fields(strings.Trim(lines[3], "{} "))
	if len(load) != 3 {
		return s, ErrSignals
	}
	s.LoadMilli, e = decimalMilli(load[0], 1e9)
	if e != nil {
		return s, e
	}
	rows := strings.Split(strings.TrimSpace(string(vm)), "\n")
	if len(rows) < 2 {
		return s, ErrSignals
	}
	header := rows[0]
	prefix := "Mach Virtual Memory Statistics: (page size of "
	if !strings.HasPrefix(header, prefix) || !strings.HasSuffix(header, " bytes)") {
		return s, ErrSignals
	}
	page, e := nonnegative(strings.TrimSuffix(strings.TrimPrefix(header, prefix), " bytes)"))
	if e != nil || page < 1 || page > 1<<20 {
		return s, ErrSignals
	}
	free, purgeable := int64(-1), int64(-1)
	for _, line := range rows[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if key != "Pages free" && key != "Pages purgeable" {
			continue
		}
		n, e := nonnegative(strings.TrimSuffix(strings.TrimSpace(value), "."))
		if e != nil {
			return s, e
		}
		if key == "Pages free" {
			if free >= 0 {
				return s, ErrSignals
			}
			free = n
		} else {
			if purgeable >= 0 {
				return s, ErrSignals
			}
			purgeable = n
		}
	}
	if free < 0 || purgeable < 0 || free > math.MaxInt64-purgeable || free+purgeable > math.MaxInt64/page {
		return s, ErrSignals
	}
	s.AvailableMemoryBytes = (free + purgeable) * page
	if s.AvailableMemoryBytes > total {
		return s, ErrSignals
	}
	s.Known = true
	return s, nil
}
