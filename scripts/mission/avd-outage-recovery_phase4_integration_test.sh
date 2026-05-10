#!/usr/bin/env bash
set -Eeuo pipefail

# Phase 4 integration tests for Task #122 (F-task116-1) — 10 integration
# tests covering the cleanup() trap on EXIT/INT/TERM/HUP + pkill-anchored-
# to-$tmp_dir + KEEP_TMP fold-in fix landed at canonical
# scripts/mission/avd-outage-recovery_test.sh.
#
# Spec source: ~/tmp/task122-phase4-test-spec-2026-05-07.md
#   md5 9106850c8a7570b94ce6bfccaae3312e (verified pre-implementation).
# Test-designer-1 spec + coord Q1-Q4 dispositions absorbed.
#
# Coverage criteria (per coord dispatch):
#   1. Cross-task boundaries          — T-i1, T-i9, T-i10
#   2. Real-fixture-path coverage     — T-i2, T-i8
#   3. Cleanup() + mission-spine      — T-i6 (mock-harness chain per Q3)
#   4. A/B-differential preservation  — T-i7 (snapshot-copy per Q4)
#   5. Failure-routing validation     — T-i1 + T-i3 + T-i4 + T-i5 + T-i6
#                                       (5 paths: natural exit, INT, TERM,
#                                        HUP, fail() short-circuit)
#
# Test-bound exemption: Coord guideline of "≤30s wall-clock per test" cannot
# be met for fixture-driven tests because canonical avd-outage-recovery_test.sh
# completes in ~34s end-to-end (9 recovery tests + 6 unit tests). Tests that
# require a full fixture run (T-i1, T-i2, T-i6, T-i7, T-i8, T-i10) are
# documented as fixture-bounded; their wall-clock is determinitic (no clock
# flake), only long. Signal-injection tests (T-i3, T-i4, T-i5) interrupt
# early at ~3s, well within bound. T-i9 is structural (~1s).
#
# Per-test broke-the-code envelope: each T-i_N has a `# Broke-the-code-
# validation:` comment block (path-b per memory rule). Empirical broke-the-
# code A/B logs (path-a) captured at ~/tmp/task122-phase4-broke-the-code/
# at SUBMIT time. Cross-test coverage matrix (post-empirical):
#
#   M1 (remove pkill from cleanup body) — primary load-bearing mutation;
#       FAILs T-i1, T-i2/a, T-i3, T-i4, T-i5, T-i6, T-i7, T-i8, T-i10
#       (9 of 10 substantive tests). T-i9 PASSes (structural baseline).
#       Empirical: M1-no-pkill-full.log.
#   M2 (move pkill INSIDE the [-z KEEP_TMP] guard) — couples pkill to rm-rf;
#       FAILs T-i2/a (KEEP_TMP=1 sub-test) via fixture's own T-u6 unit-test
#       chain. Empirical: M2-pkill-coupled-T-i2.log.
#   M3 (remove INT from trap install) — uniquely catches T-i3 via bash's
#       SIGINT-specific exit-status quirk (no INT trap → bash terminates
#       with status 0 after EXIT-trap rm-rf returns 0; canonical with INT
#       trap exits non-zero from sed-on-missing-tmp_dir). M3 does NOT break
#       T-i4/T-i5 because SIGTERM/SIGHUP exit with 143/129 (signal status
#       preserved). Empirical: M3-T-i3-no-INT-trap.log.
#   M4 (remove TERM from trap install) — does NOT break T-i4 (EXIT trap
#       covers SIGTERM-default-termination; orphans still reaped). Documented
#       empirical finding; load-bearing mutation for T-i4 is M1.
#       Empirical: M4-T-i4-no-TERM-trap.log (passes — finding).
#   M5 (remove HUP from trap install) — does NOT break T-i5, by symmetry
#       with M4. Load-bearing mutation for T-i5 is M1.
#       Empirical: M5-T-i5-no-HUP-trap.log (passes — finding).
#   M7 (add scribble-write inside cleanup body to AB_EVIDENCE_DIR-pointed
#       path) — FAILs T-i7 via md5 mismatch in snapshot-copy assertion.
#       Empirical: M7-evidence-corruption-T-i7.log.
#   M10 (modify canonical without re-vendoring) — FAILs T-i10 via md5
#       inequality. Empirical: M10-canonical-drift-T-i10.log.
#
# T-i9 (production-script structural baseline) has no applicable mutation
# in production script (no trap to break, no mock-spawn pattern in
# production). Path-b commentary documents the structural invariant.
#
# T-i8 (concurrent isolation): the spec-mentioned anchor-escape mutation
# (`${tmp_dir//./\\.}/bin/avd` → `${tmp_dir}/bin/avd`) has theoretical-only
# failure mode under mktemp 6-char entropy + path uniqueness; M1 catches it
# instead via assertion (a) "fixture exited non-zero" (cross-test catch).
# Path-b commentary in T-i8 documents.

# ----- Globals -----------------------------------------------------------

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fixture="$script_dir/avd-outage-recovery_test.sh"
production="$script_dir/avd-outage-recovery.sh"
activation_fixture="$script_dir/builtincamera-ui-activation_test.sh"
deactivation_fixture="$script_dir/builtincamera-ui-deactivation_test.sh"

# Wingout-vendored copy path for T-i10 parity check (override-able for CI).
wingout_vendored="${WINGOUT_VENDORED:-$HOME/go/src/github.com/xaionaro-go/wingout/import/ffstream/scripts/mission/avd-outage-recovery_test.sh}"

# Optional executor-2 evidence-artifact path for T-i7 (override-able).
ab_evidence_dir="${AB_EVIDENCE_DIR:-$HOME/tmp/task122-ab-evidence}"

