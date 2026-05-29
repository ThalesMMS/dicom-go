//go:build codecfull && !windows

package main

import (
	"fmt"
	"runtime"
	"time"
)

const processTreeSampleInterval = time.Millisecond

type processTreeSampler struct{}

func validateProcessTreeMemoryMeasurement() error {
	return fmt.Errorf("process-tree working-set measurement is not implemented on %s", runtime.GOOS)
}

func startProcessTreeSampler(rootPID int, interval time.Duration) (*processTreeSampler, error) {
	return nil, fmt.Errorf("process-tree working-set measurement is not implemented on %s", runtime.GOOS)
}

func (s *processTreeSampler) Stop() (uint64, error) {
	return 0, fmt.Errorf("process-tree working-set measurement is not implemented on %s", runtime.GOOS)
}
