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
			{URL: "a", FallbackPriority: 2},
			{URL: "b", FallbackPriority: 1},
			{URL: "c", FallbackPriority: 1},
			{URL: "d", FallbackPriority: 0},
			{URL: "e", FallbackPriority: 2},
		}

		got := s.ByFallbackPriority()
		want := []ffstream.Resources{
			{{URL: "d", FallbackPriority: 0}},
			{{URL: "b", FallbackPriority: 1}, {URL: "c", FallbackPriority: 1}},
			{{URL: "a", FallbackPriority: 2}, {URL: "e", FallbackPriority: 2}},
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected result\nwant: %#v\n got: %#v", want, got)
		}
	})
}