# Pre-condition gate: required executables.
[ -x "$fixture" ] || { echo "FAIL: fixture not executable: $fixture" >&2; exit 1; }
[ -x "$production" ] || { echo "FAIL: production not executable: $production" >&2; exit 1; }
[ -x "$activation_fixture" ] || { echo "FAIL: activation fixture not executable: $activation_fixture" >&2; exit 1; }

# Integration-level scratch root.
integration_tmp="$(mktemp -d -t task122-phase4-integration.XXXXXX)"

PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0

# Integration-level cleanup: best-effort reap of any fixture-spawned orphans
# under our integration_tmp tree (defense-in-depth even if a fixture's own
# cleanup() failed during a test), then remove integration_tmp. Mirrors the
# fixture's literal-dot-escape pattern + KEEP_TMP fold-in.
integration_cleanup() {
	trap - EXIT INT TERM HUP
	# Reap any orphans under integration_tmp (any depth) anchored to our tree.
	pkill -KILL -f "${integration_tmp//./\\.}/.*/bin/avd" 2>/dev/null || true
	if [ -z "${KEEP_TMP:-}" ]; then
		rm -rf "$integration_tmp"
	fi
}
trap integration_cleanup EXIT INT TERM HUP

# ----- Output helpers ----------------------------------------------------

pass() { PASS_COUNT=$((PASS_COUNT + 1)); printf 'PASS: %s\n' "$1"; }
fail() { FAIL_COUNT=$((FAIL_COUNT + 1)); printf 'FAIL: %s\n' "$1" >&2; }
skip() { SKIP_COUNT=$((SKIP_COUNT + 1)); printf 'SKIP: %s — %s\n' "$1" "$2"; }

# ----- Fixture invocation helpers ----------------------------------------

# run_fixture_scoped_tmpdir: invoke fixture (or override via FIXTURE_OVERRIDE)
# with TMPDIR scoped to a known parent under integration_tmp so the fixture's
# `mktemp -d` lands in our tree + we can reason about post-exit pgrep targets.
# Globals set: $captured_fixture_tmp_root, $captured_fixture_status,
#   $captured_fixture_stdout, $captured_fixture_stderr.
# Args: <test_label> [extra-env-args...]
run_fixture_scoped_tmpdir() {
	local test_label="$1"
	shift
	local fixture_to_invoke="${FIXTURE_OVERRIDE:-$fixture}"
	local fixture_tmp_root="$integration_tmp/fixtures/$test_label"
	mkdir -p "$fixture_tmp_root"
	captured_fixture_stdout="$fixture_tmp_root/stdout.log"
	captured_fixture_stderr="$fixture_tmp_root/stderr.log"

	set +e
	env TMPDIR="$fixture_tmp_root" "$@" "$fixture_to_invoke" \
		>"$captured_fixture_stdout" 2>"$captured_fixture_stderr"
	captured_fixture_status=$?
	set -e
	captured_fixture_tmp_root="$fixture_tmp_root"
}

# spawn_fixture_background: launch fixture in background; capture its PID for
# subsequent signal injection. TMPDIR scoped same as run_fixture_scoped_tmpdir.
#
# Bash invariant: backgrounding via `&` causes the parent shell to set SIGINT
# (and SIGQUIT) to SIG_IGN for the child process. Per POSIX + bash(1)
# ("Signals ignored on entry to a non-interactive shell cannot be trapped or
# reset"), the fixture's `trap cleanup INT` install is then silently no-op'd
# inside the fixture's bash interpreter — leaving SIGINT genuinely
# uncatchable. SIGTERM/SIGHUP are not in the inherited SIG_IGN set so the
# fixture's trap installs for those work as expected.
#
# Workaround: invoke fixture via a python3 launcher that resets SIGINT (and
# SIGQUIT) to SIG_DFL before exec'ing bash. The exec'd bash sees SIGINT as
# catchable on entry, so the fixture's `trap cleanup INT` install fires
# normally — making T-i3 SIGINT-injection assertions reach the trap surface.
# Verified empirically: pre-launcher /proc/<pid>/status SigIgn=0x6 (SIGINT
# ignored); post-launcher SigIgn=0x1000004 (SIGINT not ignored). Used for
# all signal-injection tests (T-i3, T-i4, T-i5) for uniformity even though
# only T-i3 strictly needs it.
#
# Globals set: $bg_fixture_pid, $bg_fixture_tmp_root, $bg_fixture_stdout,
#   $bg_fixture_stderr.
# Args: <test_label> [extra-env-args... — KEY=VALUE form]
spawn_fixture_background() {
	local test_label="$1"
	shift
	local fixture_to_invoke="${FIXTURE_OVERRIDE:-$fixture}"
	local fixture_tmp_root="$integration_tmp/fixtures/$test_label"
	mkdir -p "$fixture_tmp_root"
	bg_fixture_stdout="$fixture_tmp_root/stdout.log"
	bg_fixture_stderr="$fixture_tmp_root/stderr.log"
	bg_fixture_tmp_root="$fixture_tmp_root"

	# Pre-format extra env (KEY=VALUE form) into the env dict via
	# python3's os.environ; argv format: <tmp_root> <KEY=VALUE>... <fixture>.
	python3 -c '
import os, signal, sys
signal.signal(signal.SIGINT, signal.SIG_DFL)
signal.signal(signal.SIGQUIT, signal.SIG_DFL)
env = os.environ.copy()
env["TMPDIR"] = sys.argv[1]
# Remaining args: KEY=VALUE pairs followed by final fixture path.
args = sys.argv[2:]
fixture = args.pop()
for kv in args:
    if "=" not in kv:
        sys.stderr.write("spawn_fixture_background: malformed KV %r\n" % kv)
        sys.exit(2)
    k, v = kv.split("=", 1)
    env[k] = v
os.execvpe(fixture, [fixture], env)
' "$fixture_tmp_root" "$@" "$fixture_to_invoke" \
		>"$bg_fixture_stdout" 2>"$bg_fixture_stderr" &
	bg_fixture_pid="$!"
}

