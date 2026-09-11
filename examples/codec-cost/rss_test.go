package codeccost

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestPeekProcessRSSReadsCurrentProcess(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("ps RSS sampling is implemented for darwin/linux")
	}
	rss, err := peekProcessRSS(os.Getpid())
	if err != nil {
		t.Skip(err.Error())
	}
	if rss == 0 {
		t.Fatal("current process RSS was 0")
	}
}

func TestRecvRSSSampleDoesNotBlockWhenSampleIsLate(t *testing.T) {
	ch := make(chan uint64)
	started := time.Now()
	if got := recvRSSSample(ch); got != 0 {
		t.Fatalf("recvRSSSample = %d, want 0 for an unfinished sample", got)
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("recvRSSSample blocked %s after Wait; RSS wait must not enter codec time", elapsed)
	}
}
