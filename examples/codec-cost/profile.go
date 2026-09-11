package codeccost

import (
	"os"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
)

// ProfileFiles writes local pprof/trace files. No HTTP server is started.
type ProfileFiles struct {
	CPU   string
	Heap  string
	Trace string

	cpuFile   *os.File
	traceFile *os.File
}

// Start begins CPU and runtime/trace profiles when paths are set.
func (p *ProfileFiles) Start() error {
	if p == nil {
		return nil
	}
	if p.CPU != "" {
		file, err := os.Create(p.CPU)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(file); err != nil {
			file.Close()
			return err
		}
		p.cpuFile = file
	}
	if p.Trace != "" {
		file, err := os.Create(p.Trace)
		if err != nil {
			p.Stop()
			return err
		}
		if err := trace.Start(file); err != nil {
			file.Close()
			p.Stop()
			return err
		}
		p.traceFile = file
	}
	return nil
}

// Stop ends profiles and writes the heap profile if requested.
func (p *ProfileFiles) Stop() error {
	if p == nil {
		return nil
	}
	if p.cpuFile != nil {
		pprof.StopCPUProfile()
		_ = p.cpuFile.Close()
		p.cpuFile = nil
	}
	if p.traceFile != nil {
		trace.Stop()
		_ = p.traceFile.Close()
		p.traceFile = nil
	}
	if p.Heap != "" {
		file, err := os.Create(p.Heap)
		if err != nil {
			return err
		}
		defer file.Close()
		runtime.GC()
		return pprof.WriteHeapProfile(file)
	}
	return nil
}
