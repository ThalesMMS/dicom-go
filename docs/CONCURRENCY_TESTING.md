# Concurrency testing

The default `make check` remains the fast baseline. Run the focused concurrency
gates independently when changing networking, asynchronous operations,
registries, subscriptions, dispatchers or shutdown behavior:

```sh
make race
make leak
```

`make race` runs the race detector over `net/dicomweb`, `net/ul`, `net/dimse`,
`net/telemetry`, `transfer` and `ups`. `make leak` repeats the selected lifecycle
tests that explicitly wait for handlers, associations, sessions, queues,
dispatchers and deliveries to finish after cancellation, timeout, error and
shutdown. Override `RACE_COUNT`, `LEAK_COUNT` or `LEAK_TIMEOUT` for a longer
local stress run.

Both targets use loopback listeners, in-process peers and synthetic data. They
do not require a PACS, internet service, patient data, display server, native
codec or GPU runtime. External interoperability tests are excluded from the
race target. Run these targets only on a host supported by the Go race detector;
the repository CI uses Linux amd64 and records the expanded package list in the
job summary before testing.

The lifecycle gate intentionally relies on explicit completion channels and
joins instead of polling the process-wide goroutine count. A leaked operation
therefore fails at the bounded test or package timeout without depending on
unrelated runtime goroutines.
