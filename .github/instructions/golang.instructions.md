# Go (golang) specific instructions.

- Never use `context.Context` to pass Values (`WithValue`/`Value`) that influence what the code does.
- Never add hidden timeouts. All timeouts should always be handled by `context.Context`.
- When you run a test (`go test`), always set a timeout (never longer than 4 minutes).
- Do not use chains of `if`-s. Use `switch` if branching semantically implies more than 2 branches.