# pgrep_orphans_under: list any /bin/avd processes anchored under a given
# tmp-root tree. Returns 0 if some are found (caller asserts empty/non-empty).
pgrep_orphans_under() {
	local tmp_root="$1"
	pgrep -f "${tmp_root//./\\.}/.*/bin/avd" 2>/dev/null
}

# assert_no_orphans_under: assert pgrep_orphans_under returns empty. On failure,
# emits diagnostic + best-effort reap before returning 1.
assert_no_orphans_under() {
	local tmp_root="$1"
	local label="$2"
	local orphans
	orphans="$(pgrep_orphans_under "$tmp_root" || true)"
	if [ -n "$orphans" ]; then
		echo "  diagnostic: orphans alive under $tmp_root after $label:" >&2
		pgrep -af "${tmp_root//./\\.}/.*/bin/avd" >&2 2>/dev/null || true
		pkill -KILL -f "${tmp_root//./\\.}/.*/bin/avd" 2>/dev/null || true
		return 1
	fi
	return 0
}

# md5_dir_contents: deterministic md5 over all regular file contents under a
# directory (sorted by path; structure md5 includes path names + file md5s).
md5_dir_contents() {
	local dir="$1"
	if [ ! -d "$dir" ]; then
		echo "MISSING:$dir"
		return
	fi
	(cd "$dir" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 md5sum) | md5sum | cut -d' ' -f1
}

# ============================================================================
# T-i1 — External-runner invocation propagates EXIT trap (natural exit)
# ============================================================================

# Coverage criterion 1: Cross-task boundaries (external test runner observes
# fixture's cleanup() effect post-natural-exit).
#
# Broke-the-code-validation (path-b): Revert the fixture's cleanup() body to
# PRE-FIX form (`rm -rf "$tmp_dir"` only — no pkill line at L58). Re-run T-i1.
# Expected: orphan PIDs persist under the fixture's $tmp_dir/bin/avd path
# post-fixture-exit because mocks are held-as-deleted-binary processes that
# survive rm -rf. Empirical artifact captured at
# ~/tmp/task122-phase4-broke-the-code/T-i1-{pre,post}-fix.log at SUBMIT time.
# Confirms pkill-with-anchor at L58 is load-bearing for the substrate-bug
# (PID 970822 class) per Task #116.
#
# Dual-sided assertion:
#   Good: cleanup() fires + reaps fixture-spawned mocks IS happening
#   Bad : orphan mocks IS NOT persisting past natural fixture exit
test_i1_external_runner_natural_exit() {
	local label="T-i1"
	echo "RUNNING: $label — external-runner natural-exit propagates cleanup"
	run_fixture_scoped_tmpdir "t-i1"

	# Assertion (a): fixture exit code propagates correctly (0 = baseline pass).
	if [ "$captured_fixture_status" -ne 0 ]; then
		fail "$label: fixture exited non-zero ($captured_fixture_status); see $captured_fixture_stderr"
		return
	fi
	# Assertion (b): no orphan mocks persist anchored under fixture's tmp_root.
	if ! assert_no_orphans_under "$captured_fixture_tmp_root" "$label fixture exit"; then
		fail "$label: orphan mocks persist post-fixture-exit"
		return
	fi
	# Assertion (c): fixture's $tmp_dir was removed (KEEP_TMP unset → rm-rf path).
	# Sub-trees under the scoped TMPDIR should be empty (mktemp's tmp.XXXXXX gone).
	local survivors
	survivors="$(find "$captured_fixture_tmp_root" -mindepth 1 -maxdepth 1 -type d 2>/dev/null || true)"
	if [ -n "$survivors" ]; then
		fail "$label: fixture's tmp_dir not removed; survivors: $survivors"
		return
	fi
	pass "$label"
}

# ============================================================================
# T-i2 — KEEP_TMP env var integration (3 sub-tests)
# ============================================================================

