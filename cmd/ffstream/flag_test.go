// flag_test.go contains tests for command-line flags.
package main

import (
	"reflect"
	"testing"

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
