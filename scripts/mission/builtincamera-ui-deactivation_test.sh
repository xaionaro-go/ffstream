#!/usr/bin/env bash
set -euo pipefail

# Test fixture for builtincamera-ui-deactivation.sh.
#
# Style: mirrors builtincamera-ui-activation_test.sh (Task #2). Subprocess invocation
# under mocked adb/ffprobe/ffmpeg/ffstreamctl; assert exit-code + result.txt content.
# Per testing-discipline: each test has Broke-the-code self-documentation.

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/builtincamera-ui-deactivation.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

tmp_dir=$(mktemp -d)
cleanup() {
	rm -rf "$tmp_dir"
}
trap cleanup EXIT
[ -n "${KEEP_TMP:-}" ] && trap - EXIT

bin_dir="$tmp_dir/bin"
mkdir -p "$bin_dir"

# ----- UI XML fixtures -----------------------------------------------------
# Active state with enabled Deactivate button (happy-path pre-condition).
ui_active_dump="$tmp_dir/ui-active.xml"
cat >"$ui_active_dump" <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<hierarchy>
  <node text="Status: Active" bounds="[0,500][800,560]" enabled="true" clickable="false" package="center.dx.wingout" />
  <node text="Deactivate" bounds="[0,2192][487,2335]" enabled="true" clickable="true" package="center.dx.wingout" />
</hierarchy>
XML

# Inactive state — what the UI shows after _commitDeactivate fires.
ui_inactive_dump="$tmp_dir/ui-inactive.xml"
cat >"$ui_inactive_dump" <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<hierarchy>
  <node text="Status: Inactive" bounds="[0,500][800,560]" enabled="true" clickable="false" package="center.dx.wingout" />
  <node text="Activate" bounds="[0,2192][487,2335]" enabled="true" clickable="true" package="center.dx.wingout" />
</hierarchy>
XML

# Active state with Deactivate button DISABLED (rare race: Active committed but next gRPC
# leg already kicked off). Fail-52 trigger — distinct from fail-51 (no Active state).
ui_active_no_button_dump="$tmp_dir/ui-active-no-button.xml"
cat >"$ui_active_no_button_dump" <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<hierarchy>
  <node text="Status: Active" bounds="[0,500][800,560]" enabled="true" clickable="false" package="center.dx.wingout" />
  <node text="Deactivate" bounds="[0,2192][487,2335]" enabled="false" clickable="true" package="center.dx.wingout" />
</hierarchy>
XML

# Deactivate-failed dialog — fail-53 trigger.
ui_dialog_dump="$tmp_dir/ui-dialog.xml"
cat >"$ui_dialog_dump" <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<hierarchy>
  <node text="Status: Active" bounds="[0,500][800,560]" />
  <node text="Deactivate failed" bounds="[100,800][900,900]" />
</hierarchy>
XML

# Wholly inactive (Goal 3 not even applicable) — fail-51 trigger.
ui_already_inactive_dump="$tmp_dir/ui-already-inactive.xml"
cat >"$ui_already_inactive_dump" <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<hierarchy>
  <node text="Status: Inactive" bounds="[0,500][800,560]" />
  <node text="Activate" bounds="[0,2192][487,2335]" enabled="true" clickable="true" package="center.dx.wingout" />
</hierarchy>
XML

# ----- Mock binaries -------------------------------------------------------
# adb stub: dump_ui calls write FAKE_UI_PRE_TAP for label "before-deactivate", then
# FAKE_UI_POST_TAP for "after-N" labels. Lets each test control the UI sequence.
cat >"$bin_dir/adb" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "-s" ]; then
	shift 2
fi

case "${1:-}" in
	forward)
		exit 0
		;;
	pull)
		# pull /sdcard/window.xml <dest> — choose UI based on dest filename.
		dest="${3:-}"
		case "$dest" in
			*uidump-before-deactivate.xml)
				cp "$FAKE_UI_PRE_TAP" "$dest"
				;;
			*)
				cp "$FAKE_UI_POST_TAP" "$dest"
				;;
		esac
		exit 0
		;;
	shell)
		shift
		cmd="$*"
		case "$cmd" in
			"uiautomator dump /sdcard/window.xml")
				exit 0
				;;
			"pidof center.dx.wingout")
				printf '1234\n'
				exit 0
				;;
			"dumpsys window")
				printf 'mCurrentFocus=Window{center.dx.wingout/center.dx.wingout.MainActivity}\n'
				exit 0
				;;
			"dumpsys activity top")
				printf 'ACTIVITY center.dx.wingout/.MainActivity\n'
				exit 0
				;;
			input\ tap*)
				exit 0
				;;
			*)
				exit 0
				;;
		esac
		;;
