# Security policy

## Reporting a vulnerability

Please report security problems privately through GitHub's private vulnerability reporting:
go to the repository's **Security** tab and choose **Report a vulnerability**
(<https://github.com/andrew-bierman/abacus/security/advisories/new>). Do not open a public issue
or pull request for a vulnerability.

Include what you ran, what you expected and what happened, the abacus version (`abacus version`),
your platform, and, if you can, the smallest steps that reproduce it. Do not include real
credentials or tracker data.

You can expect an acknowledgement within a few days. abacus is maintained by one person, so there
is no fixed fix deadline; a confirmed issue gets a fix in the next patch release and, once it has
shipped, a published advisory that credits you unless you prefer otherwise.

## Supported versions

abacus is pre-1.0. Only the latest released minor version (see the releases page) receives fixes.

## Scope

In scope: the write gate (plan, confirm, single-use apply, audit log), the allowlists and scrubbed
environment child tools run with, the built-in tool cache (permissions, hash checks), and anything
that makes abacus run a command, write a file or send data the user did not confirm. The design is
described in `docs/tool-contract.md` and `docs/built-in-tools.md` (including the threat model).

Out of scope: vulnerabilities in the built-in tools themselves (report those to their projects;
`THIRD_PARTY_NOTICES.md` lists them), and attacks that already require write access to the
user's own account, home directory or tracker.
