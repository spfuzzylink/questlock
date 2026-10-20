package admission

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func linuxFixture() map[string]string {
	psi := "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
	return map[string]string{"/proc/meminfo": "MemTotal: 4194304 kB\nMemAvailable: 2097152 kB\n", "/proc/loadavg": "0.50 0.25 0.10 1/100 123\n", "/proc/self/cgroup": "0::/parent/leaf\n", "/proc/self/mountinfo": "36 25 0:32 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n", "/proc/pressure/cpu": psi, "/proc/pressure/memory": psi, "/sys/fs/cgroup/parent/leaf/cpu.pressure": psi, "/sys/fs/cgroup/parent/leaf/memory.pressure": psi, "/sys/fs/cgroup/parent/leaf/cpu.max": "200000 100000\n", "/sys/fs/cgroup/parent/cpu.max": "150000 100000\n", "/sys/fs/cgroup/parent/leaf/cpuset.cpus.effective": "2-3,7\n", "/sys/fs/cgroup/parent/leaf/memory.max": "2147483648\n", "/sys/fs/cgroup/parent/leaf/memory.current": "1073741824\n", "/sys/fs/cgroup/parent/memory.high": "1073741824\n", "/sys/fs/cgroup/parent/memory.current": "805306368\n"}
}
func readFixture(files map[string]string) fileReader {
	if _, ok := files["/proc/stat"]; !ok {
		var stat strings.Builder
		for i := 0; i < 8; i++ {
			fmt.Fprintf(&stat, "cpu%d 0 0 0 0\n", i)
		}
		files["/proc/stat"] = stat.String()
	}
	return func(p string) ([]byte, error) {
		v, ok := files[p]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(v), nil
	}
}