esac

exit 0
STUB
chmod +x "$bin_dir/adb"

# ffstreamctl stub: any subcommand returns empty json.
cat >"$bin_dir/ffstreamctl" <<'STUB'
#!/usr/bin/env bash
printf '{}\n'
exit 0
STUB
chmod +x "$bin_dir/ffstreamctl"

# ffprobe stub: behavior driven by env var FAKE_FFPROBE_MODE.
#  - dji-success: 1080p h264 + aac with 300 video + 480 audio frames (in-range)
#  - dji-fail: nb_read_frames out of range (FAIL DJI continuity gate)
#  - builtin-empty: simulate route absence → exit 1 (consistent with rw_timeout)
#  - builtin-publishing: exit 0 with non-empty streams (FAIL emptiness gate)
cat >"$bin_dir/ffprobe" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail

# Last arg is the URL; differentiate dji vs builtin probe by URL pattern.
url=""
for arg in "$@"; do
	url="$arg"
done

mode="${FAKE_FFPROBE_MODE:-default}"
case "$url" in
	*builtincamera-merged*)
		case "$mode" in
			builtin-publishing|*-builtin-publishing)
				cat <<'JSON'
{"streams":[{"index":0,"codec_type":"video"}]}
JSON
				exit 0
				;;
			*)
				echo "rtmp open failed (mocked empty)" >&2
				exit 1
				;;
		esac
		;;
	*dji-osmo-pocket-3-merged*|*dji-merged-*.mkv|*dji-merged*.mkv)
		case "$mode" in
			dji-fail|builtin-publishing-dji-fail)
				cat <<'JSON'
{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"r_frame_rate":"30/1","nb_read_frames":"30","duration":"1"},{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,"nb_read_frames":"40","duration":"1"}]}
JSON
				exit 0
				;;
			*)
				cat <<'JSON'
{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"r_frame_rate":"30/1","nb_read_frames":"300","duration":"10"},{"index":1,"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,"nb_read_frames":"480","duration":"10"}]}
JSON
				exit 0
				;;
		esac
		;;
	*)
		exit 1
		;;
esac
STUB
chmod +x "$bin_dir/ffprobe"

# ffmpeg stub: capture writes a 1-byte placeholder file (test treats existence as
# capture success per `[ ! -s "$capture_media" ]` gate); other invocations no-op.
cat >"$bin_dir/ffmpeg" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail

# Find the output file (positional after all options/inputs).
prev=""
out=""
for arg in "$@"; do
	if [ "$prev" = "-i" ]; then
		prev=""
		continue
	fi
	case "$arg" in
		-*)
			prev="$arg"
			;;
		*)
			out="$arg"
			prev=""
			;;
	esac
done
if [ -n "$out" ]; then
	printf 'mock-capture\n' >"$out"
fi
exit 0
STUB
chmod +x "$bin_dir/ffmpeg"

export PATH="$bin_dir:$PATH"

