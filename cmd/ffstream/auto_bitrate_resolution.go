// auto_bitrate_resolution.go contains the parser for the
// -auto_bitrate_resolution flag. Extracted from flag.go so the parsing
// logic can be unit-tested directly without driving end-to-end through
// parseFlags + recover-based fatal capture.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/xaionaro-go/avpipeline/codec"
	streammuxtypes "github.com/xaionaro-go/avpipeline/preset/streammux/types"
)

// parseAutoBitrateResolutionRows parses the -auto_bitrate_resolution
// flag's repeated occurrences into a sorted ladder.
//
// Two syntaxes per occurrence:
//   - WxH                — bare form; this resolution applies to all bitrates.
//     Returns a single row with BitrateLow=0/BitrateHigh=0 AND bareForm=true.
//     Caller fills in codec-default envelope after consulting the codec ladder.
//   - WxH:LowBps-HighBps — banded form; this resolution covers only the
//     named bitrate band.
//
// Mixing bare and banded across occurrences returns an error. Bare repeated
// returns an error. LO >= HI returns an error. Overlapping or touching bands
// return an error (BitRate() lookup is inclusive on both ends, so adjacent
// ranges sharing an endpoint would be ambiguous).
//
// Returns (rows, bareForm, err). When bareForm==true, len(rows)==1 and the
// caller MUST fill rows[0].BitrateLow / rows[0].BitrateHigh from the
// codec-default ladder envelope.
func parseAutoBitrateResolutionRows(
	vs []string,
) (streammuxtypes.AutoBitRateResolutionAndBitRateConfigs, bool, error) {
	if len(vs) == 0 {
		return nil, false, nil
	}
	var rows streammuxtypes.AutoBitRateResolutionAndBitRateConfigs
	var hasBare, hasBanded bool
	for _, v := range vs {
		resPart, bandPart, hasBand := strings.Cut(v, ":")
		var r codec.Resolution
		if err := r.Parse(resPart); err != nil {
			return nil, false, fmt.Errorf("unable to parse %q: %w", v, err)
		}
		if r.Width == 0 || r.Height == 0 {
			return nil, false, fmt.Errorf("%q has zero dimension", v)
		}
		if !hasBand {
			hasBare = true
			rows = streammuxtypes.AutoBitRateResolutionAndBitRateConfigs{{Resolution: r}}
			continue
		}
		hasBanded = true
		lo, hi, ok := strings.Cut(bandPart, "-")
		if !ok {
			return nil, false, fmt.Errorf("%q: band must be LO-HI", v)
		}
		loV, err := humanize.ParseBytes(lo)
		if err != nil {
			return nil, false, fmt.Errorf("%q: bad low bitrate: %w", v, err)
		}
		hiV, err := humanize.ParseBytes(hi)
		if err != nil {
			return nil, false, fmt.Errorf("%q: bad high bitrate: %w", v, err)
		}
		if loV >= hiV {
			return nil, false, fmt.Errorf("%q: LO must be < HI", v)
		}
		rows = append(rows, streammuxtypes.AutoBitRateResolutionAndBitRateConfig{
			Resolution:  r,
			BitrateLow:  streammuxtypes.Ubps(loV),
			BitrateHigh: streammuxtypes.Ubps(hiV),
		})
	}
	if hasBare && hasBanded {
		return nil, false, fmt.Errorf("cannot mix bare WxH with WxH:LO-HI")
	}
	if hasBare && len(vs) > 1 {
		return nil, false, fmt.Errorf("bare WxH form is not repeatable")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].BitrateLow < rows[j].BitrateLow })
	for i := 1; i < len(rows); i++ {
		if rows[i].BitrateLow <= rows[i-1].BitrateHigh {
			return nil, false, fmt.Errorf(
				"overlapping or touching bitrate bands at row %d (ranges are inclusive on both ends; e.g. use 2M-4999999 then 5M-12M, not 2M-5M then 5M-12M)",
				i,
			)
		}
	}
	return rows, hasBare, nil
}
