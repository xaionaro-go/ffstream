#!/bin/bash
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/loop-run-ffstream.sh"

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

cat > "$bin_dir/flock" <<'STUB'
#!/bin/bash
if [ "${1:-}" = "-n" ]; then
	exit 0
fi
exec /usr/bin/flock "$@"
STUB
cat > "$bin_dir/run-ffstream.sh" <<'STUB'
#!/bin/bash
printf 'run\n' >> "$RUN_LOG"
exit 78
STUB
chmod +x "$bin_dir"/*

export PATH="$bin_dir:$PATH"
export FFSTREAM_SUPERVISOR_LOCK_FILE="$tmp_dir/ffstream-supervisor.flock"
export FFSTREAM_RUNNER="$bin_dir/run-ffstream.sh"
export RUN_LOG="$tmp_dir/run.log"

set +e
"$script" > "$tmp_dir/loop.out" 2> "$tmp_dir/loop.err"
status=$?
set -e
if [ "$status" -ne 78 ]; then
	cat "$tmp_dir/loop.err" >&2
	fail "run-ffstream setup failure status 78 must stop the supervisor; got $status"
fi
if [ "$(wc -l < "$RUN_LOG")" -ne 1 ]; then
	cat "$RUN_LOG" >&2
	fail "supervisor must not loop after terminal setup status 78"
fi
if ! grep -q "unrecoverable status 78" "$tmp_dir/loop.err"; then
	cat "$tmp_dir/loop.err" >&2
	fail "supervisor must report terminal setup status"
fi
