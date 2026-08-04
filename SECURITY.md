# Security Policy

## Supported Versions

Security fixes are provided for the latest `0.1.x` release line. Pre-release
commits and older release lines may receive fixes only when the maintainers
explicitly announce support.

| Version | Supported |
| --- | --- |
| `0.1.x` | Yes |
| `< 0.1.0` | No |

## Reporting a Vulnerability

Report suspected vulnerabilities privately by email. Do not open a public
issue or include credentials, exploit details, or sensitive Tool arguments in
public logs.

Security contact: mailto:security@tiggy.cloud

Include the affected version, impact, reproduction conditions, and any known
mitigation. The maintainers will coordinate validation, remediation, advisory
publication, and release revocation when required. We aim to acknowledge a
report within three business days.

## Security Boundaries

Security-sensitive areas include credential redaction, tool argument logging, SafeSpace path containment, command execution, MCP transports, memory scope isolation, and extension resource cleanup.

`command/local` executes host processes and is not a security sandbox. Its Root limits only the initial working directory; commands can still use absolute paths, the network, inherited operating-system authority, and descendant processes. Use an isolated `command.Runner` implementation when executing untrusted scripts.

MCP stdio servers are also host processes, not sandboxes. They receive only explicitly configured environment variables but retain the Runtime process's operating-system authority. Streamable HTTP credentials must be injected through controlled headers or a caller-owned `http.Client`; they must not enter Tool arguments, model context, or default logs.
