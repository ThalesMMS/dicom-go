package render

import (
	"context"
	"runtime"
	"sync"
)

func parallelRows(rowCount int, renderRow func(int)) {
	if rowCount <= 0 {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 || rowCount < 32 {
		for row := 0; row < rowCount; row++ {
			renderRow(row)
		}
		return
	}
	if workers > rowCount {
		workers = rowCount
	}
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		start := rowCount * worker / workers
		end := rowCount * (worker + 1) / workers
		if start == end {
			continue
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for row := start; row < end; row++ {
				renderRow(row)
			}
		}(start, end)
	}
	wg.Wait()
}

// parallelRowsContext renders independent output rows with the shared bounded
// worker pool while honoring cancellation between rows. Workers that observe a
// canceled context skip their remaining range; the caller receives ctx.Err()
// only after every started worker has stopped touching the output buffer.
func parallelRowsContext(ctx context.Context, rowCount int, renderRow func(int)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	parallelRows(rowCount, func(row int) {
		if ctx.Err() == nil {
			renderRow(row)
		}
	})
	return ctx.Err()
}
