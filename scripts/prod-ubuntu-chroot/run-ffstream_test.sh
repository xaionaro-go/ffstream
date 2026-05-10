#!/bin/bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/run-ffstream.sh"

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

canonical_ffstream=/data/user/0/com.termux/files/usr/bin/ffstream
stub_ffstream="$tmp_dir/ffstream-stub"

cat > "$bin_dir/fix-mic" <<'STUB'
#!/bin/bash
exit 0
STUB
cat > "$bin_dir/prlimit" <<'STUB'
#!/bin/bash
if [ -n "${PRLIMIT_ARGS_LOG:-}" ]; then
	: > "$PRLIMIT_ARGS_LOG"
fi
while [ "$#" -gt 0 ]; do
	case "$1" in
		--as=*)
			if [ -n "${PRLIMIT_ARGS_LOG:-}" ]; then
				printf '%s\n' "$1" >> "$PRLIMIT_ARGS_LOG"
			fi
			shift
			;;
		--)
			shift
			exec "$@"
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
if [ -n "${TERMUX_ROOT_ARGS_LOG:-}" ]; then
	printf '%s\n' "$*" >> "$TERMUX_ROOT_ARGS_LOG"
fi
case "${1:-}" in
	test)
		if [ "${2:-}" != "-x" ]; then
			echo "unexpected test args: $*" >&2
			exit 64
		fi
		if [ "${3:-}" = "$EXPECTED_CANONICAL_FFSTREAM" ] \
				&& [ -x "$FFSTREAM_CANONICAL_STUB" ]; then
			exit 0
		fi
		test -x "${3:-}"
		;;
	"$EXPECTED_CANONICAL_FFSTREAM")
		shift
		exec "$FFSTREAM_CANONICAL_STUB" "$@"
		;;
	*)
		exec "$@"
		;;
esac
STUB
cat > "$stub_ffstream" <<'STUB'
#!/bin/bash
if [ -n "${FFSTREAM_STUB_ARGS_LOG:-}" ]; then
	: > "$FFSTREAM_STUB_ARGS_LOG"
	for arg in "$@"; do
		printf '%s\n' "$arg" >> "$FFSTREAM_STUB_ARGS_LOG"
	done
