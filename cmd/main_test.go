package main

import (
	"errors"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestWaitHandlerReturnsOnTerminationSignal pins the behaviour that makes the graceful shutdown in
// main reachable at all. A signal missing from signal.Notify keeps its default disposition, and for
// SIGTERM that means the runtime terminates the process immediately: the handler never returns,
// client.Stop is never called, and in-flight connections are severed instead of drained.
//
// Note the failure mode if the SIGTERM registration is ever dropped: this test does not report a
// normal assertion failure, it takes the whole test binary down with it and `go test` reports
// "signal: terminated". That is still a hard failure, and it is the same thing production does.
//
// These cases must not run in parallel — signal registration is process-wide state.
func TestWaitHandlerReturnsOnTerminationSignal(t *testing.T) {
	for name, sig := range map[string]syscall.Signal{
		"SIGTERM": syscall.SIGTERM,
		"SIGINT":  syscall.SIGINT,
	} {
		t.Run(name, func(t *testing.T) {
			_, waitHandler := wait(zerolog.Nop())
			t.Cleanup(func() { signal.Reset(os.Interrupt, syscall.SIGTERM) })

			done := make(chan int, 1)
			go func() { done <- waitHandler() }()

			// wait registered the handler before returning, so the signal lands in its buffered
			// channel rather than killing the binary; no need to wait for the goroutine first.
			require.NoError(t, syscall.Kill(syscall.Getpid(), sig))

			select {
			case exitStatus := <-done:
				require.Zero(t, exitStatus, "a termination signal is a clean exit, not a failure")
			case <-time.After(5 * time.Second):
				t.Fatal("waitHandler did not return; the graceful shutdown in main would be skipped")
			}
		})
	}
}

// TestWaitHandlerAsyncErrorExitsNonZero pins that an async failure still unblocks the handler and
// still reports a non-zero exit status, so moving signal.Notify out of the handler cannot quietly
// change how a startup or runtime failure is surfaced.
func TestWaitHandlerAsyncErrorExitsNonZero(t *testing.T) {
	asyncError, waitHandler := wait(zerolog.Nop())
	t.Cleanup(func() { signal.Reset(os.Interrupt, syscall.SIGTERM) })

	done := make(chan int, 1)
	go func() { done <- waitHandler() }()

	asyncError(errors.New("something failed asynchronously"))

	select {
	case exitStatus := <-done:
		require.Equal(t, 1, exitStatus, "an async error must not be reported as a clean exit")
	case <-time.After(5 * time.Second):
		t.Fatal("waitHandler did not return after an async error")
	}
}

// TestShutdownTimeoutExceedsRequestTimeout guards the relationship the shutdownTimeout comment
// relies on: it must be longer than the router's request timeout, otherwise a slow but healthy
// render trips the shutdown deadline and main turns a clean stop into a Fatal.
func TestShutdownTimeoutExceedsRequestTimeout(t *testing.T) {
	t.Parallel()

	// internal/transport.Server.initMiddleware: s.router.Use(m.timeout(15 * time.Second))
	const routerRequestTimeout = 15 * time.Second

	require.Greater(t, shutdownTimeout, routerRequestTimeout)
}