# Coverage criterion 2: Real-fixture-path coverage under env var combinations.
# Sub-tests: (a) KEEP_TMP=1 → tmp_dir survives + mocks reaped; (b) KEEP_TMP
# unset → tmp_dir removed + mocks reaped (covered by T-i1 baseline; re-asserted
# here for spec faithfulness); (c) KEEP_TMP="" empty → behaves like unset per
# L59 `[ -z "${KEEP_TMP:-}" ]` empty-or-unset semantic.
#
# Broke-the-code-validation (path-b): Move pkill INSIDE the
# `[ -z "${KEEP_TMP:-}" ]` block at L59-61 (mistakenly coupling pkill to
# rm-rf). Re-run sub-test (a) with KEEP_TMP=1: pkill would not fire, mocks
# survive. Empirical artifact at ~/tmp/task122-phase4-broke-the-code/
# T-i2-{pre,post}-fix.log at SUBMIT time. Confirms pkill is unconditional
# (outside the KEEP_TMP guard).
#
# Dual-sided:
#   (a) KEEP_TMP=1 preserves tmp_dir IS happening AND reaps mocks IS happening
#   (b) KEEP_TMP unset removes tmp_dir IS happening AND reaps mocks IS happening
#   (c) KEEP_TMP="" matches unset behavior IS happening
test_i2_keep_tmp_env_var() {
	local label="T-i2"
	echo "RUNNING: $label — KEEP_TMP env var combinations (3 sub-tests)"

	# Sub-test (a): KEEP_TMP=1
	run_fixture_scoped_tmpdir "t-i2-a-keep" KEEP_TMP=1
	if [ "$captured_fixture_status" -ne 0 ]; then
		fail "$label/a (KEEP_TMP=1): fixture status=$captured_fixture_status"
		return
	fi
	# Mocks reaped: pgrep empty
	if ! assert_no_orphans_under "$captured_fixture_tmp_root" "$label/a (KEEP_TMP=1)"; then
		fail "$label/a: mocks NOT reaped under KEEP_TMP=1 (pkill must be unconditional)"
		return
	fi
	# tmp_dir SURVIVES: at least one tmp.XXXXXX dir exists under our scoped TMPDIR
	local kept_dirs
	kept_dirs="$(find "$captured_fixture_tmp_root" -mindepth 1 -maxdepth 1 -type d 2>/dev/null || true)"
	if [ -z "$kept_dirs" ]; then
		fail "$label/a: KEEP_TMP=1 should preserve tmp_dir; no survivor found"
		return
	fi

	# Sub-test (b): KEEP_TMP unset (defense-in-depth re-assert; T-i1 covers same)
	run_fixture_scoped_tmpdir "t-i2-b-unset"
	if [ "$captured_fixture_status" -ne 0 ]; then
		fail "$label/b (unset): fixture status=$captured_fixture_status"
		return
	fi
	if ! assert_no_orphans_under "$captured_fixture_tmp_root" "$label/b (KEEP_TMP unset)"; then
		fail "$label/b: mocks NOT reaped under KEEP_TMP unset"
		return
	fi
	local survivors_b
	survivors_b="$(find "$captured_fixture_tmp_root" -mindepth 1 -maxdepth 1 -type d 2>/dev/null || true)"
	if [ -n "$survivors_b" ]; then
		fail "$label/b: KEEP_TMP unset should remove tmp_dir; survivor: $survivors_b"
		return
	fi

	# Sub-test (c): KEEP_TMP="" empty (matches unset per `[ -z ]`)
	run_fixture_scoped_tmpdir "t-i2-c-empty" KEEP_TMP=""
	if [ "$captured_fixture_status" -ne 0 ]; then
		fail "$label/c (empty): fixture status=$captured_fixture_status"
		return
	fi
	if ! assert_no_orphans_under "$captured_fixture_tmp_root" "$label/c (KEEP_TMP empty)"; then
		fail "$label/c: mocks NOT reaped under KEEP_TMP=\"\""
		return
	fi
	local survivors_c
	survivors_c="$(find "$captured_fixture_tmp_root" -mindepth 1 -maxdepth 1 -type d 2>/dev/null || true)"
	if [ -n "$survivors_c" ]; then
		fail "$label/c: KEEP_TMP=\"\" should match unset (rm-rf); survivor: $survivors_c"
		return
	fi
	pass "$label (3 sub-tests)"
}

# ============================================================================
# T-i3 — SIGINT mid-test injection
# ============================================================================

# Coverage criterion 5(b): Failure-routing — external SIGINT triggers cleanup.
#
# Broke-the-code-validation (path-b): Remove `INT` from the trap install at
# fixture L63 (`trap cleanup EXIT INT TERM HUP` → `trap cleanup EXIT TERM
# HUP`). Re-run T-i3. Expected: SIGINT bypasses fixture's cleanup, fixture
# exits via default SIGINT-handling (130) without running cleanup → mocks
# alive post-exit. Empirical artifact at ~/tmp/task122-phase4-broke-the-
# code/T-i3-{pre,post}-fix.log at SUBMIT time.
#
# Dual-sided:
#   Good: SIGINT triggers cleanup IS happening
#   Bad : orphan persistence post-SIGINT IS NOT happening
#
# Determinism: 3-second pre-injection delay is deterministic relative to
# fixture's mock-spawn timing (first run_recovery_test call spawns mock
# within ~1s of fixture launch; 3s margin avoids race).
test_i3_sigint_injection() {
	local label="T-i3"
	echo "RUNNING: $label — SIGINT mid-fixture-run triggers cleanup"
	spawn_fixture_background "t-i3"

	# Wait for fixture to spawn at least one mock.
	sleep 3
	if [ -z "$(pgrep_orphans_under "$bg_fixture_tmp_root" || true)" ]; then
		# Fixture finished too fast OR mock not yet visible. Best-effort wait + recheck.
		sleep 1
	fi

	# Inject SIGINT.
	kill -INT "$bg_fixture_pid" 2>/dev/null || true

	# Wait for fixture to exit (bounded by per-bash-signal-handling latency).
	set +e
	wait "$bg_fixture_pid"
	local fixture_exit=$?
	set -e

	# Allow mock's TERM trap to fire (1s sleep loop granularity in mock body).
	sleep 1.5

	# Assertion (a): fixture exited (non-zero expected post-SIGINT).
	if [ "$fixture_exit" -eq 0 ]; then
		fail "$label: fixture exited 0 post-SIGINT; expected non-zero"
		return
	fi
	# Assertion (b): no orphan mocks survive.
	if ! assert_no_orphans_under "$bg_fixture_tmp_root" "$label SIGINT injection"; then
		fail "$label: orphan mocks survived SIGINT (cleanup() did not fire on INT)"
		return
	fi
	pass "$label"
}

# ============================================================================
# T-i4 — SIGTERM mid-test injection
# ============================================================================

