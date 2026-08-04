# Security Policy

The project is not yet publicly released. Report suspected vulnerabilities privately to the repository owner rather than opening a public issue.

Security-sensitive areas include credential redaction, tool argument logging, SafeSpace path containment, command execution, MCP transports, memory scope isolation, and extension resource cleanup.

`command/local` executes host processes and is not a security sandbox. Its Root limits only the initial working directory; commands can still use absolute paths, the network, inherited operating-system authority, and descendant processes. Use an isolated `command.Runner` implementation when executing untrusted scripts.

MCP stdio servers are also host processes, not sandboxes. They receive only explicitly configured environment variables but retain the Runtime process's operating-system authority. Streamable HTTP credentials must be injected through controlled headers or a caller-owned `http.Client`; they must not enter Tool arguments, model context, or default logs.

Before a public release, this file must be updated with a supported-version table and a monitored security contact.
