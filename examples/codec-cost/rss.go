package codeccost

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func peekProcessRSS(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, nil
	}
	switch runtime.GOOS {
	case "darwin", "linux":
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0, err
		}
		kb, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	default:
		return 0, nil
	}
}

// startRSSSample peeks child RSS once while the child is expected to be alive.
// The returned function is called after Wait and must not block: a late sample
// is dropped so RSS collection cannot enter StageExecute.
func startRSSSample(pid int) func() uint64 {
	result := make(chan uint64, 1)
	go func() {
		time.Sleep(3 * time.Millisecond)
		rss, _ := peekProcessRSS(pid)
		result <- rss
	}()
	return func() uint64 {
		return recvRSSSample(result)
	}
}

func recvRSSSample(ch <-chan uint64) uint64 {
	select {
	case rss := <-ch:
		return rss
	default:
		return 0
	}
}

func waitAndSampleRSS(cmd *exec.Cmd) (uint64, error) {
	if cmd == nil {
		return 0, nil
	}
	if cmd.Process == nil {
		return 0, cmd.Wait()
	}
	finish := startRSSSample(cmd.Process.Pid)
	err := cmd.Wait()
	return finish(), err
}