# Coverage criterion 5(c): Failure-routing — external SIGTERM triggers cleanup.
#
# Broke-the-code-validation (path-b empirical-finding-driven; path-a captured
# at ~/tmp/task122-phase4-broke-the-code/M4-T-i4-no-TERM-trap.log):
# Empirical M4 mutation (`trap cleanup EXIT INT TERM HUP` → `trap cleanup
# EXIT INT HUP`, removing TERM) does NOT break this test. Reason: bash's
# default SIGTERM action is to terminate, which fires the EXIT trap; the
# EXIT trap (cleanup) reaps mocks via the unconditional pkill regardless of
# whether TERM was in the trap install list. fixture exits with status 143
# (128+SIGTERM=15) which still satisfies assertion (a) "non-zero" + assertion
# (b) "no orphans". The TERM-trap install is therefore stylistic / single-
# fire-via-disarm rather than load-bearing for orphan-reaping. The actual
# load-bearing mutation that breaks this test is M1 (remove pkill from
# cleanup body) — captured at M1-no-pkill-full.log: T-i4 FAILs with
# "orphan mocks survived SIGTERM" because EXIT-trap cleanup runs but has
# no pkill to reap the spawned mock. M1 is the cross-test primary mutation
# (catches T-i1, T-i3, T-i4, T-i5, T-i6, T-i7, T-i8 simultaneously per
# M1-no-pkill-full.log).
#
# Dual-sided: SIGTERM triggers orphan-reap (via EXIT-trap path) IS
# happening; orphan persistence post-SIGTERM IS NOT happening.
test_i4_sigterm_injection() {
	local label="T-i4"
	echo "RUNNING: $label — SIGTERM mid-fixture-run triggers cleanup"
	spawn_fixture_background "t-i4"

	sleep 3
	kill -TERM "$bg_fixture_pid" 2>/dev/null || true

	set +e
	wait "$bg_fixture_pid"
	local fixture_exit=$?
	set -e
	sleep 1.5

	if [ "$fixture_exit" -eq 0 ]; then
		fail "$label: fixture exited 0 post-SIGTERM; expected non-zero"
		return
	fi
	if ! assert_no_orphans_under "$bg_fixture_tmp_root" "$label SIGTERM injection"; then
		fail "$label: orphan mocks survived SIGTERM"
		return
	fi
	pass "$label"
}

# ============================================================================
# T-i5 — SIGHUP mid-test injection
# ============================================================================

# Coverage criterion 5(d): Failure-routing — SIGHUP triggers cleanup.
#
# Broke-the-code-validation (path-b empirical-finding-driven; path-a captured
# at ~/tmp/task122-phase4-broke-the-code/M5-T-i5-no-HUP-trap.log):
# Empirical M5 mutation (remove HUP from trap install) does NOT break this
# test, by symmetry with the T-i4/SIGTERM finding. Bash's default SIGHUP
# action is to terminate; EXIT trap fires; cleanup reaps mocks via
# unconditional pkill. fixture exits with status 129 (128+SIGHUP=1) which
# satisfies both assertions. HUP-trap install is therefore stylistic rather
# than load-bearing for orphan-reaping. Load-bearing mutation that breaks
# this test is M1 (remove pkill from cleanup) — captured at
# M1-no-pkill-full.log: T-i5 FAILs with "orphan mocks survived SIGHUP".
#
# Dual-sided: SIGHUP triggers orphan-reap (via EXIT-trap path) IS happening;
# orphan persistence post-SIGHUP IS NOT happening.
test_i5_sighup_injection() {
	local label="T-i5"
	echo "RUNNING: $label — SIGHUP mid-fixture-run triggers cleanup"
	spawn_fixture_background "t-i5"

	sleep 3
	kill -HUP "$bg_fixture_pid" 2>/dev/null || true

	set +e
	wait "$bg_fixture_pid"
	local fixture_exit=$?
	set -e
	sleep 1.5

	if [ "$fixture_exit" -eq 0 ]; then
		fail "$label: fixture exited 0 post-SIGHUP; expected non-zero"
		return
	fi
	if ! assert_no_orphans_under "$bg_fixture_tmp_root" "$label SIGHUP injection"; then
		fail "$label: orphan mocks survived SIGHUP"
		return
	fi
	pass "$label"
}

# ============================================================================
# T-i7 — A/B-differential evidence-artifact preservation (snapshot-copy)
# ============================================================================

# Coverage criterion 4: A/B-differential preservation — fixture invocation
# under broader test harness must not corrupt executor-2's evidence artifacts
# under $ab_evidence_dir (default ~/tmp/task122-ab-evidence/).
#
# Approach (per coord Q4 disposition + spec §4.4): snapshot-copy baseline.
# We copy original to integration_tmp BEFORE fixture run, then md5-compare
# original (post-run) against snapshot (immutable) — preserves executor-2's
# original artifacts AND detects any corruption surface introduced by fixture.
#
# Broke-the-code-validation (path-b): Add `rm -rf '$ab_evidence_dir'` to
# fixture cleanup() body (mistakenly extending cleanup scope). Re-run T-i7.
# Expected: artifacts disappear, md5 mismatch, assertion fails. Empirical
# artifact at ~/tmp/task122-phase4-broke-the-code/T-i7-{pre,post}-fix.log
# at SUBMIT time.
#
# Dual-sided: evidence preserved IS happening; evidence destruction IS NOT.
test_i7_evidence_preservation() {
	local label="T-i7"
	echo "RUNNING: $label — evidence-artifact preservation (snapshot-copy A/B)"

	if [ ! -d "$ab_evidence_dir" ]; then
		skip "$label" "evidence dir not present at $ab_evidence_dir (executor-2 SUBMIT artifact)"
		return
	fi

	# Snapshot-copy baseline (immutable).
	local snapshot_dir="$integration_tmp/t-i7-evidence-snapshot"
	cp -a "$ab_evidence_dir" "$snapshot_dir"
	local baseline_md5
	baseline_md5="$(md5_dir_contents "$snapshot_dir")"

	# Run fixture (any path; fixture should not touch evidence dir).
	run_fixture_scoped_tmpdir "t-i7"
	if [ "$captured_fixture_status" -ne 0 ]; then
		fail "$label: fixture status=$captured_fixture_status"
		return
	fi

	# Recompute md5 of original.
	local post_md5
	post_md5="$(md5_dir_contents "$ab_evidence_dir")"

	if [ "$baseline_md5" != "$post_md5" ]; then
		fail "$label: evidence md5 changed; baseline=$baseline_md5 post=$post_md5"
		return
	fi
	# Defensive: original dir still exists.
	if [ ! -d "$ab_evidence_dir" ]; then
		fail "$label: evidence dir vanished post-fixture-run"
		return
	fi
	pass "$label (baseline_md5=$baseline_md5 preserved)"
}

