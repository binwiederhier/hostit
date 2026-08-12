# Security policy

hostit runs as a root daemon and hosts other people's code, so its isolation
boundaries are load-bearing. Security reports are taken seriously.

## Reporting a vulnerability

**Please do not open a public GitHub issue for a security vulnerability.**

Report it privately instead, either by:

- opening a [GitHub security advisory](https://github.com/binwiederhier/hostit/security/advisories/new)
  (Security tab -> Report a vulnerability), or
- emailing the maintainer at `phil@heckel.io`.

Include enough to reproduce: affected version or commit, configuration relevant to
the issue, and the steps or proof of concept. You will get an acknowledgement, and
we will work with you on a fix and a coordinated disclosure. Please give a
reasonable window to release a fix before disclosing publicly.

## Supported versions

Fixes land on the latest release. There is no long-term-support branch; upgrade to
the current version to receive security fixes.

## Scope

Things especially worth reporting:

- Escaping an app's container onto the host, or onto another app's uid, files,
  processes, network namespace or loopback port.
- An app-scoped token or SSH login reaching outside its one app (another app, the
  account, or an admin endpoint).
- The built-in assistant sandbox exfiltrating the operator's API key or Claude
  subscription credential, or reaching tools beyond its target app's REST surface.
- Bypassing authentication or authorization (session, OAuth, admin token, or the
  same-origin protection on cookie-authenticated requests).
- TLS/ACME issuance issues (e.g. obtaining a certificate for a name you do not own).

The daemon is the trusted control plane and legitimately runs as root; a report
that "the root daemon can do root things" is expected behavior, not a vulnerability.
Operators are responsible for securing the host itself.
