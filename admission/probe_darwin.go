package admission

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"
)

type probeBuffer struct{ bytes.Buffer }

func (b *probeBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, ErrSignals
	}
	return b.Buffer.Write(p)
}
func sampleSystem(ctx context.Context) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	run := func(name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = []string{"LC_ALL=C", "LANG=C"}
		var out probeBuffer
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		cmd.WaitDelay = time.Second
		if cmd.Run() != nil {
			return nil, ErrSignals
		}
		return out.Bytes(), nil
	}
	sys, err := run("/usr/sbin/sysctl", "-n", "hw.logicalcpu", "hw.memsize", "kern.memorystatus_vm_pressure_level", "vm.loadavg")
	if err != nil {
		return Snapshot{At: time.Now(), Source: "darwin_sysctl_vm_stat"}, err
	}
	vm, err := run("/usr/bin/vm_stat")
	if err != nil {
		return Snapshot{At: time.Now(), Source: "darwin_sysctl_vm_stat"}, err
	}
	return parseDarwin(sys, vm, time.Now())
}
