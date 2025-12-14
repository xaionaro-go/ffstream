package ffstream

import "time"

// Config is a configuration of FFStream.
// Keep fields additive (backwards compatible).
type Config struct {
	// InputRetryInterval is a delay between input reconnect attempts.
	// Zero means: use the internal/default retry interval.
	InputRetryInterval time.Duration
}

func DefaultConfig() Config {
	return Config{
		InputRetryInterval: -1,
	}
}

type Option interface {
	apply(*Config)
}

// Options is a helper wrapper around []Option.
type Options []Option

func (opts Options) apply(cfg *Config) {
	for _, opt := range opts {
		opt.apply(cfg)
	}
}

func (opts Options) Config() Config {
	cfg := DefaultConfig()
	opts.apply(&cfg)
	return cfg
}

type OptionInputRetryIntervalValue time.Duration

func (o OptionInputRetryIntervalValue) apply(cfg *Config) {
	cfg.InputRetryInterval = time.Duration(o)
}

func OptionInputRetryInterval(interval time.Duration) OptionInputRetryIntervalValue {
	return OptionInputRetryIntervalValue(interval)
}
