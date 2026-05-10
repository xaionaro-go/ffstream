//go:build with_libav

// sender_factory_open_timeout_test.go covers the cross-task interface
// contract between ffstream's senderFactory.newOutputKernel and avpipeline's
// kernel.Output OpenTimeout watchdog (Task #12 Phase 4 Step 7 per spec at
// ~/tmp/task12-step7-spec/spec.md md5 338ed02416a722afd3e00c2029452d16).
//
// Two integration tests exercise the boundary:
//
//	TestSenderFactory_BoundedOpenOnUnreachable — open against a TCP listener
//	    that accepts but never completes the RTMP handshake; verify the
//	    OpenTimeout=10s watchdog interrupts avformat_open_input via
//	    astiav.IOInterrupter and the returned error wraps
//	    context.DeadlineExceeded within bounded wall-clock.
//
//	TestSenderFactory_SuccessfulOpenPassthrough — open a file:// destination
//	    that completes promptly; verify the watchdog does NOT spuriously
//	    fire and the returned error is nil with a non-nil *kernel.Output.
//
// Both tests construct the output via senderFactory.newOutputKernel
// (same-package internal access; spec §4.1 option (a)) so the
// ffstream→avpipeline boundary is crossed on the real call path with no
// mocks at the avformat boundary, per ATE Phase 4 protocol "every cross-
// task interface must have at least one test on the real call path".
package ffstream

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xaionaro-go/observability"
)

// newTestSenderFactory returns a zero-value senderFactory suitable for
// driving newOutputKernel from a test. senderFactory is `type senderFactory
// FFStream`; a zero-value FFStream cast to *senderFactory leaves
// s.StreamMux == nil so the mux-mode switch at the top of newOutputKernel
// is bypassed (the `if s.StreamMux != nil` guard skips it). All other
// sender-factory state accessed by newOutputKernel after NewOutputFromURL
// (Filter assignment via s.OutputQualityMeasurer etc.) is set on the
// returned *kernel.Output but never invoked because the success-path tests
// here close the output without dispatching packets.
func newTestSenderFactory(t *testing.T) *senderFactory {
	t.Helper()
	return (*senderFactory)(&FFStream{})
}

// TestSenderFactory_BoundedOpenOnUnreachable asserts that
// senderFactory.newOutputKernel applies OpenTimeout=10s plumbing such that
// an unreachable peer does NOT block the caller indefinitely (the bug
// being prevented is the pre-fix 95min+ wedge per ffstream commit 28b4d8c4
// body). The test stands up a TCP listener that accepts the connection but
// never sends a valid RTMP handshake response; avformat_open_input enters
// cgo and would block on TCP retransmission budget without the watchdog.
//
// Broke-the-code-validation: if the OpenTimeout plumbing at
// pkg/ffstream/sender_factory.go:143 were removed (e.g., `OpenTimeout: 0`
// or omission), the avpipeline-side watchdog at kernel/output.go:564
// (`if cfg.OpenTimeout > 0`) would not engage. No context.WithTimeout
// wrap; the watchdog goroutine waits on a parent ctx that never fires.
// astiav.OpenIOContext blocks in cgo for the full TCP retransmission
// budget (95min+ empirical per pre-fix wedge). The test would fail on
// `require.Less(elapsed, 15*time.Second)` and on the
// `errors.Is(err, context.DeadlineExceeded)` assertion (no DeadlineExceeded
// wrap because no openCtx.Err()).
//
// This direct mechanism-level falsification is the load-bearing assertion
// that the OpenTimeout plumbing is engaged on the real call path through
// the ffstream→avpipeline boundary.
//
// Dual-sided per testing-discipline:
//   - Good behavior IS happening: open returns within ~15s; error wraps
//     context.DeadlineExceeded.
//   - Bad behavior IS NOT happening: open does not block indefinitely;
//     pre-fix 95min+ wedge does not recur.
func TestSenderFactory_BoundedOpenOnUnreachable(t *testing.T) {
	ctx := context.Background()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = listener.Close()
	})

	connCh := make(chan net.Conn, 1)
	acceptErrCh := make(chan error, 1)
	observability.Go(ctx, func(context.Context) {
		conn, err := listener.Accept()
		if err != nil {
			acceptErrCh <- err
			return
		}
		connCh <- conn
	})

	var acceptedConn net.Conn
	t.Cleanup(func() {
		if acceptedConn != nil {
			_ = acceptedConn.Close()
		}
	})

	outputURL := "rtmp://" + listener.Addr().String() + "/blocked-open"
	factory := newTestSenderFactory(t)
	template := SenderTemplate{URLTemplate: outputURL}

	start := time.Now()
	output, err := factory.newOutputKernel(ctx, template, outputURL, 0)
	elapsed := time.Since(start)

	// Drain accept goroutine so the cleanup t.Cleanup hooks reap the
	// listener-accepted connection if present.
	select {
	case acceptedConn = <-connCh:
	case <-acceptErrCh:
	default:
	}

	require.Error(t, err,
		"open MUST return error on unreachable peer with OpenTimeout=10s; "+
			"the pre-fix wedge would have produced no error within bounded wall-clock")
	require.True(t, errors.Is(err, context.DeadlineExceeded),
		"error MUST wrap context.DeadlineExceeded (semantic-deterministic "+
			"assertion per Task #142 v23-2 reviewer guidance); got: %v", err)
	require.Less(t, elapsed, 15*time.Second,
		"open MUST return within ~15s (OpenTimeout=10s + slack); got %s = "+
			"pre-fix wedge regression", elapsed)
	require.Nil(t, output,
		"output MUST be nil when open errored")
}