fi
exit "${FFSTREAM_STUB_STATUS:-0}"
STUB
chmod +x "$bin_dir"/* "$stub_ffstream"

streaming_env="$tmp_dir/streaming.env"
cat > "$streaming_env" <<'EOF'
WIDTH=1920
HEIGHT=1080
ACODEC=aac
VCODEC=av1_mediacodec
FRAMERATE=30
KEYFRAME_INTERVAL=1
ADDR_HOME=127.0.0.1
FFSTREAM_LOG_LEVEL=info
FFSTREAM_AUTO_BITRATE=true
FFSTREAM_AUTOBITRATE_MAX_HEIGHT=1080
FFSTREAM_AUTOBITRATE_MIN_HEIGHT=180
FFSTREAM_AUTO_BYPASS=false
FFSTREAM_RAM_CAP_AS=unlimited
FFSTREAM_GOMEMLIMIT=2GiB
EOF

export PATH="$bin_dir:$PATH"
export FFSTREAM_PATH="$PATH"
export FFSTREAM_STREAMING_ENV_FILE="$streaming_env"
export FFSTREAM_BIN_RUNNER=termux-root
export FFSTREAM_LOG_FILE="$tmp_dir/ffstream.log"
export EXPECTED_CANONICAL_FFSTREAM="$canonical_ffstream"
export FFSTREAM_CANONICAL_STUB="$stub_ffstream"
export FFSTREAM_STUB_ARGS_LOG="$tmp_dir/ffstream-args.log"
export TERMUX_ROOT_ARGS_LOG="$tmp_dir/termux-root-args.log"
export PRLIMIT_ARGS_LOG="$tmp_dir/prlimit-args.log"

rm -f "$FFSTREAM_STUB_ARGS_LOG" "$TERMUX_ROOT_ARGS_LOG" "$PRLIMIT_ARGS_LOG"
"$script" > "$tmp_dir/default.out" 2> "$tmp_dir/default.err" \
	|| fail "default canonical launch should succeed"
if ! grep -qx -- "test -x $canonical_ffstream" "$TERMUX_ROOT_ARGS_LOG"; then
	cat "$TERMUX_ROOT_ARGS_LOG" >&2
	fail "run-ffstream.sh must validate the canonical ffstream path via the runner"
fi
if ! grep -q "^$canonical_ffstream -v info " "$TERMUX_ROOT_ARGS_LOG"; then
	cat "$TERMUX_ROOT_ARGS_LOG" >&2
	fail "run-ffstream.sh must launch the canonical ffstream path by default"
fi
if ! awk '
	prev == "-c:v" && $0 == "av1_mediacodec" { found = 1 }
	{ prev = $0 }
	END { exit found ? 0 : 1 }
' "$FFSTREAM_STUB_ARGS_LOG"; then
	cat "$FFSTREAM_STUB_ARGS_LOG" >&2
	fail "run-ffstream.sh must launch mediamtx ffstream with -c:v av1_mediacodec"
fi
if ! grep -qx -- "--as=unlimited" "$PRLIMIT_ARGS_LOG"; then
	cat "$PRLIMIT_ARGS_LOG" >&2
	fail "run-ffstream.sh must pass the validated AS cap to prlimit"
fi

rm -f "$FFSTREAM_STUB_ARGS_LOG" "$TERMUX_ROOT_ARGS_LOG" "$PRLIMIT_ARGS_LOG"
set +e
env FFSTREAM_BIN=/tmp/off-mission-ffstream "$script" \
	> "$tmp_dir/ignored-bin.out" 2> "$tmp_dir/ignored-bin.err"
status=$?
set -e
if [ "$status" -ne 0 ]; then
	cat "$tmp_dir/ignored-bin.err" >&2
	fail "FFSTREAM_BIN from the environment must be ignored, not treated as production configuration; got $status"
fi
if grep -q "/tmp/off-mission-ffstream" "$TERMUX_ROOT_ARGS_LOG"; then
	cat "$TERMUX_ROOT_ARGS_LOG" >&2
	fail "run-ffstream.sh must not pass environment FFSTREAM_BIN to the runner"
fi
if ! grep -q "^$canonical_ffstream -v info " "$TERMUX_ROOT_ARGS_LOG"; then
	cat "$TERMUX_ROOT_ARGS_LOG" >&2
	fail "run-ffstream.sh must still launch the canonical ffstream path when FFSTREAM_BIN is set"
fi

rm -f "$FFSTREAM_STUB_ARGS_LOG" "$TERMUX_ROOT_ARGS_LOG" "$PRLIMIT_ARGS_LOG"
set +e
non_av1_env="$tmp_dir/non-av1.env"
sed 's/VCODEC=av1_mediacodec/VCODEC=h265_mediacodec/' \
	"$streaming_env" > "$non_av1_env"
env FFSTREAM_STREAMING_ENV_FILE="$non_av1_env" "$script" \
	> "$tmp_dir/non-av1.out" 2> "$tmp_dir/non-av1.err"
status=$?
set -e
if [ "$status" -ne 78 ]; then
	cat "$tmp_dir/non-av1.err" >&2
	fail "non-AV1 VCODEC must exit 78; got $status"
fi
if [ -e "$FFSTREAM_STUB_ARGS_LOG" ]; then
	fail "non-AV1 VCODEC must fail before launching ffstream"
fi
if ! grep -q "VCODEC" "$tmp_dir/non-av1.err" \
		|| ! grep -q "av1_mediacodec" "$tmp_dir/non-av1.err"; then
	cat "$tmp_dir/non-av1.err" >&2
	fail "non-AV1 diagnostic must mention VCODEC and av1_mediacodec"
fi

rm -f "$FFSTREAM_STUB_ARGS_LOG" "$TERMUX_ROOT_ARGS_LOG" "$PRLIMIT_ARGS_LOG"
set +e
low_as_env="$tmp_dir/low-as.env"
sed 's/FFSTREAM_RAM_CAP_AS=unlimited/FFSTREAM_RAM_CAP_AS=21474836480/' \
	"$streaming_env" > "$low_as_env"
env FFSTREAM_STREAMING_ENV_FILE="$low_as_env" "$script" \
	> "$tmp_dir/low-as.out" 2> "$tmp_dir/low-as.err"
status=$?
set -e
if [ "$status" -ne 78 ]; then
	cat "$tmp_dir/low-as.err" >&2
	fail "insufficient FFSTREAM_RAM_CAP_AS must exit 78; got $status"
fi
if [ -e "$FFSTREAM_STUB_ARGS_LOG" ]; then
	fail "insufficient AS cap must fail before launching ffstream"
fi
if ! grep -q "FFSTREAM_RAM_CAP_AS" "$tmp_dir/low-as.err"; then
	cat "$tmp_dir/low-as.err" >&2
	fail "insufficient AS cap must report FFSTREAM_RAM_CAP_AS"
fi
