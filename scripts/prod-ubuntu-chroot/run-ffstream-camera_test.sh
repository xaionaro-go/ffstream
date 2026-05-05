#!/bin/bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/run-ffstream-camera.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

tmp_dir=$(mktemp -d)
cleanup() {
	rm -rf "$tmp_dir"
}
trap cleanup EXIT

bin_dir="$tmp_dir/bin"
mkdir -p "$bin_dir"

cat > "$bin_dir/fix-mic" <<'STUB'
#!/bin/bash
exit 0
STUB
cat > "$bin_dir/prlimit" <<'STUB'
#!/bin/bash
while [ "$#" -gt 0 ]; do
	case "$1" in
		--)
			shift
			exec "$@"
			;;
		--as=*)
			shift
			;;
		*)
			exec "$@"
			;;
	esac
done
exit 0
STUB
cat > "$bin_dir/nice" <<'STUB'
#!/bin/bash
if [ "${1:-}" = "-n" ]; then
	shift 2
fi
exec "$@"
STUB
cat > "$bin_dir/taskset" <<'STUB'
#!/bin/bash
if [ "${1:-}" = "-c" ]; then
	shift 2
fi
exec "$@"
STUB
cat > "$bin_dir/termux-root" <<'STUB'
#!/bin/bash
exec "$@"
STUB
cat > "$bin_dir/ffstream-stub" <<'STUB'
#!/bin/bash
if [ -n "${FFSTREAM_STUB_ARGS_LOG:-}" ]; then
	: > "$FFSTREAM_STUB_ARGS_LOG"
	for arg in "$@"; do
		printf '%s\n' "$arg" >> "$FFSTREAM_STUB_ARGS_LOG"
	done
fi
case "${FFSTREAM_STUB_MODE:-no-marker}" in
	marker)
		mkdir -p "$(dirname "$FFSTREAM_END_MARKER_FILE")"
		printf 'intentional\n' > "$FFSTREAM_END_MARKER_FILE"
		;;
	no-marker)
		;;
	*)
		echo "unexpected FFSTREAM_STUB_MODE=$FFSTREAM_STUB_MODE" >&2
		exit 64
		;;
