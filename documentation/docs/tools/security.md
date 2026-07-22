---
title: Tool security
sidebar_position: 4
---

# Tool security

Tool code runs with the operating-system privileges of your process unless the
tool itself creates a stronger boundary. Neither an LLM prompt nor
`permission.Unrestricted` is a sandbox.

## Treat tool inputs as untrusted

Validate every model-supplied value as if it came from an external API client:

- resolve paths and reject symlink escapes;
- use argument arrays instead of concatenated shell strings;
- allowlist protocols and hosts before network requests;
- set request, output, and execution limits;
- remove credentials and personal data from returned output;
- honor cancellation and deadlines;
- do not use a user-facing confirmation as input validation.

## Path tools

A secure project file tool should:

1. require project-relative paths;
2. clean and resolve symlinks;
3. verify the resolved path remains under the configured root;
4. reject credential/config/private-key paths before opening the file;
5. bound line count and byte count;
6. reject unsupported binary data.

Temporary-file writes affect real files in the temporary directory. They do not
modify the original project file unless another operation copies, renames, or
uses that temporary artifact. Temp access is useful for compilation and
intermediate artifacts, but it is still host access and needs quotas and cleanup.

## Shell tools

If an application provides shell execution:

- parse commands before policy classification;
- distinguish ordinary project-local operations from high-risk actions;
- prevent path escapes on every file operand, including symlink targets;
- limit environment variables and output sizes;
- set timeouts and kill process groups on interruption;
- decide explicitly whether user shell profiles are loaded;
- remember that profiles can execute arbitrary startup code;
- require permission for privilege changes, persistence, destructive host
  operations, or sandbox bypass.

Running a project-local script or applying executable mode to a project-local
script can be medium/low risk under a trusted project policy. The same action
outside the project should be reclassified or denied. Classification must use
resolved targets, not just command names.

## Permissions are policy, not containment

Permissions provide auditable human admission. A compromised or incorrectly
implemented handler may still exceed its declared capability. For stronger
guarantees, run the agent process or individual tool in a container, VM,
restricted OS account, seccomp profile, macOS sandbox profile, or equivalent
platform boundary.

## Confirmation text

Confirmation details may come partly from model arguments. Normalize control
characters and bound their size before display so the text cannot forge
terminal lines. Never display secrets. The `playground/terminal` package's
`confirm_demo` example collapses whitespace and enforces a 240-character limit.

## HTTP exposure

The default server binds to loopback. Before remote exposure, enforce
authentication, session ownership, authorization for decision endpoints, TLS,
CORS, rate limits, and audit logs. A permission ID alone is insufficient: the
API additionally requires matching session, turn, and call IDs.
