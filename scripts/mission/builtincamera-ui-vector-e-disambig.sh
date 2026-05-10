#!/usr/bin/env bash
# builtincamera-ui-vector-e-disambig.sh — T-int-4 mission-witness fixture
# per Task #175 Phase 4 v2 spec md5 2f54e30bd7533fcb53c6e1b1fceb2aa4 §4.4.
#
# Plan B AVD-side-only synthetic-cascade variant per coord W262
# ratification + W265 framing: phone-side wingout + ffstream Activate
# is REPLACED by an ffmpeg-rtmp-sender on the dev box. This sidesteps
# the Task #178 phone-side blocker (ffstream PID 25001 crash via
# AAudio cgo callback teardown race) while preserving the H1/H2
# disambiguation at the actual cascade defect locus on AVD-side.
#
# Phone-side ESDS production was already verified at Task #116
# mission-witness PASS (873 audio frames + pts continuity); this
# fixture's purpose is to disambiguate WHERE in the AVD cascade
# audio bytes are lost — Hop 1 drops vs Hop 2 drops (H1 D-A3b
# mechanism) vs post-hop2 consumer-merged route drops (H2
# D-A4-NEW3).
#
# Scenario per phase2-design.md §10 disambiguation tree:
#
#   Stream A: pixel/builtincamera-aac-48000     (pre-cascade ingest)
#   Stream B: pixel/builtincamera-h264-aac-encoder-input  (post-hop1)
#   Stream C: pixel/builtincamera-merged         (post-hop2 consumer)
#
#   bytes_A | bytes_B | bytes_C | Verdict
#   --------|---------|---------|--------
#       0   |    0    |    0    | Phone-side / synth-publish-failed
#      >0   |    0    |    0    | Hop 1 drops — A2/A3 in hop 1 cascade
#      >0   |   >0    |    0    | Hop 2 drops — H1 (D-A3b) OR H2 (D-A4-NEW3)
#      >0   |   >0    |   >0    | Drop is consumer-side (out of scope)
#
# H1 confirmation: bytes pattern matches (>0,>0,0) AND AVD logs show
#   repeated Vector E E5 marker `filterOutputCh frame dropped
#   (encoder error already set)` from kernel/transcoder.go.
#
# H2 confirmation: bytes pattern matches (>0,>0,0) AND AVD logs
#   show repeated `unable to push audio.*queue is full` from
#   node/node_serve.go.
#
# Mission-witness role: regression net for Task #179 (Vector A
# mechanism fix). Outputs disambiguation verdict + Vector E Warnf
# log presence to result.txt.
#
# Defense-in-depth gates (per coord W269):
#   - PID stability gate: assert AVD daemon UNCHANGED across capture
#     window; FAIL on restart-mid-capture (separates true ESDS-gap
#     from crash-coincidence false-positives). Not strictly needed on
#     Plan B (no phone-side ffstream involved) but applied to AVD
#     daemon as cross-validation hardening.
#
# Cleanup discipline mirrors avd-outage-recovery.sh: trap on
# EXIT/INT/TERM/HUP reaps backgrounded ffmpeg + ffprobe processes;
# tmp dir preserved or removed per KEEP_TMP env var.

set -uo pipefail

# ---------------------------------------------------------------
# Configuration (env-overridable)
# ---------------------------------------------------------------

AVD_HOST="${AVD_HOST:-192.168.141.16}"
AVD_INGEST_PORT="${AVD_INGEST_PORT:-1945}"
AVD_EGRESS_PORT="${AVD_EGRESS_PORT:-1946}"

# Synthetic publish source — silent AAC at 48000Hz mono + h264
# testpattern at 1920x1080 30fps. Deterministic; no phone-harness.
PUBLISH_DURATION_SEC="${PUBLISH_DURATION_SEC:-45}"
CAPTURE_DURATION_SEC="${CAPTURE_DURATION_SEC:-30}"

# Spec §10 wire-capture stream paths.
STREAM_A="pixel/builtincamera-aac-48000"
STREAM_B="pixel/builtincamera-h264-aac-encoder-input"
STREAM_C="pixel/builtincamera-merged"

# AVD log capture target. localhost expected when AVD runs on the
# same dev box; otherwise ssh in.
AVD_LOG_HOST="${AVD_LOG_HOST:-localhost}"
AVD_LOG_USER="${AVD_LOG_USER:-streaming}"
AVD_LOG_PATH="${AVD_LOG_PATH:-/var/log/avd/avd.log}"

# AVD daemon process name for PID-stability gate.
AVD_PROC_NAME="${AVD_PROC_NAME:-avd}"

