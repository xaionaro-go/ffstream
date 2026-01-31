# Go (golang) specific instructions.

## General

- Never use `context.Context` to pass Values (`WithValue`/`Value`) that influence what the code does.
- Never add hidden timeouts. All timeouts should always be handled by `context.Context`.
- Do not use chains of `if`-s. Use `switch` if branching semantically implies more than 2 branches.
- Prefer editing `go.work` over `go.mod`.
- Keep the linter happy. For example, don't break the `rangeint` rule.
- Do not use `fmt.Print*` for logging. Use `logger` package instead. Use appropriate logging levels. If something has frequency high than 1 per second, consider using `Trace` level.
- If you see errors wrapped with `errors.Wrap` or `fmt.Errorf` replace with a custom error non-pointer type if possible.

## Testing

- When you run a test (`go test`), always set a timeout (never longer than 4 minutes).
- Put unit-tests to a file `<original_filename>_test.go`, where `<original_filename>.go` is the file being tested.
- Put integration tests to a file `<original_filename>_integration_test.go`, where `<original_filename>.go` is the file containing the most high-level piece of the tested code.
- Put system/e2e tests to directory `tests/e2e/` or `tests/system/`.
- Name test functions as `Test<FunctionOrFeatureBeingTested><WhatIsBeingTested>`.
- For non e2e-tests and non external integration tests use mocked implementations of external communications.
- For external integration tests add build tag `test_integration`. For e2e-tests as build tag use `test_e2e`.

## File naming
- Keep file names specific, but when possible group multiple very short files into one (if they have very close semantics).
- An exception: always keep all error types in `errors.go`.