func TestHostLoadDenominatorIsIndependentFromEffectiveCPUs(t *testing.T) {
	f := linuxFixture()
	var stat strings.Builder
	for i := 0; i < 64; i++ {
		fmt.Fprintf(&stat, "cpu%d 0 0 0 0\n", i)
	}
	f["/proc/stat"] = stat.String()
	f["/proc/loadavg"] = "8.0 8.0 8.0 1/100 1"
	f["/sys/fs/cgroup/parent/cpu.max"] = "max 100000"
	s, e := sampleLinux(readFixture(f), 2, time.Now())
	if e != nil || s.CPUCapacityMilli != 2000 || s.LoadCapacityMilli != 64000 {
		t.Fatal(s, e)
	}
	c := testConfig()
	scheduler, e := New(c, func(context.Context, Job) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	scheduler.Update(s)
	if scheduler.Status().Reason == "resource_pressure" {
		t.Fatal("host load falsely overloaded narrow affinity")
	}
	f["/sys/fs/cgroup/parent/leaf/cpu.pressure"] = "some avg10=30.00 avg60=0 avg300=0 total=1\nfull avg10=0 avg60=0 avg300=0 total=0\n"
	s, e = sampleLinux(readFixture(f), 2, time.Now())
	if e != nil || s.CPUStallMilliPercent != 30000 {
		t.Fatal(s, e)
	}
	scheduler.Update(s)
	if scheduler.Status().Reason != "resource_pressure" {
		t.Fatal("cgroup stalls ignored")
	}
}

func TestLinuxEffectiveAncestorQuotasMemoryAndPressure(t *testing.T) {
	f := linuxFixture()
	s, e := sampleLinux(readFixture(f), 8, time.Now())
	if e != nil || !s.Known || s.CPUCapacityMilli != 1500 || s.AvailableMemoryBytes != 256*MiB {
		t.Fatal(s, e)
	}
	f["/sys/fs/cgroup/parent/leaf/cpu.max"] = "25000 100000"
	f["/sys/fs/cgroup/parent/leaf/memory.current"] = "3000000000"
	f["/proc/pressure/memory"] = "some avg10=2.50 avg60=1.00 avg300=0.00 total=42\nfull avg10=1.00 avg60=0.00 avg300=0.00 total=10\n"
	s, e = sampleLinux(readFixture(f), 8, time.Now())
	if e != nil || s.CPUCapacityMilli != 250 || s.AvailableMemoryBytes != 0 || s.MemoryStallMilliPercent != 2500 {
		t.Fatal(s, e)
	}
}

func TestLinuxUnavailableMalformedAndNamespaceFixtures(t *testing.T) {
	for name, value := range map[string]string{"/proc/self/cgroup": "2:cpu:/legacy\n", "/sys/fs/cgroup/parent/cpu.max": "NaN 100000", "/sys/fs/cgroup/parent/leaf/cpuset.cpus.effective": "1-3,2", "/sys/fs/cgroup/parent/memory.high": "9223372036854775808", "/proc/pressure/memory": "some avg10=NaN avg60=0 avg300=0 total=0\nfull avg10=0 avg60=0 avg300=0 total=0", "/proc/meminfo": "MemAvailable: 9223372036854775807 kB"} {
		f := linuxFixture()
		f[name] = value
		s, e := sampleLinux(readFixture(f), 8, time.Now())
		if e == nil || s.Known {
			t.Fatal("unsafe fixture accepted", name, s)
		}
	}
	f := linuxFixture()
	delete(f, "/sys/fs/cgroup/parent/leaf/memory.pressure")
	if _, e := sampleLinux(readFixture(f), 8, time.Now()); e == nil {
		t.Fatal("missing pressure guessed normal")
	}
	f = linuxFixture()
	f["/proc/self/mountinfo"] = "36 25 0:32 /parent /sys/fs/cgroup rw - cgroup2 cgroup rw\n"
	f["/proc/self/cgroup"] = "0::/parent/leaf\n"
	for k, v := range f {
		if strings.HasPrefix(k, "/sys/fs/cgroup/parent/") {
			f[strings.Replace(k, "/sys/fs/cgroup/parent/", "/sys/fs/cgroup/", 1)] = v
		}
	}
	s, e := sampleLinux(readFixture(f), 8, time.Now())
	if e != nil || !s.Known || s.CPUCapacityMilli != 1500 {
		t.Fatal(s, e)
	}
}

func TestBoundedCPUQuotaCPUSetAndPSINumbers(t *testing.T) {
	for _, s := range []string{"max 0", "-1 100000", "1 9223372036854775807", "1 100000 extra", "+1 1000"} {
		if _, e := parseCPUQuota([]byte(s), 8000); e == nil {
			t.Fatal(s)
		}
	}
	if n, e := parseCPUQuota([]byte("9223372036854775807 100000"), 8000); e != nil || n != 8000 {
		t.Fatal(n, e)
	}
	for _, s := range []string{"", "0-1000001", "2-1", "1,1", "0-3,2-4", "+1"} {
		if _, e := parseCPUSet([]byte(s)); e == nil {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"NaN", "Inf", "-1", "101"} {
		if _, e := parsePSI([]byte("some avg10="+s+" avg60=0 avg300=0 total=0"), false); e == nil {
			t.Fatal(s)
		}
	}
}

func TestDarwinNativeSignalFixtures(t *testing.T) {
	sys := []byte("8\n17179869184\n1\n{ 1.25 1.0 0.75 }\n")
	vm := []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10000.\nPages speculative: 3000.\nPages purgeable: 2000.\nPages inactive: 999999.\n")
	s, e := parseDarwin(sys, vm, time.Now())
	if e != nil || !s.Known || s.CPUCapacityMilli != 8000 || s.LoadMilli != 1250 || s.AvailableMemoryBytes != 15000*16384 || s.MemoryPressure != Normal {
		t.Fatal(s, e)
	}
	s, e = parseDarwin([]byte(strings.Replace(string(sys), "\n1\n", "\n4\n", 1)), vm, time.Now())
	if e != nil || s.MemoryPressure != Critical {
		t.Fatal(s, e)
	}
	for _, bad := range [][]byte{[]byte(strings.Replace(string(sys), "1.25", "NaN", 1)), []byte(strings.Replace(string(sys), "\n1\n", "\n3\n", 1)), []byte("8\n17179869184\n1\n")} {
		if _, e := parseDarwin(bad, vm, time.Now()); e == nil {
			t.Fatal("invalid macOS fixture accepted")
		}
	}
	if _, e := parseDarwin(sys, []byte(strings.Replace(string(vm), "10000.", "9223372036854775807.", 1)), time.Now()); e == nil {
		t.Fatal("page arithmetic overflow accepted")
	}
}

func TestDarwinCountsPrintedSpeculativeQueueOnce(t *testing.T) {
	sys := []byte("8\n17179869184\n1\n{ 1.25 1.0 0.75 }\n")
	vm := []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10000.\nPages speculative: 3000.\nPages purgeable: 2000.\nPages inactive: 999999.\nFile-backed pages: 999999.\nPages active: 999999.\nPages occupied by compressor: 999999.\n")
	s, err := parseDarwin(sys, vm, time.Now())
	if err != nil || !s.Known || s.AvailableMemoryBytes != 15000*16384 {
		t.Fatal(s, err)
	}
	if s.Source != "darwin_sysctl_vm_stat_free_speculative_purgeable" {
		t.Fatal("memory-accounting source is not explicit", s.Source)
	}
	// File-backed and inactive totals overlap VM queues. Increasing them must
	// not create additional capacity or add speculative pages a second time.
	vm = []byte(strings.ReplaceAll(string(vm), "999999.", "9223372036854775807."))
	unchanged, err := parseDarwin(sys, vm, time.Now())
	if err != nil || unchanged.AvailableMemoryBytes != s.AvailableMemoryBytes {
		t.Fatal(unchanged, err)
	}
}

func TestDarwinMemoryCountersFailClosed(t *testing.T) {
	sys := []byte("8\n17179869184\n1\n{ 1.25 1.0 0.75 }\n")
	vm := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10000.\nPages speculative: 3000.\nPages purgeable: 2000.\n"
	for name, bad := range map[string]string{
		"missing speculative":   strings.Replace(vm, "Pages speculative: 3000.\n", "", 1),
		"duplicate speculative": vm + "Pages speculative: 3000.\n",
		"duplicate free":        vm + "Pages free: 10000.\n",
		"duplicate purgeable":   vm + "Pages purgeable: 2000.\n",
		"negative speculative":  strings.Replace(vm, "3000.", "-1.", 1),
		"unknown speculative":   strings.Replace(vm, "3000.", "unknown.", 1),
		"unsigned overflow":     strings.Replace(vm, "3000.", "9223372036854775808.", 1),
		"sum overflow":          strings.Replace(vm, "3000.", "9223372036854775807.", 1),
		"beyond physical RAM":   strings.Replace(vm, "3000.", "1048576.", 1),
		"missing terminator":    strings.Replace(vm, "3000.", "3000", 1),
	} {
		t.Run(name, func(t *testing.T) {
			s, err := parseDarwin(sys, []byte(bad), time.Now())
			if err == nil || s.Known {
				t.Fatal("unsafe macOS memory signal accepted", s, err)
			}
		})
	}
}

func TestDarwinReclaimableFreeDoesNotOverridePressure(t *testing.T) {
	vm := []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10000.\nPages speculative: 500000.\nPages purgeable: 2000.\n")
	for _, level := range []string{"2", "4"} {
		s, err := parseDarwin([]byte("8\n17179869184\n"+level+"\n{ 1.25 1.0 0.75 }\n"), vm, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		c, err := New(DefaultConfig(), func(context.Context, Job) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		c.Update(s)
		if c.Status().ExpensiveWorkerLimit != 0 || c.Status().Reason != "resource_pressure" {
			t.Fatal("speculative capacity bypassed kernel pressure", c.Status())
		}
	}
}

func TestDarwinSpeculativeCapacityFitsUnchangedInferenceReservation(t *testing.T) {
	for _, speculative := range []string{"0", "8000"} {
		t.Run(speculative, func(t *testing.T) {
			now := time.Now()
			cfg := DefaultConfig()
			c, err := New(cfg, func(context.Context, Job) error {
				t.Fatal("capacity test must not execute an inference job")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			c.now = func() time.Time { return now }
			vm := []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 16000.\nPages speculative: " + speculative + ".\nPages purgeable: 16000.\n")
			for i := 0; i < cfg.RecoverySamples; i++ {
				now = now.Add(time.Second)
				s, err := parseDarwin([]byte("8\n17179869184\n1\n{ 1.25 1.0 0.75 }\n"), vm, now)
				if err != nil {
					t.Fatal(err)
				}
				c.Update(s)
			}
			_, err = c.Submit(context.Background(), Job{"one", LaneInference, nil})
			if speculative == "0" && !errors.Is(err, ErrDoesNotFit) {
				t.Fatal("insufficient capacity admitted", err, c.Status())
			}
			if speculative != "0" && err != nil {
				t.Fatal("distinct free queue was not usable", err, c.Status())
			}
		})
	}
}

func TestSystemSourceCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, e := SystemSource().Sample(ctx)
	if !errors.Is(e, context.Canceled) || s.Known {
		t.Fatal(s, e)
	}
}
