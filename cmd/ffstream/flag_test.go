package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/xaionaro-go/avpipeline/kernel"
	avptypes "github.com/xaionaro-go/avpipeline/types"
	"github.com/xaionaro-go/ffstream/pkg/ffstream"
)

func TestResourcesByFallbackPriority(t *testing.T) {
	ctx := context.Background()

	t.Run("nilOnEmpty", func(t *testing.T) {
		var s ffstream.Resources
		if got := s.ByFallbackPriority(ctx); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})

	t.Run("groupsAndSorts", func(t *testing.T) {
		s := ffstream.Resources{
			{
				URL: "a",
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{
						{Key: "fallback_priority", Value: "2"},
					},
				},
			},
			{
				URL: "b",
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{
						{Key: "fallback_priority", Value: "1"},
					},
				},
			},
			{
				URL: "c",
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{
						{Key: "fallback_priority", Value: "1"},
					},
				},
			},
			{URL: "d"}, // priority 0 (default)
			{
				URL: "e",
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{
						{Key: "fallback_priority", Value: "2"},
					},
				},
			},
		}

		got := s.ByFallbackPriority(ctx)
		want := []ffstream.Resources{
			{{URL: "d"}}, // priority 0
			{
				{
					URL: "b",
					InputConfig: kernel.InputConfig{
						CustomOptions: avptypes.DictionaryItems{
							{Key: "fallback_priority", Value: "1"},
						},
					},
				},
				{
					URL: "c",
					InputConfig: kernel.InputConfig{
						CustomOptions: avptypes.DictionaryItems{
							{Key: "fallback_priority", Value: "1"},
						},
					},
				},
			},
			{
				{
					URL: "a",
					InputConfig: kernel.InputConfig{
						CustomOptions: avptypes.DictionaryItems{
							{Key: "fallback_priority", Value: "2"},
						},
					},
				},
				{
					URL: "e",
					InputConfig: kernel.InputConfig{
						CustomOptions: avptypes.DictionaryItems{
							{Key: "fallback_priority", Value: "2"},
						},
					},
				},
			},
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected result\nwant: %#v\n got: %#v", want, got)
		}
	})
}
