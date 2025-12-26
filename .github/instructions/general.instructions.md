# Task execution rules

## 0) Priority order (use this to resolve conflicts)
1. Safety + data integrity + do-not-touch constraints
2. Explicit user instructions in the current task
3. Interface stability + minimal-diff policy

## 1) Completion and stopping
- A task is **DONE** only with objective evidence. Do not infer, do not assume, only directly objectively confirm and prove.
- If you cannot obtain objective evidence, the task is **NOT DONE**. State exactly what evidence is missing and why.
- If absolutely blocked on required user input/permission/action, report **BLOCKED** with the fields listed below and run `sleep $[ 24 * 3600 ]` (after `sleep` is finished you need to recheck if you are unblocked, and if not, finish the execution):
  - what is blocked,
  - the exact question(s),
  - the exact next command(s) you would run after the answer.

## 2) Interfaces and scope control
- Never change public interfaces, CLI flags, config schema, RPC/GRPC surfaces, or exported symbols unless explicitly asked.

## 3) File editing discipline
- Immediately before editing any file, re-read it.
- Keep changes as local as possible: smallest scope, shortest lifetime, minimal visibility.
- Do not introduce new entities unless they remove duplication or improve single-source-of-truth in the touched scope.

## 4) Generated / forbidden paths
- Do not edit `**/imports/**`, `**/import/**` -- these directories are not the source of truth for the source code.

## 5) Commands, installs, and environment
- When a dependency is missing:
  - Prefer OS packages via the detected system package manager (`apt`, `dnf`, `pacman`, `xbps`, etc.).
  - If installation requires permission or could be disruptive, propose the exact command first and wait for approval.

## 6) Testing policy
- Before fixing a bug, prefer adding/adjusting a unit/integration test that reproduces it (when feasible and not prohibitively expensive).
- After code changes, ensure relevant tests are updated and passing.
- If tests are not feasible, document why and provide an alternative verification method.
- If logs were provided for diagnosis, use them as verification evidence: confirm the fix by showing (from the logs) that the previously-missing behavior now occurs (or the previously-present error no longer occurs). If the original logs are insufficient to verify, state what additional log line(s) or log level would be needed.

## 7) Diagnostics and logging
- When unsure, prefer more diagnostics.

## 8) Root cause and correctness checks
- Fix root causes, not symptoms.
- I repeat: before considering an issue solve, think if there could be a deeper reason of the issue, and address it. For example:
  - If something is nil, then just a check for nil is not enough: why is it nil? Should it be nil? If not, fix the root cause.

## 9) Self-review and reversions
- Critique your own change before declaring DONE: list likely failure modes and confirm they are addressed by evidence or checks. Continue repeating this critique&fix cycles until nothing left to critique.
- If a change you made is reverted, assume it was incorrect.

## 10) Hints file usage
- Every ~5 meaningful steps and before any major step, read `.github/instructions/hints.md`.
- Delete the hints after reading them.

## 11) Output verbosity
- Keep outputs short.
- Again: do not write long texts! Let me repeat: DO NOT WRITE LONG TEXTS!
- On success, report only:
  - Status: DONE
  - Evidence (tests/build/repro)
  - Optional next step
- On failure/block, report only:
  - Status: NOT DONE or BLOCKED
  - What happened (1–3 lines)
  - Next steps (exact commands/questions)

## 12) Coding
- Don't make cheap initializations be lazy, initialize normally instead.
- After every change, try to find ways to reduce the amount of code in the pieces related to the change. But don't change the code that is not affected by (/related to) the change. Also one-lining the code IS NOT reducing it's amount: you should remove logic, not amount of lines; keep the code readable (even if it requires more lines). Try to simplify, e.g. remove unnecessary `if`-s.
- If a change requires or touches 'ugly' workarounds, treat it as a design smell: pause and look for a more elegant approach.
- When strong input expectations exist, validate inputs. If no error channel exists, use an assertion (or equivalent invariant enforcement).
- Maintain internal semantic consistency: one source of truth for each piece of logic/constant, within the touched scope.
- Split logic into distinct functions when there is an opportunity to do so. Prefer small functions, but do not split a self-sufficient thought into pieces that are no longer semantically self-sufficient.
