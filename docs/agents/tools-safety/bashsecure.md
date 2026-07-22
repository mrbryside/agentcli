# Bash security helper

`toolexecution/bashsecure` is an optional helper for caller-owned Bash tools; it is not automatically registered. It parses Bash with `mvdan.cc/sh`, rejects unsupported dynamic constructs and destructive/privileged command families, validates nested scripts, and confines relevant paths to configured project and temporary roots.

Policy classification is separate from execution. Permission descriptors can use the parsed command and permission mode to decide which capabilities/risk require admission. Platform files provide macOS sandbox behavior and an explicit unsupported fallback.

Do not treat command filtering as a complete isolation boundary. A permitted executable may still have broad authority. Strong isolation requires a container, VM, restricted account, or operating-system sandbox. Add policy tests for every newly allowed command form and path edge case.

Back to [tools-safety/index.md](index.md).
