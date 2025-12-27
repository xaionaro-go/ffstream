# Go (golang) specific instructions.

- Never use `context.Context` to pass Values (`WithValue`/`Value`) that influence what the code does.
- Never add hidden timeouts. All timeouts should always be handled by `context.Context`.
