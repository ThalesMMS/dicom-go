//go:build codecfull && windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestProcessTreePIDsIncludesNestedDescendants(t *testing.T) {
	entries := []processEntry{
		{pid: 10, parentPID: 1},
		{pid: 11, parentPID: 10},
		{pid: 12, parentPID: 11},
		{pid: 13, parentPID: 10},
		{pid: 20, parentPID: 1},
	}
	got, ok := processTreePIDs(entries, 10)
	if !ok {
		t.Fatal("root was not found")
	}
	want := map[uint32]bool{10: true, 11: true, 12: true, 13: true}
	if len(got) != len(want) {
		t.Fatalf("process tree = %v, want %v", got, want)
	}
	for _, pid := range got {
		if !want[pid] {
			t.Fatalf("process tree contains unrelated PID %d: %v", pid, got)
		}
	}
}

func TestProcessTreePIDsReportsMissingRoot(t *testing.T) {
	if got, ok := processTreePIDs([]processEntry{{pid: 11, parentPID: 10}}, 10); ok || got != nil {
		t.Fatalf("missing root returned (%v, %t)", got, ok)
	}
}

func TestProcessTreePIDsTerminatesOnParentCycle(t *testing.T) {
	entries := []processEntry{
		{pid: 10, parentPID: 11},
		{pid: 11, parentPID: 10},
		{pid: 12, parentPID: 11},
	}
	got, ok := processTreePIDs(entries, 10)
	if !ok {
		t.Fatal("root was not found")
	}
	want := map[uint32]bool{10: true, 11: true, 12: true}
	if len(got) != len(want) {
		t.Fatalf("process tree = %v, want each PID exactly once", got)
	}
	for _, pid := range got {
		if !want[pid] {
			t.Fatalf("process tree contains unexpected PID %d: %v", pid, got)
		}
		delete(want, pid)
	}
	if len(want) != 0 {
		t.Fatalf("process tree omitted PIDs: %v", want)
	}
}

func TestSampleProcessTreeWorkingSetCurrentProcess(t *testing.T) {
	got, err := sampleProcessTreeWorkingSet(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if got == 0 {
		t.Fatal("current process working set is zero")
	}
}

func TestProcessTreeSamplerIncludesChildWorkingSet(t *testing.T) {
	baseline, err := sampleProcessTreeWorkingSet(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	sampler, err := startProcessTreeSampler(os.Getpid(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	samplerStopped := false
	defer func() {
		if !samplerStopped {
			_, _ = sampler.Stop()
		}
	}()

	command := exec.Command(os.Args[0], "-test.run=^TestProcessTreeSamplerHelper$")
	command.Env = append(os.Environ(), "CODECFULL_MEMORY_SAMPLER_CHILD=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	processWaited := false
	defer func() {
		if !processWaited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		_ = stdout.Close()
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read helper readiness: %v", err)
	}
	if line != "ready\n" {
		t.Fatalf("helper output = %q, want readiness marker", line)
	}
	childWorkingSet, err := processWorkingSet(uint32(command.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	peak, err := sampler.Stop()
	samplerStopped = true
	if err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	processWaited = true
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	if peak < baseline+childWorkingSet/2 {
		t.Fatalf("peak process-tree working set = %d; baseline = %d, child = %d", peak, baseline, childWorkingSet)
	}
}

func TestProcessTreeSamplerHelper(t *testing.T) {
	if os.Getenv("CODECFULL_MEMORY_SAMPLER_CHILD") != "1" {
		return
	}
	allocation := make([]byte, 32<<20)
	for index := 0; index < len(allocation); index += 4096 {
		allocation[index] = byte(index)
	}
	fmt.Println("ready")
	time.Sleep(100 * time.Millisecond)
	runtime.KeepAlive(allocation)
}