# ============================================================================
# T-i8 — Concurrent fixture execution (cross-fixture orphan isolation)
# ============================================================================

# Coverage criterion 2 extension: Real-fixture-path coverage under concurrency.
#
# Spec §2.8 + §4.3: 3 parallel fixture instances with distinct $tmp_dir from
# mktemp; each instance's cleanup() pkill anchored to its own tmp_dir; no
# cross-instance pkill collision (instance A's cleanup must NOT reap instance
# B's mocks).
#
# Broke-the-code-validation (path-b only — empirical mutation of L58 anchor
# to remove the literal-dot-escape produces probabilistic-only failure under
# mktemp's 6-char entropy; the load-bearing concern is theoretical
# defense-in-depth rather than current-bug catch). Mutation: replace
# `${tmp_dir_safe//./\\.}/bin/avd` with `${tmp_dir_safe}/bin/avd` (no escape).
# Expected effect: dot in `/tmp/tmp.XXXXXX` interpreted as regex wildcard;
# parallel instances whose paths share most chars + length COULD over-match
# across the regex engine's interpretation. Practical risk under mktemp's
# distinct-suffix entropy is ~0; spec §1.2 cleanup body comment cites
# defense-in-depth + symmetry-with-curative-pattern as motivation for the
# escape.
#
# Dual-sided: per-instance cleanup IS happening within own scope; cross-
# instance pkill IS NOT happening (anchor isolates).
#
# Determinism: 3 parallel instances synchronized via shell wait; no real-clock.
test_i8_concurrent_fixtures() {
	local label="T-i8"
	echo "RUNNING: $label — 3 concurrent fixture instances (orphan isolation)"

	local pid1 pid2 pid3
	local tmp_root1="$integration_tmp/fixtures/t-i8-a"
	local tmp_root2="$integration_tmp/fixtures/t-i8-b"
	local tmp_root3="$integration_tmp/fixtures/t-i8-c"
	mkdir -p "$tmp_root1" "$tmp_root2" "$tmp_root3"

	env TMPDIR="$tmp_root1" "$fixture" >"$tmp_root1/stdout.log" 2>"$tmp_root1/stderr.log" &
	pid1="$!"
	env TMPDIR="$tmp_root2" "$fixture" >"$tmp_root2/stdout.log" 2>"$tmp_root2/stderr.log" &
	pid2="$!"
	env TMPDIR="$tmp_root3" "$fixture" >"$tmp_root3/stdout.log" 2>"$tmp_root3/stderr.log" &
	pid3="$!"

	set +e
	wait "$pid1"; local s1=$?
	wait "$pid2"; local s2=$?
	wait "$pid3"; local s3=$?
	set -e

	if [ "$s1" -ne 0 ] || [ "$s2" -ne 0 ] || [ "$s3" -ne 0 ]; then
		fail "$label: at least one parallel fixture failed (s1=$s1 s2=$s2 s3=$s3)"
		return
	fi

	# Assertion (a): no orphans under any of the 3 tmp_roots.
	for tr in "$tmp_root1" "$tmp_root2" "$tmp_root3"; do
		if ! assert_no_orphans_under "$tr" "$label parallel"; then
			fail "$label: orphans survived under $tr"
			return
		fi
	done

	# Assertion (b): each fixture's mktemp.XXXXXX subdir was REMOVED (KEEP_TMP unset).
	for tr in "$tmp_root1" "$tmp_root2" "$tmp_root3"; do
		local survs
		survs="$(find "$tr" -mindepth 1 -maxdepth 1 -type d 2>/dev/null || true)"
		if [ -n "$survs" ]; then
			fail "$label: tmp_dir not removed under $tr: $survs"
			return
		fi
	done

	# Assertion (c): distinct $tmp_dir uniqueness (per critique §4.3) — each
	# scoped TMPDIR distinct by construction (separate filesystem subtree).
	# Defensive verification that 3 distinct paths participated:
	if [ "$tmp_root1" = "$tmp_root2" ] || [ "$tmp_root2" = "$tmp_root3" ] || [ "$tmp_root1" = "$tmp_root3" ]; then
		fail "$label: parallel fixture roots not distinct (test setup bug)"
		return
	fi
	pass "$label (3 parallel instances; isolation maintained)"
}

# ============================================================================
# T-i9 — Production script standalone invocation (no fixture trap baseline)
# ============================================================================