esac
exit "${FFSTREAM_STUB_STATUS:-0}"
STUB
chmod +x "$bin_dir"/*

streaming_env="$tmp_dir/streaming.env"
cat > "$streaming_env" <<'EOF'
FFSTREAM_LOG_LEVEL=info
ACODEC=aac
EOF

export PATH="$bin_dir:$PATH"
export FFSTREAM_CAMERA_PATH="$PATH"
export FFSTREAM_CAMERA_STREAMING_ENV_FILE="$streaming_env"
export FFSTREAM_BIN="$bin_dir/ffstream-stub"
export FFSTREAM_BIN_RUNNER=termux-root
export FFSTREAM_END_MARKER_FILE="$tmp_dir/end-marker"
export FFSTREAM_END_MARKER_FILE_CHROOT="$tmp_dir/end-marker"
export FFSTREAM_CAMERA_LOG_FILE="$tmp_dir/ffstream-camera.log"
export FFSTREAM_RAM_CAP_AS=unlimited
export FFSTREAM_STUB_ARGS_LOG="$tmp_dir/ffstream-args.log"

run_case() {
	local name=$1
	shift
	rm -f "$FFSTREAM_END_MARKER_FILE" "$FFSTREAM_CAMERA_LOG_FILE"
	(
		"$@"
	)
}

assert_config_failure() {
	local name=$1
	local env_file=$2
	local diagnostic_pattern=$3

	rm -f "$FFSTREAM_END_MARKER_FILE" "$FFSTREAM_CAMERA_LOG_FILE" "$FFSTREAM_STUB_ARGS_LOG"
	set +e
	env -u ACODEC -u FFSTREAM_LOG_LEVEL \
		FFSTREAM_CAMERA_STREAMING_ENV_FILE="$env_file" \
		FFSTREAM_STUB_MODE=marker \
		FFSTREAM_STUB_STATUS=0 \
		"$script" > "$tmp_dir/$name.out" 2> "$tmp_dir/$name.err"
	status=$?
	set -e
	if [ "$status" -ne 78 ]; then
		echo "$name stderr:" >&2
		cat "$tmp_dir/$name.err" >&2
		fail "$name must exit 78 so the supervisor stops; got $status"
	fi
	if [ -e "$FFSTREAM_STUB_ARGS_LOG" ]; then
		fail "$name must fail before launching ffstream"
	fi
	if ! grep -Eq "$diagnostic_pattern" "$tmp_dir/$name.err"; then
		echo "$name stderr:" >&2
		cat "$tmp_dir/$name.err" >&2
		fail "$name must report the config error"
	fi
}

missing_acodec_env="$tmp_dir/missing-acodec.env"
cat > "$missing_acodec_env" <<'EOF'
FFSTREAM_LOG_LEVEL=info
EOF
invalid_env="$tmp_dir/invalid-streaming.env"
cat > "$invalid_env" <<'EOF'
FFSTREAM_LOG_LEVEL=info
ACODEC=aac
if
EOF

assert_config_failure missing_env "$tmp_dir/missing.env" "streaming.env|config file"
assert_config_failure missing_acodec "$missing_acodec_env" "ACODEC"
assert_config_failure invalid_env "$invalid_env" "invalid|syntax"

set +e
run_case no_marker_zero env FFSTREAM_STUB_MODE=no-marker FFSTREAM_STUB_STATUS=0 "$script" \
	> "$tmp_dir/no-marker.out" 2> "$tmp_dir/no-marker.err"
status=$?
set -e
if [ "$status" -eq 0 ]; then
	fail "raw status 0 without End marker must not be supervisor-clean"
fi
if ! grep -q "without an End marker" "$tmp_dir/no-marker.err"; then
	cat "$tmp_dir/no-marker.err" >&2
	fail "missing diagnostic for status 0 without marker"
fi

run_case marker_zero env FFSTREAM_STUB_MODE=marker FFSTREAM_STUB_STATUS=0 "$script" \
	> "$tmp_dir/marker.out" 2> "$tmp_dir/marker.err" || fail "End marker should produce clean exit"
if [ -e "$FFSTREAM_END_MARKER_FILE" ]; then
	fail "consumed End marker must be removed"
fi
if grep -qx -- "-i" "$FFSTREAM_STUB_ARGS_LOG"; then
	fail "idle ffstream-camera launch must not include built-in camera/mic inputs"
fi
if grep -qx -- "-f" "$FFSTREAM_STUB_ARGS_LOG"; then
	fail "idle ffstream-camera launch must not include output format/publish target"
fi
if grep -Eq "android_camera|android_microphone|rtmp://|rtmps://|srt://" "$FFSTREAM_STUB_ARGS_LOG"; then
	fail "idle ffstream-camera launch must not include built-in inputs or publish URLs"
fi
if ! grep -qx -- "-listen_control" "$FFSTREAM_STUB_ARGS_LOG"; then
	fail "idle ffstream-camera launch must expose the control socket for later Activate"
fi
if ! grep -qx -- "tcp:127.0.0.1:3594" "$FFSTREAM_STUB_ARGS_LOG"; then
	fail "idle ffstream-camera launch must listen on the camera control port"
fi

printf 'stale\n' > "$FFSTREAM_END_MARKER_FILE"
set +e
env FFSTREAM_STUB_MODE=no-marker FFSTREAM_STUB_STATUS=0 "$script" \
	> "$tmp_dir/stale.out" 2> "$tmp_dir/stale.err"
status=$?
set -e
if [ "$status" -eq 0 ]; then
	fail "stale marker from before launch must not classify this run as clean"
fi
if [ -e "$FFSTREAM_END_MARKER_FILE" ]; then
	fail "stale marker must be removed before launching ffstream"
fi