# ----- Test runner helpers -------------------------------------------------
run_test_run() {
	local name="$1"
	local pre_tap="$2"
	local post_tap="$3"
	local ffprobe_mode="$4"
	local expected_result="$5"
	local expected_status="$6"

	local run_root="$tmp_dir/run-$name"
	rm -rf "$run_root"
	mkdir -p "$run_root"

	set +e
	RUN_ROOT="$run_root" \
		PHONE_SERIAL=41041JEKB08092 \
		FFSTREAMCTL_BIN="$bin_dir/ffstreamctl" \
		FAKE_UI_PRE_TAP="$pre_tap" \
		FAKE_UI_POST_TAP="$post_tap" \
		FAKE_FFPROBE_MODE="$ffprobe_mode" \
		DEACTIVATION_POLL_COUNT=2 \
		DEACTIVATION_POLL_INTERVAL_SECONDS=0 \
		BUILTIN_CAMERA_PROBE_TIMEOUT_SECONDS=2 \
		DJI_CAPTURE_SECONDS=1 \
		"$script" >"$tmp_dir/$name.stdout" 2>"$tmp_dir/$name.stderr"
	local status=$?
	set -e

	local run_dir
	run_dir=$(sed -n 's/^RUN_DIR=//p' "$tmp_dir/$name.stdout" | tail -1)
	if [ -z "$run_dir" ] || [ ! -s "$run_dir/result.txt" ]; then
		cat "$tmp_dir/$name.stdout" >&2
		cat "$tmp_dir/$name.stderr" >&2
		fail "$name: harness did not write a result"
	fi

	local result
	result=$(cat "$run_dir/result.txt")
	if [ "$result" != "$expected_result" ]; then
		cat "$tmp_dir/$name.stdout" >&2
		cat "$tmp_dir/$name.stderr" >&2
		ls "$run_dir" >&2 || true
		fail "$name: expected result='$expected_result' got '$result' (status=$status)"
	fi
	if [ "$status" -ne "$expected_status" ]; then
		fail "$name: expected status=$expected_status got $status (result=$result)"
	fi
}

# ============================================================================
# Integration tests
# ============================================================================

# --- T-d1: happy path → BUILTINCAMERA_DEACTIVATION_PASS ---
# Broke-the-code: any of (a) probe_builtin_camera_empty returns 1 on empty,
# (b) probe_dji_continuity returns 1 on valid capture, (c) ui_shows_inactive_status
# false-negatives on Status:Inactive — would all FAIL with mismatched fail codes.
run_test_run "T-d1-happy-path" \
	"$ui_active_dump" "$ui_inactive_dump" "default" \
	"BUILTINCAMERA_DEACTIVATION_PASS" 0

# --- T-d2: not active before tap → fail 51 ---
# Broke-the-code: ui_shows_active_status that defaults to true would let the
# script proceed to tap a non-existent Deactivate button.
run_test_run "T-d2-not-active" \
	"$ui_already_inactive_dump" "$ui_inactive_dump" "default" \
	"BUILTINCAMERA_DEACTIVATION_FAIL_NOT_ACTIVE_BEFORE_TAP" 51

# --- T-d3: Deactivate button not enabled → fail 52 ---
# Broke-the-code: deactivate_button_point_from_dump python helper accepting
# enabled="false" would make the script tap a disabled button (no UI response,
# would later cascade to fail-55 status-never-inactive instead of fail-52).
run_test_run "T-d3-no-button" \
	"$ui_active_no_button_dump" "$ui_inactive_dump" "default" \
	"BUILTINCAMERA_DEACTIVATION_FAIL_NO_ENABLED_DEACTIVATE_BUTTON" 52

# --- T-d4: deactivate dialog visible → fail 53 ---
# Broke-the-code: deactivate_dialog_visible omitting "Deactivate failed" pattern
# would let the script ignore an explicit error dialog and cascade to status-poll.
run_test_run "T-d4-dialog" \
	"$ui_active_dump" "$ui_dialog_dump" "default" \
	"BUILTINCAMERA_DEACTIVATION_FAIL_DIALOG" 53

# --- T-d5: status never reaches Inactive → fail 55 ---
# Broke-the-code: removing the post-loop ui_shows_inactive_status final check
# would cause the script to fall through to AVD-side gates with stale state →
# wrong fail code (likely 56 or PASS depending on luck).
run_test_run "T-d5-never-inactive" \
	"$ui_active_dump" "$ui_active_dump" "default" \
	"BUILTINCAMERA_DEACTIVATION_FAIL_STATUS_NEVER_INACTIVE" 55

# --- T-d6: builtin camera still publishing → fail 56 ---
# Broke-the-code: probe_builtin_camera_empty inverted logic (treating non-empty
# streams as success) would let a still-publishing route slip through as PASS.
run_test_run "T-d6-builtin-still-publishing" \
	"$ui_active_dump" "$ui_inactive_dump" "builtin-publishing" \
	"BUILTINCAMERA_DEACTIVATION_FAIL_BUILTIN_STILL_PUBLISHING" 56

