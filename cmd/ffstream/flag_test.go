// flag_test.go contains tests for command-line flags.
package main

import (
	"context"
	"reflect"
	"testing"

	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
)

func TestResourcesByFallbackPriority(t *testing.T) {
	t.Run("nilOnEmpty", func(t *testing.T) {
		var s ffstream.Resources
		if got := s.ByFallbackPriority(); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})

	t.Run("groupsAndSorts", func(t *testing.T) {
		s := ffstream.Resources{
			{URL: "a", Priority: 2},
			{URL: "b", Priority: 1},
			{URL: "c", Priority: 1},
			{URL: "d"}, // priority 0 (default)
			{URL: "e", Priority: 2},
		}

		got := s.ByFallbackPriority()
		want := []ffstream.Resources{
			{{URL: "d"}}, // priority 0
			{
				{URL: "b", Priority: 1},
				{URL: "c", Priority: 1},
			},
			{
				{URL: "a", Priority: 2},
				{URL: "e", Priority: 2},
			},
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected result\nwant: %#v\n got: %#v", want, got)
		}
	})
}

func TestParseFlags_Suppressed(t *testing.T) {
	args := []string{"ffstream", "-suppressed", "true", "-i", "rtsp://input1", "rtmp://output"}
	_, flags := parseFlags(args)

	if len(flags.Inputs) != 1 {
		t.Fatalf("expected 1 input, got %d", len(flags.Inputs))
	}

	if !flags.Inputs[0].Suppressed {
		t.Errorf("expected Suppressed to be true for input1")
	}

	if flags.Inputs[0].URL != "rtsp://input1" {
		t.Errorf("expected URL to be rtsp://input1, got %q", flags.Inputs[0].URL)
	}
}

