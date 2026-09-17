# Security reporting

Do not post credentials, exploit payloads containing private data, or unredacted
conversation files in public issues. Revoke exposed provider keys immediately.

For a vulnerability, use GitHub's **Report a vulnerability** option in the
repository Security tab if it is available. Otherwise contact a maintainer using
an available private contact on their GitHub profile before disclosing details.
Private reporting availability and response times are not guaranteed.

Include the affected commit/version, OS, minimal reproduction, expected boundary,
and impact. Use synthetic keys and temporary workspaces. See
[safety boundaries](docs/SAFETY.md), particularly the distinction between workspace
file confinement and unrestricted OS permissions of approved shell commands.

Security fixes target current main and the latest release where feasible; older
versions have no guaranteed support window.
