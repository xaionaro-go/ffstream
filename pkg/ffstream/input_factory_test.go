package ffstream

import (
	"context"
	"testing"

	"github.com/xaionaro-go/avpipeline/kernel"
	avptypes "github.com/xaionaro-go/avpipeline/types"
)

func TestInputFactory_NewInput_MultipleResourcesSamePriority(t *testing.T) {
	ctx := context.Background()

	s := &FFStream{}
	s.InputsInfo = []Resources{
		{
			{
				URL: "file:/does-not-exist-a",
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{{Key: "f", Value: "mpegts"}},
				},
			},
			{
				URL: "file:/does-not-exist-b",
				InputConfig: kernel.InputConfig{
					CustomOptions: avptypes.DictionaryItems{{Key: "f", Value: "mpegts"}},
				},
			},
		},
	}

	f := newInputFactory(s, 0)
	tee, err := f.NewInput(ctx)
	if err == nil {
		t.Fatalf("expected error opening non-existing inputs, got nil")
	}
	if tee != nil {
		// On partial success implementations might return a partially built tee;
		// we don't rely on that and keep the assertion minimal.
		_ = tee
	}
}