# --- T-d7: DJI continuity broken → fail 57 ---
# Broke-the-code: probe_dji_continuity skipping the 250-600 video / 300-700
# audio frame-count gates would accept partial captures (e.g., 30 video frames
# in the dji-fail mock) and incorrectly PASS the DJI gate.
run_test_run "T-d7-dji-broken" \
	"$ui_active_dump" "$ui_inactive_dump" "dji-fail" \
	"BUILTINCAMERA_DEACTIVATION_FAIL_DJI_CONTINUITY_BROKEN" 57

# ============================================================================
# Unit tests via sourcing
# ============================================================================
unit_test_run() {
	local name="$1"
	local stub_dir="$2"
	local body="$3"

	(
		set -Eeuo pipefail
		export PATH="$stub_dir:$PATH"
		export RUN_ROOT="$stub_dir/run-root"
		export RUN_DIR="$stub_dir/run-dir"
		mkdir -p "$RUN_DIR"
		# shellcheck disable=SC1090
		source "$script"
		eval "$body"
	)
	local rc=$?
	if [ "$rc" -ne 0 ]; then
		fail "unit test $name failed with rc=$rc"
	fi
}

# --- T-u1: ui_shows_inactive_status detects "Status: Inactive" ---
# Broke-the-code: pattern looking for "Inactive" alone (without "Status: " prefix)
# would false-positive on dialog text like "deactivate failed... mark Inactive..."
tu1_dir="$tmp_dir/tu1"
mkdir -p "$tu1_dir/run-dir"
cp "$ui_inactive_dump" "$tu1_dir/run-dir/uidump-test1.xml"
unit_test_run "T-u1: ui_shows_inactive_status positive" "$tu1_dir" '
	ui_shows_inactive_status "test1" || exit 1
'

# --- T-u2: ui_shows_inactive_status NEGATIVE on Active state ---
# Broke-the-code: a substring-only pattern (e.g., `rg -q "Inactive"`) instead of
# the anchored `text="Status: Inactive"|content-desc="Status: Inactive"` would
# false-positive on the Active-state dump because the literal substring "Inactive"
# can appear inside dialog body copy or nearby labels (e.g., "marked Inactive
# so you can retry"). This test would FAIL with `false positive: Active state
# matched Inactive predicate` because the predicate would now return 0 against
# a dump that contains only `Status: Active` plus the Activate button — proving
# the helper's anchoring discipline catches a class of false-positives the
# happy-path positive test (T-u1) cannot reach.
tu2_dir="$tmp_dir/tu2"
mkdir -p "$tu2_dir/run-dir"
cp "$ui_active_dump" "$tu2_dir/run-dir/uidump-test2.xml"
unit_test_run "T-u2: ui_shows_inactive_status negative on Active" "$tu2_dir" '
	if ui_shows_inactive_status "test2"; then
		echo "false positive: Active state matched Inactive predicate" >&2
		exit 1
	fi
'

# --- T-u3: deactivate_dialog_visible detects error-dialog text ---
# Broke-the-code: missing "Deactivate failed" alternation would silence the
# dialog-detection signal and cascade to status-poll fail.
tu3_dir="$tmp_dir/tu3"
mkdir -p "$tu3_dir/run-dir"
cp "$ui_dialog_dump" "$tu3_dir/run-dir/uidump-test3.xml"
unit_test_run "T-u3: deactivate_dialog_visible matches error dialog" "$tu3_dir" '
	deactivate_dialog_visible "test3" || exit 1
'

# --- T-u4: deactivate_dialog_visible NEGATIVE on dialog-free dump ---
# Broke-the-code: matching "Deactivate" alone (without "failed" / "did not complete")
# would false-positive on the Deactivate button itself.
tu4_dir="$tmp_dir/tu4"
mkdir -p "$tu4_dir/run-dir"
cp "$ui_active_dump" "$tu4_dir/run-dir/uidump-test4.xml"
unit_test_run "T-u4: deactivate_dialog_visible negative on button-only dump" "$tu4_dir" '
	if deactivate_dialog_visible "test4"; then
		echo "false positive: Deactivate button text matched dialog predicate" >&2
		exit 1
	fi
'

echo "All deactivation tests passed."