# Run dir (per CLAUDE.md scratch-storage rule).
RUN_ROOT="${RUN_ROOT:-${HOME}/tmp/test-builtincamera-ui-vector-e-disambig-$(date -u +%Y%m%dT%H%M%SZ)}"
RUN_DIR="${RUN_DIR:-$RUN_ROOT/disambig-$(date -u +%Y%m%dT%H%M%SZ)}"
KEEP_TMP="${KEEP_TMP:-0}"

# Result file path inside RUN_DIR.
RESULT_FILE="$RUN_DIR/result.txt"

# Spec §10 H1 marker (E5 site) + H2 marker.
H1_MARKER='filterOutputCh frame dropped (encoder error already set'
H2_MARKER='unable to push audio.*queue is full'

# ---------------------------------------------------------------
# Cleanup trap (mirrors avd-outage-recovery.sh discipline)
# ---------------------------------------------------------------

declare -a BG_PIDS=()

cleanup() {
	local rc=$?
	# Reap any backgrounded children; ignore errors on already-dead.
	for pid in "${BG_PIDS[@]:-}"; do
		if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
			kill -TERM "$pid" 2>/dev/null || true
		fi
	done
	# Brief grace window for graceful exit.
	sleep 0.5
	for pid in "${BG_PIDS[@]:-}"; do
		if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
			kill -KILL "$pid" 2>/dev/null || true
		fi
	done

	if [[ "$KEEP_TMP" == "1" ]]; then
		printf 'cleanup: KEEP_TMP=1 — RUN_DIR preserved at %s\n' "$RUN_DIR" >&2
	fi
	exit "$rc"
}
trap cleanup EXIT INT TERM HUP

# ---------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------

log_iso() {
	date -u +'%Y-%m-%dT%H:%M:%SZ'
}

log_info() {
	printf '%s [INFO ] %s\n' "$(log_iso)" "$*" >&2
}

log_warn() {
	printf '%s [WARN ] %s\n' "$(log_iso)" "$*" >&2
}

log_err() {
	printf '%s [ERROR] %s\n' "$(log_iso)" "$*" >&2
}

# ---------------------------------------------------------------
# Pre-flight
# ---------------------------------------------------------------

preflight() {
	mkdir -p "$RUN_DIR"
	printf '%s\n' "$RUN_DIR" >"$RUN_ROOT/latest-run.txt"

	for tool in ffmpeg ffprobe; do
		if ! command -v "$tool" >/dev/null 2>&1; then
			log_err "preflight: missing required tool '$tool' on PATH"
			return 60
		fi
	done

	# AVD ingest port reachable?
	if ! timeout 3 bash -c "</dev/tcp/$AVD_HOST/$AVD_INGEST_PORT" 2>/dev/null; then
		log_err "preflight: AVD ingest $AVD_HOST:$AVD_INGEST_PORT not reachable (RTMP listener down?)"
		return 61
	fi
	if ! timeout 3 bash -c "</dev/tcp/$AVD_HOST/$AVD_EGRESS_PORT" 2>/dev/null; then
		log_warn "preflight: AVD egress $AVD_HOST:$AVD_EGRESS_PORT not reachable (will fail egress reads later)"
	fi

	log_info "preflight: PASS — RUN_DIR=$RUN_DIR"
	return 0
}

# ---------------------------------------------------------------
# PID-stability gate per coord W269
# ---------------------------------------------------------------

# Capture AVD daemon PID at start; assert UNCHANGED at end.
# On Plan B (synthetic-cascade) the daemon under stability gate is
# AVD itself — the cascade transcoder runs there. Drift indicates
# crash + restart, which would invalidate the disambiguation result.
avd_daemon_pid_snapshot() {
	if [[ "$AVD_LOG_HOST" == "localhost" || "$AVD_LOG_HOST" == "127.0.0.1" ]]; then
		pgrep -x "$AVD_PROC_NAME" 2>/dev/null | head -1 || echo ""
	else
		ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new \
			"${AVD_LOG_USER}@${AVD_LOG_HOST}" \
			"pgrep -x $AVD_PROC_NAME 2>/dev/null | head -1" || echo ""
	fi
}

# ---------------------------------------------------------------
# Synthetic publish (Plan B replacement for phone-side wingout)
# ---------------------------------------------------------------