// TestSenderFactory_SuccessfulOpenPassthrough asserts that the OpenTimeout
// watchdog does NOT spuriously fire on a destination that opens promptly.
// Uses a plain filesystem path with .flv extension for deterministic,
// fast, network-independent open. Spec §3.1 recommended file:// scheme,
// but empirical at this build (FFmpeg 8.0.1 / astiav at avpipeline
// 01d4b707) showed avformat_alloc_output_context2 misinterpreted the
// "file" scheme prefix as a format-name hint ("Requested output format
// 'file' is not known"). Plain filesystem path lets avformat auto-detect
// FLV from the .flv extension via filename, sidestepping that parser
// quirk while preserving spec intent (fast-completing destination that
// exercises the watchdog's no-spurious-fire branch).
//
// Note on URL-scheme caveat: Task #151 (URL-scheme-aware OpenTimeout —
// skip non-network outputs) flags that filesystem destinations shouldn't
// ideally be subject to network OpenTimeout. The current implementation
// applies OpenTimeout uniformly across all schemes, so this test exercises
// the current behavior. If Task #151 lands and excludes filesystem paths
// from OpenTimeout entirely, the assertion that the watchdog does not
// fire on fast open is preserved (no DeadlineExceeded), but the URL
// choice may need re-anchoring to a network scheme that completes
// promptly (e.g., a local TCP listener that completes the RTMP handshake)
// to keep exercising the watchdog code path. Re-anchor scope is
// out-of-band of this test.
//
// Broke-the-code-validation: if the watchdog logic in avpipeline
// kernel/output.go:562-606 incorrectly fired before the underlying
// OpenIOContext returned (e.g., a race condition where the watchdog's
// `<-ctx.Done()` selected before `<-openIODone` for a fast open), the
// test would fail because o.Interrupt() would trip astiav.IOInterrupter,
// avio_open2 would return "Immediate exit requested", and the returned
// error would wrap context.DeadlineExceeded via openCtx.Err() —
// require.NoError(err) would fail.
//
// Dual-sided per testing-discipline:
//   - Good behavior IS happening: open returns within ~1s with err=nil
//     and non-nil output.
//   - Bad behavior IS NOT happening: watchdog does not spuriously fire on
//     fast success; errors.Is(err, context.DeadlineExceeded) is false.
func TestSenderFactory_SuccessfulOpenPassthrough(t *testing.T) {
	ctx := context.Background()

	tempDir := t.TempDir()
	outputURL := filepath.Join(tempDir, "test-output.flv")

	factory := newTestSenderFactory(t)
	template := SenderTemplate{URLTemplate: outputURL}

	start := time.Now()
	output, err := factory.newOutputKernel(ctx, template, outputURL, 0)
	elapsed := time.Since(start)
	t.Cleanup(func() {
		if output != nil {
			_ = output.Close(ctx)
		}
	})

	require.NoError(t, err,
		"open MUST succeed on reachable file:// destination; the watchdog "+
			"must not spuriously fire on a fast-completing open")
	require.NotNil(t, output,
		"output MUST be non-nil on successful open")
	require.Less(t, elapsed, 1*time.Second,
		"file:// open MUST return promptly; got %s suggests watchdog mis-fire "+
			"(file:// open is sub-100ms typical)", elapsed)
}