# Coverage criterion 1 extension: Cross-task boundaries — production-side
# baseline. Per spec §1.2 G7 + §2.9: production avd-outage-recovery.sh has
# no own trap install; orphan-reaping is fixture-test-mock-specific concern
# (production uses real AVD daemon, not spawned mocks).
#
# Implementation per spec §4.1 (skip-if-no-AVD-daemon guard): primary
# assertion is structural — verify production has no trap + no mktemp + no
# bin_dir/avd-mock-spawn pattern. This documents the boundary that Task
# #122's fixture-only fix is sufficient.
#
# Optional behavioral extension: if real AVD daemon detectable (via PID file
# + kill -0 liveness), run production briefly + observe orphan count. If no
# AVD detectable, skip behavioral but still execute structural.
#
# Broke-the-code-validation (path-b): N/A — no trap to break in production.
# Test serves as boundary-clarification baseline; mutation = adding spurious
# trap to production would be tested via T-i1-style verification (out of
# scope for production script).
#
# Dual-sided: production behavior preserved IS happening; mock-orphan-spawn
# pattern IS NOT happening in production (real daemon, not mock).
test_i9_production_baseline() {
	local label="T-i9"
	echo "RUNNING: $label — production script no-trap baseline (structural)"

	# Structural assertion (a): production has 0 trap installs.
	local trap_count
	trap_count="$(grep -cE '^trap ' "$production" || true)"
	if [ "$trap_count" -ne 0 ]; then
		fail "$label: production unexpectedly has $trap_count trap install(s) at top-level"
		return
	fi
	# Structural assertion (b): production has 0 mktemp -d (no scratch tmp_dir).
	local mktemp_count
	mktemp_count="$(grep -cE 'mktemp\s+-d' "$production" || true)"
	if [ "$mktemp_count" -ne 0 ]; then
		fail "$label: production unexpectedly uses mktemp -d ($mktemp_count occurrences); test mocks scratch lifecycle in fixture only"
		return
	fi
	# Structural assertion (c): production has 0 mock-spawn pattern (`bin_dir/.*&`
	# disown sequence). Production uses real AVD via setsid+nohup, not /tmp/bin/avd.
	local mock_spawn_count
	mock_spawn_count="$(grep -cE 'bin_dir.*\&' "$production" || true)"
	if [ "$mock_spawn_count" -ne 0 ]; then
		fail "$label: production unexpectedly contains bin_dir mock-spawn pattern ($mock_spawn_count); no mocks in production"
		return
	fi

	# Behavioral extension: skip-if-no-AVD-daemon guard.
	local avd_pid_file="${AVD_PID_FILE:-$HOME/tmp/avd.pid}"
	if [ ! -s "$avd_pid_file" ]; then
		pass "$label (structural; behavioral skipped — no AVD PID file at $avd_pid_file)"
		return
	fi
	local pid
	pid="$(cat "$avd_pid_file")"
	if ! [[ "$pid" =~ ^[1-9][0-9]*$ ]] || ! kill -0 "$pid" 2>/dev/null; then
		pass "$label (structural; behavioral skipped — AVD PID $pid not alive)"
		return
	fi
	# Real AVD detectable. Pre-condition gate satisfied; behavioral extension
	# would invoke production here. Out of scope for this CI test (would
	# require real adb/phone setup); document and pass.
	pass "$label (structural; behavioral-extension-deferred — real AVD detected at PID $pid)"
}

# ============================================================================
# T-i10 — Wingout-vendored copy parity (md5 + behavioral)
# ============================================================================

# Coverage criterion 1 extension: Cross-task boundaries — vendored-copy parity.
#
# Per spec §2.10 + coord Q2 disposition (fail-only on drift; resync is
# separate workflow): T-i10 asserts md5 equality between canonical fixture
# AND wingout-vendored copy at import/ffstream/scripts/mission/. Plus
# behavioral parity sub-test: invoke vendored copy via T-i1-style wrapper
# protocol; assert no orphans (confirms vendored copy honors same cleanup()
# trap).
#
# Coord-cited canonical md5 at SHA 3fd380a: f92fa1d5544626ba8a7f1678b388c7f0.
#
# Broke-the-code-validation (path-b): Modify only canonical (e.g., add a
# stray comment) without re-vendoring. Re-run T-i10. Expected: md5 inequality
# assertion fails. Empirical artifact at ~/tmp/task122-phase4-broke-the-
# code/T-i10-{pre,post}-fix.log at SUBMIT time. Confirms vendor-sync
# discipline is enforced at test-time.
#
# Dual-sided: vendored copy parity IS preserved; vendor-drift IS NOT.
test_i10_vendored_parity() {
	local label="T-i10"
	echo "RUNNING: $label — wingout-vendored copy md5 + behavioral parity"

	if [ ! -f "$wingout_vendored" ]; then
		skip "$label" "vendored copy not present at $wingout_vendored"
		return
	fi

	# Md5 sub-assertion.
	local canonical_md5 vendored_md5
	canonical_md5="$(md5sum "$fixture" | cut -d' ' -f1)"
	vendored_md5="$(md5sum "$wingout_vendored" | cut -d' ' -f1)"
	if [ "$canonical_md5" != "$vendored_md5" ]; then
		fail "$label: md5 mismatch — canonical=$canonical_md5 vendored=$vendored_md5"
		return
	fi

	# Behavioral sub-assertion: invoke vendored copy under T-i1-style protocol.
	FIXTURE_OVERRIDE="$wingout_vendored" run_fixture_scoped_tmpdir "t-i10-vendored"
	unset FIXTURE_OVERRIDE
	if [ "$captured_fixture_status" -ne 0 ]; then
		fail "$label: vendored copy exited non-zero ($captured_fixture_status)"
		return
	fi
	if ! assert_no_orphans_under "$captured_fixture_tmp_root" "$label vendored exit"; then
		fail "$label: orphan mocks survived vendored fixture exit"
		return
	fi
	pass "$label (md5=$canonical_md5; behavioral parity confirmed)"
}

# ============================================================================
# T-i6 — Mission-spine sequential chain (run last; longest)
# ============================================================================