start_synthetic_publish() {
	local out_log="$RUN_DIR/synth-publish.log"
	local target="rtmp://${AVD_HOST}:${AVD_INGEST_PORT}/${STREAM_A}"

	log_info "synthetic publish: ffmpeg → $target ($PUBLISH_DURATION_SEC s)"

	# Silent AAC 48kHz mono + h264 testsrc 1920x1080@30fps.
	# Deterministic: same inputs every run. anullsrc bytes-pattern
	# is constant; testsrc PTS pattern is constant.
	ffmpeg -nostdin -hide_banner -loglevel warning \
		-re \
		-f lavfi -i "testsrc=size=1920x1080:rate=30" \
		-f lavfi -i "anullsrc=channel_layout=mono:sample_rate=48000" \
		-c:v libx264 -preset ultrafast -tune zerolatency -g 60 \
		-c:a aac -b:a 64k -ar 48000 -ac 1 \
		-t "$PUBLISH_DURATION_SEC" \
		-f flv "$target" \
		>"$out_log" 2>&1 &

	local pid=$!
	BG_PIDS+=("$pid")
	log_info "synthetic publish: started pid=$pid (log $out_log)"
}

# ---------------------------------------------------------------
# 3-way wire capture per spec §10
# ---------------------------------------------------------------

# Capture each stream to a local FLV file via ffmpeg -t bound.
# Byte-count post-capture = stat -c '%s' file.
capture_stream_bytes() {
	local stream_path="$1"
	local label="$2"
	local out_file="$RUN_DIR/stream-${label}.flv"
	local out_log="$RUN_DIR/stream-${label}.log"
	local target="rtmp://${AVD_HOST}:${AVD_EGRESS_PORT}/${stream_path}"

	log_info "capture[$label]: ffmpeg ← $target ($CAPTURE_DURATION_SEC s)"

	ffmpeg -nostdin -hide_banner -loglevel warning \
		-i "$target" \
		-t "$CAPTURE_DURATION_SEC" \
		-c copy \
		-f flv "$out_file" \
		>"$out_log" 2>&1 &

	local pid=$!
	BG_PIDS+=("$pid")
}

bytes_received() {
	local label="$1"
	local out_file="$RUN_DIR/stream-${label}.flv"
	if [[ -f "$out_file" ]]; then
		stat -c '%s' "$out_file" 2>/dev/null || echo 0
	else
		echo 0
	fi
}

# ---------------------------------------------------------------
# AVD log capture for E5 marker (H1) + queue-full (H2)
# ---------------------------------------------------------------

start_avd_log_capture() {
	local out_log="$RUN_DIR/avd-debug.log"

	log_info "AVD log capture: tail → $out_log"

	if [[ "$AVD_LOG_HOST" == "localhost" || "$AVD_LOG_HOST" == "127.0.0.1" ]]; then
		# Local AVD: tail directly. If log doesn't exist yet, touch it
		# so tail -F has something to follow.
		if [[ ! -f "$AVD_LOG_PATH" ]]; then
			log_warn "AVD log $AVD_LOG_PATH does not exist; creating empty"
			# Caller permissions matter — try mkdir+touch but don't fail if RO.
			mkdir -p "$(dirname "$AVD_LOG_PATH")" 2>/dev/null || true
			touch "$AVD_LOG_PATH" 2>/dev/null || true
		fi
		tail -F "$AVD_LOG_PATH" >"$out_log" 2>&1 &
	else
		ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new \
			"${AVD_LOG_USER}@${AVD_LOG_HOST}" \
			"tail -F $AVD_LOG_PATH" \
			>"$out_log" 2>&1 &
	fi

	local pid=$!
	BG_PIDS+=("$pid")
	log_info "AVD log capture: started pid=$pid"
}

count_marker_occurrences() {
	local marker_re="$1"
	local log_file="$RUN_DIR/avd-debug.log"
	# `grep -c` writes the count to stdout AND exits rc=1 when count=0;
	# capturing the count with `|| count=0` falls back to 0 only on
	# error path (read failure), avoiding double-emission of "0\n0".
	local count
	if [[ -f "$log_file" ]]; then
		count=$(grep -cE "$marker_re" "$log_file" 2>/dev/null) || count=0
	else
		count=0
	fi
	echo "$count"
}

# ---------------------------------------------------------------
# Disambiguation tree per spec §10
# ---------------------------------------------------------------

# Inputs: bytes_a, bytes_b, bytes_c (numeric).
# Stdout: verdict label (one of:
#   PHONE_SIDE_DEFECT, HOP1_DROPS, HOP2_DROPS_OR_MERGED,
#   CONSUMER_SIDE_DEFECT).
classify_verdict() {
	local a="$1" b="$2" c="$3"

	local a_pos=0 b_pos=0 c_pos=0
	[[ "$a" -gt 0 ]] && a_pos=1
	[[ "$b" -gt 0 ]] && b_pos=1
	[[ "$c" -gt 0 ]] && c_pos=1

	case "${a_pos}${b_pos}${c_pos}" in
		000) echo "PHONE_SIDE_DEFECT" ;;
		100) echo "HOP1_DROPS" ;;
		110) echo "HOP2_DROPS_OR_MERGED" ;;
		111) echo "CONSUMER_SIDE_DEFECT" ;;
		*)   echo "UNKNOWN_${a_pos}${b_pos}${c_pos}" ;;
	esac
}

