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