// TestParseFlags_QueueSize verifies that the four -queue_size_* flags are
// registered with the parser (not silently passed through as unknown
// options to the trailing output URL) and that their values land in the
// matching Flags fields. The deprecated -queue_size_default knob is also
// covered: parseFlags simply preserves its raw value in
// Flags.QueueSizeDefault — the per-role fan-out lives in main.go (see the
// transcoderInput / outputInput / *Error block in main()), not here.
func TestParseFlags_QueueSize(t *testing.T) {
	const outputURL = "rtmp://output"

	// queueLeak inspects flags.Outputs[0].CustomOptions for any entry whose
	// Key matches one of the registered queue-size flag names. Such a match
	// would mean the parser failed to consume the flag and convertUnknown-
	// OptionsToAVPCustomOptions later turned the leftover "-queue_size_*
	// N" pair into a custom option attached to the output URL — exactly
	// the regression we want to catch.
	queueLeak := func(t *testing.T, opts avptypes.DictionaryItems) {
		t.Helper()
		for _, opt := range opts {
			switch opt.Key {
			case "queue_size_default",
				"queue_size_transcoder",
				"queue_size_output",
				"queue_size_error":
				t.Errorf("queue-size flag leaked into output CustomOptions as %q=%q", opt.Key, opt.Value)
			}
		}
	}

	cases := []struct {
		name     string
		flagName string
		value    string
		check    func(t *testing.T, f Flags)
	}{
		{
			name:     "transcoder",
			flagName: "-queue_size_transcoder",
			value:    "7",
			check: func(t *testing.T, f Flags) {
				if f.QueueSizeTranscoder != 7 {
					t.Errorf("QueueSizeTranscoder: want 7, got %d", f.QueueSizeTranscoder)
				}
				// Untargeted siblings stay at the zero sentinel.
				if f.QueueSizeOutput != 0 || f.QueueSizeError != 0 || f.QueueSizeDefault != 0 {
					t.Errorf("untargeted siblings should be 0, got default=%d output=%d error=%d",
						f.QueueSizeDefault, f.QueueSizeOutput, f.QueueSizeError)
				}
			},
		},
		{
			name:     "output",
			flagName: "-queue_size_output",
			value:    "11",
			check: func(t *testing.T, f Flags) {
				if f.QueueSizeOutput != 11 {
					t.Errorf("QueueSizeOutput: want 11, got %d", f.QueueSizeOutput)
				}
				if f.QueueSizeTranscoder != 0 || f.QueueSizeError != 0 || f.QueueSizeDefault != 0 {
					t.Errorf("untargeted siblings should be 0, got default=%d transcoder=%d error=%d",
						f.QueueSizeDefault, f.QueueSizeTranscoder, f.QueueSizeError)
				}
			},
		},
		{
			name:     "error",
			flagName: "-queue_size_error",
			value:    "3",
			check: func(t *testing.T, f Flags) {
				if f.QueueSizeError != 3 {
					t.Errorf("QueueSizeError: want 3, got %d", f.QueueSizeError)
				}
				if f.QueueSizeTranscoder != 0 || f.QueueSizeOutput != 0 || f.QueueSizeDefault != 0 {
					t.Errorf("untargeted siblings should be 0, got default=%d transcoder=%d output=%d",
						f.QueueSizeDefault, f.QueueSizeTranscoder, f.QueueSizeOutput)
				}
			},
		},
		{
			name:     "deprecatedDefault",
			flagName: "-queue_size_default",
			value:    "5",
			check: func(t *testing.T, f Flags) {
				// parseFlags is the wrong layer to exercise the fan-out:
				// the deprecated -queue_size_default knob is preserved
				// verbatim here, and main.go reads it together with the
				// per-role flags to derive the effective queue sizes.
				if f.QueueSizeDefault != 5 {
					t.Errorf("QueueSizeDefault: want 5, got %d", f.QueueSizeDefault)
				}
				if f.QueueSizeTranscoder != 0 || f.QueueSizeOutput != 0 || f.QueueSizeError != 0 {
					t.Errorf("per-role fields must stay 0 at parseFlags layer (fan-out is in main.go); got transcoder=%d output=%d error=%d",
						f.QueueSizeTranscoder, f.QueueSizeOutput, f.QueueSizeError)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"ffstream", "-i", "rtsp://input1", tc.flagName, tc.value, outputURL}
			_, flags := parseFlags(args)

			tc.check(t, flags)

			// Pass-through regression check: registered flags must not
			// leak into the trailing output's CustomOptions.
			if len(flags.Outputs) != 1 {
				t.Fatalf("expected exactly 1 output, got %d (%#v)", len(flags.Outputs), flags.Outputs)
			}
			if got := flags.Outputs[0].URL; got != outputURL {
				t.Errorf("output URL: want %q, got %q", outputURL, got)
			}
			queueLeak(t, flags.Outputs[0].CustomOptions)
		})
	}
}

// TestParseFlags_QueueSize_AllAtOnce covers the realistic case where the
// operator overrides every per-role flag in a single invocation: every
// field must reflect its argument independently, with no cross-talk
// between flags.
func TestParseFlags_QueueSize_AllAtOnce(t *testing.T) {
	args := []string{
		"ffstream",
		"-i", "rtsp://input1",
		"-queue_size_transcoder", "7",
		"-queue_size_output", "11",
		"-queue_size_error", "3",
		"-queue_size_default", "5",
		"rtmp://output",
	}
	_, flags := parseFlags(args)

	if flags.QueueSizeTranscoder != 7 {
		t.Errorf("QueueSizeTranscoder: want 7, got %d", flags.QueueSizeTranscoder)
	}
	if flags.QueueSizeOutput != 11 {
		t.Errorf("QueueSizeOutput: want 11, got %d", flags.QueueSizeOutput)
	}
	if flags.QueueSizeError != 3 {
		t.Errorf("QueueSizeError: want 3, got %d", flags.QueueSizeError)
	}
	if flags.QueueSizeDefault != 5 {
		t.Errorf("QueueSizeDefault: want 5, got %d", flags.QueueSizeDefault)
	}

	if len(flags.Outputs) != 1 {
		t.Fatalf("expected exactly 1 output, got %d (%#v)", len(flags.Outputs), flags.Outputs)
	}
	for _, opt := range flags.Outputs[0].CustomOptions {
		switch opt.Key {
		case "queue_size_default",
			"queue_size_transcoder",
			"queue_size_output",
			"queue_size_error":
			t.Errorf("queue-size flag leaked into output CustomOptions as %q=%q", opt.Key, opt.Value)
		}
	}
}

