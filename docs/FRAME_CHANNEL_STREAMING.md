# Cancelable native frame delivery

`parser.NewFrameChannelSinkContext(ctx, ch)` and its `object` facade add
cancelable backpressure without changing `NewFrameChannelSink(ch)`. The legacy
adapter still blocks until a receiver or buffer slot becomes available.

Use the context adapter when a consumer can stop early. Cancellation or deadline
expiry interrupts a blocked channel send and remains discoverable through
`errors.Is(err, context.Canceled)` or `context.DeadlineExceeded`, including the
parser's `ParseError` / `ErrFrameSink` chain. No helper goroutine is created per
send. Cancellation already present at entry prevents delivery; a send that races
with cancellation can still deliver one frame.

The producer owns finalization and the sink owns closing the channel. Consumers
cancel the context; they must never close the channel, send to it, or share it
between independently owned sinks. `Close` interrupts blocked sends, waits for
active sends, then closes the channel exactly once. The context adapter supports
concurrent `HandleFrame` and `Close`; the parser `Reader` remains single-owner.
The legacy adapter requires sends to finish before `Close`.

Each delivered `Frame.Data` is a distinct buffer owned by the receiver. Retaining
it requires no copy; sending does not copy it again. The sender must not mutate
it after delivery. Channel capacity sets the number of queued frames; an
unbuffered channel applies immediate backpressure. Memory also includes the
current producer frame and any buffers retained by the consumer. Metadata is
copied by value. The parser's existing byte limits still apply.

`Reader.ReadAll` and `ReadDataSet` finalize their sink on success, parse failure,
sink error or cancellation. Token-level `Next` users own finalization when they
stop reading. `object.ReadFileWithOptions`, `ReadDataSetWithOptions` and their
`Open` variants now also finalize on errors before parser creation (for example,
invalid Part 10 headers, unsupported syntax and failed file opening). Nested
helpers call the underlying sink's `Close` once; read and close errors are both
preserved. Caller-owned input readers are not closed by these functions.

This context controls channel delivery. It cannot interrupt an arbitrary
`io.Reader.Read` blocked before the next frame, nor guarantee interruption of
arbitrary computation inside a custom sink. For network sources configure read
deadlines, a context-aware reader, or close a source whose API supports concurrent
interruption. The source owner is responsible for that I/O lifecycle.

Run the [complete example](../examples/stream-native-frames/main.go) to display
at most three native frames, cancel delivery and join the parser:

```sh
go run ./examples/stream-native-frames -- /path/to/synthetic.dcm 3
go test -race ./parser ./object ./examples/stream-native-frames -run Frame -count=1
```

This sink remains dedicated to native `Frame` values. The distinct
`NewEncodedFrameChannelSinkContext` reuses its cancellation lifecycle for the
[incremental encoded-frame contract](ENCAPSULATED_FRAME_STREAMING.md), which
assembles JPEG/JPEG-LS Items before delivery. Neither channel adapter decodes pixels.