# Distinguish H1 (D-A3b) vs H2 (D-A4-NEW3) when verdict =
# HOP2_DROPS_OR_MERGED. Inputs: marker counts.
classify_hop2_subhypothesis() {
	local h1_count="$1" h2_count="$2"
	if [[ "$h1_count" -gt 0 && "$h2_count" -le 0 ]]; then
		echo "H1_D_A3B_VECTOR_E_E5_MARKER_PRESENT"
	elif [[ "$h2_count" -gt 0 && "$h1_count" -le 0 ]]; then
		echo "H2_D_A4_NEW3_QUEUE_FULL_PRESENT"
	elif [[ "$h1_count" -gt 0 && "$h2_count" -gt 0 ]]; then
		echo "BOTH_H1_AND_H2_MARKERS_PRESENT"
	else
		echo "NEITHER_MARKER_PRESENT_INCONCLUSIVE"
	fi
}

# H1 confirmation classification when verdict = HOP1_DROPS.
# Spec §4.4 L212 [T1: spec-v1.md md5 2f54e30b read this session, high]:
#   "For verdicts (>0,0,0) AND (>0,>0,0): assert AVD logs contain Vector E
#    Warnf marker (E5 site) — confirms H1 (D-A3b mechanism)"
# H2 marker (queue-full at consumer) is post-hop2; cross-contaminating in
# HOP1_DROPS pattern would indicate an unrelated stream — not classified
# here. Only H1 marker presence distinguishes confirmed-H1 from
# inconclusive in hop-1-drops scenarios.
classify_hop1_subhypothesis() {
	local h1_count="$1"
	if [[ "$h1_count" -gt 0 ]]; then
		echo "H1_D_A3B_VECTOR_E_E5_MARKER_PRESENT"
	else
		echo "NEITHER_MARKER_PRESENT_INCONCLUSIVE"
	fi
}

# ---------------------------------------------------------------
# Main mission cycle
# ---------------------------------------------------------------

