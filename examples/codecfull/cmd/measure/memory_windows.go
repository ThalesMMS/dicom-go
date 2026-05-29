//go:build codecfull && windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const (
	processTreeSampleInterval = time.Millisecond

	th32csSnapProcess       = 0x00000002
	processQueryInformation = 0x0400
	processVMRead           = 0x0010
	maxPath                 = 260

	errorInvalidHandle    = syscall.Errno(6)
	errorInvalidParameter = syscall.Errno(87)
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	psapi                        = syscall.NewLazyDLL("psapi.dll")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
	procGetProcessMemoryInfo     = psapi.NewProc("GetProcessMemoryInfo")
)

type processEntry struct {
	pid       uint32
	parentPID uint32
}

type processEntry32 struct {
	size              uint32
	usage             uint32
	processID         uint32
	defaultHeapID     uintptr
	moduleID          uint32
	threads           uint32
	parentProcessID   uint32
	basePriorityClass int32
	flags             uint32
	executableFile    [maxPath]uint16
}

type processMemoryCounters struct {
	size                       uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

type processTreeSampler struct {
	rootPID  uint32
	interval time.Duration
	done     chan struct{}
	result   chan processTreeSampleResult
}

type processTreeSampleResult struct {
	peak uint64
	err  error
}

func validateProcessTreeMemoryMeasurement() error {
	_, err := sampleProcessTreeWorkingSet(uint32(syscall.Getpid()))
	return err
}

func startProcessTreeSampler(rootPID int, interval time.Duration) (*processTreeSampler, error) {
	if rootPID <= 0 {
		return nil, fmt.Errorf("invalid root process ID %d", rootPID)
	}
	if interval <= 0 {
		return nil, fmt.Errorf("invalid sampling interval %s", interval)
	}
	initial, err := sampleProcessTreeWorkingSet(uint32(rootPID))
	if err != nil {
		return nil, err
	}
	sampler := &processTreeSampler{
		rootPID:  uint32(rootPID),
		interval: interval,
		done:     make(chan struct{}),
		result:   make(chan processTreeSampleResult, 1),
	}
	go sampler.sample(initial)
	return sampler, nil
}

func (s *processTreeSampler) sample(initial uint64) {
	peak := initial
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			current, err := sampleProcessTreeWorkingSet(s.rootPID)
			if err != nil {
				s.result <- processTreeSampleResult{peak: peak, err: err}
				return
			}
			peak = max(peak, current)
		case <-s.done:
			current, err := sampleProcessTreeWorkingSet(s.rootPID)
			s.result <- processTreeSampleResult{peak: max(peak, current), err: err}
			return
		}
	}
}

func (s *processTreeSampler) Stop() (uint64, error) {
	close(s.done)
	result := <-s.result
	return result.peak, result.err
}

func sampleProcessTreeWorkingSet(rootPID uint32) (uint64, error) {
	entries, err := snapshotProcesses()
	if err != nil {
		return 0, err
	}
	pids, ok := processTreePIDs(entries, rootPID)
	if !ok {
		return 0, fmt.Errorf("root process %d is absent from process snapshot", rootPID)
	}
	var total uint64
	for _, pid := range pids {
		workingSet, err := processWorkingSet(pid)
		if err != nil {
			if pid != rootPID && processNoLongerExists(err) {
				continue
			}
			return 0, fmt.Errorf("query working set for process %d: %w", pid, err)
		}
		total += workingSet
	}
	return total, nil
}

func snapshotProcesses() ([]processEntry, error) {
	handle, _, callErr := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if handle == ^uintptr(0) {
		return nil, windowsCallError("CreateToolhelp32Snapshot", callErr)
	}
	defer procCloseHandle.Call(handle)

	var raw processEntry32
	raw.size = uint32(unsafe.Sizeof(raw))
	ok, _, callErr := procProcess32FirstW.Call(handle, uintptr(unsafe.Pointer(&raw)))
	if ok == 0 {
		return nil, windowsCallError("Process32FirstW", callErr)
	}
	entries := make([]processEntry, 0, 256)
	for {
		entries = append(entries, processEntry{
			pid:       raw.processID,
			parentPID: raw.parentProcessID,
		})
		ok, _, callErr = procProcess32NextW.Call(handle, uintptr(unsafe.Pointer(&raw)))
		if ok != 0 {
			continue
		}
		if errors.Is(callErr, syscall.ERROR_NO_MORE_FILES) {
			break
		}
		return nil, windowsCallError("Process32NextW", callErr)
	}
	return entries, nil
}

func processTreePIDs(entries []processEntry, rootPID uint32) ([]uint32, bool) {
	children := make(map[uint32][]uint32, len(entries))
	foundRoot := false
	for _, entry := range entries {
		if entry.pid == rootPID {
			foundRoot = true
		}
		if entry.pid != entry.parentPID {
			children[entry.parentPID] = append(children[entry.parentPID], entry.pid)
		}
	}
	if !foundRoot {
		return nil, false
	}
	pids := []uint32{rootPID}
	visited := map[uint32]bool{rootPID: true}
	for index := 0; index < len(pids); index++ {
		for _, childPID := range children[pids[index]] {
			if visited[childPID] {
				continue
			}
			visited[childPID] = true
			pids = append(pids, childPID)
		}
	}
	return pids, true
}

func processWorkingSet(pid uint32) (uint64, error) {
	handle, _, callErr := procOpenProcess.Call(
		processQueryInformation|processVMRead,
		0,
		uintptr(pid),
	)
	if handle == 0 {
		return 0, windowsCallError("OpenProcess", callErr)
	}
	defer procCloseHandle.Call(handle)

	var counters processMemoryCounters
	counters.size = uint32(unsafe.Sizeof(counters))
	ok, _, callErr := procGetProcessMemoryInfo.Call(
		handle,
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.size),
	)
	if ok == 0 {
		return 0, windowsCallError("GetProcessMemoryInfo", callErr)
	}
	return uint64(counters.workingSetSize), nil
}

func processNoLongerExists(err error) bool {
	return errors.Is(err, errorInvalidParameter) ||
		errors.Is(err, errorInvalidHandle)
}

func windowsCallError(name string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("%s failed", name)
	}
	return fmt.Errorf("%s: %w", name, err)
}
