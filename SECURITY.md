# Security policy

LLMGW sits between users and LLM providers and sees personal data in clear text before pseudonymising it. Security reports are welcome and taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private reporting instead: *Security → Report a vulnerability* on this repository. Include the version (`./llmgw -version`), the configuration involved (without secrets) and the steps to reproduce.

You should get an acknowledgement within a week. Fixes ship as a patch release, credited in [RELEASE_NOTES.md](RELEASE_NOTES.md) unless you prefer otherwise.

## In scope

- data that reaches an LLM provider in clear text although the profile asks to pseudonymise or block it (for example a field of the request that the gateway does not walk through);
- authentication or authorisation bypass: gateway keys, admin token, SSO sessions, Admin and Support roles;
- leaks of provider keys, admin secrets or original values (logs, metrics, error messages, dashboard);
- weaknesses in the web protection (`[security]`), the access requests flow or the deployment scripts.

## Not a vulnerability

Detection misses on free text are a known limit of rule-based anonymisation (see *Current limitations* in the README). Report them as regular issues, with an invented example that reproduces the miss, never with real personal data.

## Supported versions

Only the latest release receives fixes.