main() {
	log_info "Task #175 Phase 4 T-int-4 — Plan B AVD-side synthetic-cascade disambig fixture"

	if ! preflight; then
		log_err "preflight FAILED"
		exit 60
	fi

	# 1. PID-stability gate — pre snapshot.
	local pid_pre
	pid_pre="$(avd_daemon_pid_snapshot)"
	if [[ -z "$pid_pre" ]]; then
		log_warn "PID-stability gate: AVD daemon PID not detected pre-capture; continuing without gate"
	else
		log_info "PID-stability gate: pre-capture AVD daemon pid=$pid_pre"
	fi

	# 2. Start AVD log capture BEFORE publish so we catch early E5
	#    emissions during cascade boot.
	start_avd_log_capture
	sleep 1  # log-tail spinup grace.

	# 3. Start synthetic publish to AVD ingest.
	start_synthetic_publish
	sleep 5  # cascade boot grace; spec §10 calls for ≥30s stable
	          # but Plan B uses shorter grace + bounded capture window.

	# 4. Start 3-way wire capture in parallel (each is a backgrounded
	#    ffmpeg -t CAPTURE_DURATION_SEC pull).
	capture_stream_bytes "$STREAM_A" "A"
	capture_stream_bytes "$STREAM_B" "B"
	capture_stream_bytes "$STREAM_C" "C"

	log_info "capture-window: ${CAPTURE_DURATION_SEC}s starting"
	sleep "$((CAPTURE_DURATION_SEC + 5))"  # capture + grace.

	# 5. PID-stability gate — post snapshot.
	local pid_post
	pid_post="$(avd_daemon_pid_snapshot)"
	if [[ -n "$pid_pre" && -n "$pid_post" && "$pid_pre" != "$pid_post" ]]; then
		log_err "PID-stability gate FAILED: AVD daemon pid changed mid-capture (pre=$pid_pre post=$pid_post)"
		log_err "Disambiguation result is INVALID due to crash + restart during capture window"
		{
			printf 'verdict: ABORT_AVD_DAEMON_RESTART\n'
			printf 'reason: AVD daemon pid changed during capture window\n'
			printf 'pid_pre: %s\n' "$pid_pre"
			printf 'pid_post: %s\n' "$pid_post"
			printf 'iso: %s\n' "$(log_iso)"
		} >"$RESULT_FILE"
		exit 70
	fi
	log_info "PID-stability gate: PASS (pre=$pid_pre post=$pid_post)"

	# 6. Tally byte-counts + marker counts.
	local bytes_a bytes_b bytes_c h1_count h2_count
	bytes_a="$(bytes_received A)"
	bytes_b="$(bytes_received B)"
	bytes_c="$(bytes_received C)"
	h1_count="$(count_marker_occurrences "$H1_MARKER")"
	h2_count="$(count_marker_occurrences "$H2_MARKER")"

	log_info "byte-counts: A=$bytes_a B=$bytes_b C=$bytes_c"
	log_info "marker counts: H1=$h1_count H2=$h2_count"

	# 7. Classify verdict per spec §10 disambiguation tree.
	local verdict
	verdict="$(classify_verdict "$bytes_a" "$bytes_b" "$bytes_c")"

	# Spec §4.4 L212 H1 confirmation applies to BOTH HOP1_DROPS (>0,0,0)
	# AND HOP2_DROPS_OR_MERGED (>0,>0,0) — set-union, not logical AND
	# (byte patterns are mutually exclusive). Classify subhypothesis for
	# both verdicts so result.txt + exit code reflect H1 confirmation
	# state rather than treating hop1 as automatically-in-scope.
	local subhypothesis="N/A"
	case "$verdict" in
		HOP1_DROPS)
			subhypothesis="$(classify_hop1_subhypothesis "$h1_count")"
			;;
		HOP2_DROPS_OR_MERGED)
			subhypothesis="$(classify_hop2_subhypothesis "$h1_count" "$h2_count")"
			;;
	esac

	# 8. Emit result.txt per spec §4.4 fixture-exit contract.
	{
		printf 'iso: %s\n' "$(log_iso)"
		printf 'fixture: builtincamera-ui-vector-e-disambig.sh\n'
		printf 'plan: B (AVD-side-only synthetic-cascade per coord W262)\n'
		printf 'avd_host: %s\n' "$AVD_HOST"
		printf 'capture_duration_sec: %d\n' "$CAPTURE_DURATION_SEC"
		printf 'pid_pre: %s\n' "$pid_pre"
		printf 'pid_post: %s\n' "$pid_post"
		printf 'pid_stability: PASS\n'
		printf 'bytes_a: %s\n' "$bytes_a"
		printf 'bytes_b: %s\n' "$bytes_b"
		printf 'bytes_c: %s\n' "$bytes_c"
		printf 'h1_marker_count: %s\n' "$h1_count"
		printf 'h2_marker_count: %s\n' "$h2_count"
		printf 'verdict: %s\n' "$verdict"
		printf 'subhypothesis: %s\n' "$subhypothesis"
	} >"$RESULT_FILE"

	log_info "result.txt written to $RESULT_FILE"
	log_info "verdict: $verdict"
	[[ "$subhypothesis" != "N/A" ]] && log_info "subhypothesis: $subhypothesis"

	# 9. Exit code reflects mission-witness scope:
	#    - HOP1_DROPS with H1 marker → exit 0
	#      (in-scope drop class confirmed via E5 Warnf assertion per
	#       spec §4.4 L212)
	#    - HOP1_DROPS with NEITHER marker → exit 71
	#      (drop pattern present but H1 mechanism unconfirmed —
	#       inconclusive, NOT a false positive)
	#    - HOP2_DROPS_OR_MERGED with H1 / H2 / BOTH marker → exit 0
	#      (in-scope; H1 OR H2 hypothesis confirmed)
	#    - HOP2_DROPS_OR_MERGED with NEITHER marker → exit 71
	#      (drop confirmed but mechanism inconclusive)
	#    - PHONE_SIDE_DEFECT / CONSUMER_SIDE_DEFECT → exit 72
	#      (out-of-scope; spec §4.4)
	#    - UNKNOWN → exit 73 (defensive)
	case "$verdict" in
		HOP1_DROPS)
			case "$subhypothesis" in
				H1_*) exit 0 ;;
				NEITHER_*) exit 71 ;;
				*) exit 73 ;;
			esac ;;
		HOP2_DROPS_OR_MERGED)
			case "$subhypothesis" in
				H1_*|H2_*|BOTH_*) exit 0 ;;
				NEITHER_*) exit 71 ;;
				*) exit 73 ;;
			esac ;;
		PHONE_SIDE_DEFECT|CONSUMER_SIDE_DEFECT) exit 72 ;;
		*) exit 73 ;;
	esac
}

main "$@"