// TestParseFlags_QueueSize_SentinelZero pins the documented sentinel: when
// none of the four queue-size flags are passed, all four fields stay at
// uint64(0), which main.go then forwards as "leave the avpipeline
// compiled-in default" for every channel.
func TestParseFlags_QueueSize_SentinelZero(t *testing.T) {
	args := []string{"ffstream", "-i", "rtsp://input1", "rtmp://output"}
	_, flags := parseFlags(args)

	if flags.QueueSizeDefault != 0 {
		t.Errorf("QueueSizeDefault: want 0, got %d", flags.QueueSizeDefault)
	}
	if flags.QueueSizeTranscoder != 0 {
		t.Errorf("QueueSizeTranscoder: want 0, got %d", flags.QueueSizeTranscoder)
	}
	if flags.QueueSizeOutput != 0 {
		t.Errorf("QueueSizeOutput: want 0, got %d", flags.QueueSizeOutput)
	}
	if flags.QueueSizeError != 0 {
		t.Errorf("QueueSizeError: want 0, got %d", flags.QueueSizeError)
	}
}

func TestExtractAndStripPriority(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name         string
		in           avptypes.DictionaryItems
		wantPriority uint
		wantOpts     avptypes.DictionaryItems
	}{
		{
			name:         "empty",
			in:           nil,
			wantPriority: 0,
			wantOpts:     nil,
		},
		{
			name: "noPriorityKey",
			in: avptypes.DictionaryItems{
				{Key: "foo", Value: "bar"},
			},
			wantPriority: 0,
			wantOpts: avptypes.DictionaryItems{
				{Key: "foo", Value: "bar"},
			},
		},
		{
			name: "singlePriorityOnly",
			// fallback_priority is the only entry; after stripping the
			// result slice is empty so the implementation returns nil.
			in: avptypes.DictionaryItems{
				{Key: "fallback_priority", Value: "5"},
			},
			wantPriority: 5,
			wantOpts:     nil,
		},
		{
			name: "mixed",
			in: avptypes.DictionaryItems{
				{Key: "foo", Value: "bar"},
				{Key: "fallback_priority", Value: "3"},
				{Key: "baz", Value: "qux"},
			},
			wantPriority: 3,
			wantOpts: avptypes.DictionaryItems{
				{Key: "foo", Value: "bar"},
				{Key: "baz", Value: "qux"},
			},
		},
		{
			name: "malformedValue",
			// On parse failure the implementation logs and `continue`s,
			// which means the bad entry is stripped (not preserved) and
			// priority stays at 0. With no other items the result is nil.
			in: avptypes.DictionaryItems{
				{Key: "fallback_priority", Value: "not-a-number"},
			},
			wantPriority: 0,
			wantOpts:     nil,
		},
		{
			name: "duplicatePriorityLastWins",
			// Loop overwrites priority on each match → last value wins.
			// Both entries are stripped from the result.
			in: avptypes.DictionaryItems{
				{Key: "fallback_priority", Value: "2"},
				{Key: "fallback_priority", Value: "7"},
			},
			wantPriority: 7,
			wantOpts:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPriority, gotOpts := extractAndStripPriority(ctx, tc.in)
			if gotPriority != tc.wantPriority {
				t.Fatalf("priority: want %d, got %d", tc.wantPriority, gotPriority)
			}
			if !reflect.DeepEqual(gotOpts, tc.wantOpts) {
				t.Fatalf("opts:\nwant: %#v\n got: %#v", tc.wantOpts, gotOpts)
			}
		})
	}
}