# Coverage criterion 3: Cleanup() integration with mission-spine.
#
# Per coord Q3 disposition: mock harness for CI-feasibility. Goal 1
# (goal1-normal-topology.sh) has no _test.sh sibling and depends on real
# adb/phone setup → not invoked in mock harness mode (would require real
# phone environment). T-i6 instead invokes the available _test.sh siblings:
# Goal 5 (avd-outage-recovery_test.sh) → Goal 2 (builtincamera-ui-
# activation_test.sh) sequentially, with pgrep-clean assertion at each
# transition boundary. Goal 5 is the orphan-spawning fixture; Goal 2 is
# sourcing-only (no mock daemons spawned per inspection of activation
# fixture body — uses bash function overrides, not mock binaries). Therefore
# T-i6 effectively validates: Goal 5 cleanup fires before Goal 2 starts;
# Goal 2's environment is pristine.
#
# Per critique §4.2: AVD_OUTAGE_SECONDS env var override supported by
# fixture (already 1 internally per L296); CI feasibility achieved.
#
# Broke-the-code-validation (path-b; cross-references T-i1's empirical):
# Reverting fixture cleanup() to PRE-FIX form (rm -rf only) per T-i1 broke-
# the-code mutation produces orphans persisting INTO Goal 2's environment.
# Goal 2 then sees stray mocks alive in its baseline pgrep — chain pollution
# evident. T-i1's broke-the-code log at ~/tmp/task122-phase4-broke-the-code/
# captures the same root mutation; T-i6 chain assertion catches the
# cross-mission propagation.
#
# Dual-sided: chain executes cleanly IS happening; cross-mission orphan
# accumulation IS NOT happening.
test_i6_mission_spine_chain() {
	local label="T-i6"
	echo "RUNNING: $label — mission-spine chain (Goal 5 → Goal 2 mock-harness)"

	# Pre: integration tree pgrep clean (best-effort baseline).
	local pre_orphans
	pre_orphans="$(pgrep_orphans_under "$integration_tmp" || true)"
	if [ -n "$pre_orphans" ]; then
		fail "$label: pre-chain orphans present: $pre_orphans"
		return
	fi

	# Stage 1: Goal 5 fixture (orphan-spawning fixture per Task #122 substrate).
	local stage1_root="$integration_tmp/fixtures/t-i6-stage1-goal5"
	mkdir -p "$stage1_root"
	set +e
	env TMPDIR="$stage1_root" "$fixture" \
		>"$stage1_root/stdout.log" 2>"$stage1_root/stderr.log"
	local s1=$?
	set -e
	if [ "$s1" -ne 0 ]; then
		fail "$label/stage1 (Goal 5): fixture exited $s1"
		return
	fi
	# Mid-chain assertion: no orphans carryover from stage 1.
	if ! assert_no_orphans_under "$stage1_root" "$label/stage1 boundary"; then
		fail "$label: orphans carryover from Goal 5 fixture into chain"
		return
	fi

	# Stage 2: Goal 2 fixture (sourcing-only; should produce no orphans).
	local stage2_root="$integration_tmp/fixtures/t-i6-stage2-goal2"
	mkdir -p "$stage2_root"
	set +e
	env TMPDIR="$stage2_root" "$activation_fixture" \
		>"$stage2_root/stdout.log" 2>"$stage2_root/stderr.log"
	local s2=$?
	set -e
	if [ "$s2" -ne 0 ]; then
		fail "$label/stage2 (Goal 2): fixture exited $s2"
		return
	fi
	# Mid-chain assertion: no orphans under stage2 root.
	if ! assert_no_orphans_under "$stage2_root" "$label/stage2 boundary"; then
		fail "$label: orphans surfaced under Goal 2 fixture root"
		return
	fi

	# Final assertion: pgrep clean across entire integration tree.
	local final_orphans
	final_orphans="$(pgrep_orphans_under "$integration_tmp" || true)"
	if [ -n "$final_orphans" ]; then
		fail "$label: post-chain orphans persist across integration tree: $final_orphans"
		return
	fi

	pass "$label (Goal 5 + Goal 2 chain; no cross-mission orphan accumulation)"
}

# ============================================================================
# Driver
# ============================================================================

# Test execution ordering per spec §3.4 + dispatch:
#   - Signal-injection group T-i3 → T-i4 → T-i5 (sequential)
#   - Parallelizable group T-i1, T-i2, T-i7, T-i8, T-i10 (sequential here for
#     deterministic per-test reporting; parallelization is allowed per spec
#     but adds output-interleaving complexity)
#   - T-i9 structural (fast)
#   - T-i6 mission-spine (longest; run last)
#
# TEST_FILTER env var (optional, regex): if set, only tests whose label
# matches the regex run. Used at SUBMIT time to capture targeted broke-the-
# code A/B differential evidence per test (mutate canonical fixture, run
# only the affected test, capture log, restore). Default unset → all 10
# tests run.
should_run() {
	local label="$1"
	[ -z "${TEST_FILTER:-}" ] && return 0
	[[ "$label" =~ $TEST_FILTER ]]
}

should_run "T-i1"  && test_i1_external_runner_natural_exit
should_run "T-i2"  && test_i2_keep_tmp_env_var
should_run "T-i3"  && test_i3_sigint_injection
should_run "T-i4"  && test_i4_sigterm_injection
should_run "T-i5"  && test_i5_sighup_injection
should_run "T-i7"  && test_i7_evidence_preservation
should_run "T-i8"  && test_i8_concurrent_fixtures
should_run "T-i9"  && test_i9_production_baseline
should_run "T-i10" && test_i10_vendored_parity
should_run "T-i6"  && test_i6_mission_spine_chain

# ----- Summary + exit ----------------------------------------------------

printf '\n=== Phase 4 integration test summary ===\n'
printf '  PASS : %d\n' "$PASS_COUNT"
printf '  FAIL : %d\n' "$FAIL_COUNT"
printf '  SKIP : %d\n' "$SKIP_COUNT"

if [ "$FAIL_COUNT" -gt 0 ]; then
	echo "Phase 4 integration tests FAILED."
	exit 1
fi
echo "All Phase 4 integration tests passed."
exit 0
