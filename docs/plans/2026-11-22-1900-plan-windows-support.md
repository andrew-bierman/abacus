---
title: Windows Support - Plan
type: plan
date: 2026-11-22
execution: none
---

# Windows Support - Plan

**Status:** not started. abacus is a TUI today and is heading to native macOS and Linux apps; Windows is
a reasonable later target, not a rejected one. This plan records what it would take so the choice can be
made with real numbers.

## Where things stand

`GOOS=windows go build ./cmd/abacus` succeeds with `CGO_ENABLED=0`. Nothing else has been run on Windows.

Bundled tools are out of scope for the same reason: `bundled-tools.json` pins darwin and linux
(amd64, arm64) only, and `scripts/package-release.sh` and `scripts/dev-bundle.sh` stop with "has no pin
for windows-amd64". A Windows release would need pinned `.exe` assets (or zip members) per tool and the
same `bundled/` sidecar lookup; until then a Windows user would have to supply the tools themselves.
Four places have non-unix fallbacks, and two of them weaken a guarantee instead of failing:

| Area | File | Windows behavior today | Needed |
| --- | --- | --- | --- |
| Sidecar owner check | `internal/core/owner_other.go` | `ownerUID` reports "unknown", so the owner check **passes** (fails open) | Decide the rule (owner is the user or Administrators/SYSTEM) and check the file ACL; until then fail closed |
| Config write lock | `internal/core/config_lock_other.go` | no-op, so concurrent `config set` can race | `LockFileEx` on a lock file |
| Process groups | `internal/core/procgroup_other.go` | no-op, so cancel or timeout cannot kill a child tree | a Job Object (kill-on-close) per child |
| Lambe process identity | `internal/lambe/identity_other.go` | fails closed (skips descriptors that carry an identity) | process creation time via `GetProcessTimes`, and a liveness probe |

Mode-bit checks (`0o002`, `0o020`, `0o111`) in `internal/core/locate.go` mean nothing on Windows: files have
no execute bit and permissions are ACLs.

## Open questions (owner decisions)

1. Is Windows a goal, and native only, or also WSL? WSL already runs the linux archive, so "supported" may
   be satisfied by documenting WSL.
2. The bundled tools: do bd, bv, Lambe and beads-lease publish `windows-amd64` assets with checksums?
   Lambe supervises agents and its own platform story decides whether Queues can exist on Windows at all.
3. Archive format (`.zip`) and sidecar names (`bd.exe`); `bundled-tools.json` needs `windows-amd64` entries
   and `scripts/package-release.sh` needs a zip path (it is tar-only today).
4. The TUI library's Windows terminal behavior (conhost vs Windows Terminal, key handling) is unverified.
5. `o` opens a browser or tool; the open command differs (`start`, `explorer`).

## Suggested order, if it is wanted

1. Make the non-unix fallbacks fail closed (owner check refuses on Windows unless explicitly implemented).
   This is worth doing regardless of Windows support. Small.
2. Check upstream tool availability (question 2). If a core tool has no Windows build, decide which domains
   degrade to "not connected" there.
3. Implement the Job Object, file lock and ACL owner check behind the existing build-tag seams, with tests
   that run on a Windows machine or runner.
4. Packaging: zip, `.exe` names, manifest entries.
5. Run doctor, setup and the TUI on a real Windows terminal; then document support.

## Effort

Steps 1 and 2 are small. Steps 3 to 5 are a medium piece of work, mostly gated on a Windows test machine
and on upstream Windows builds. None of it is needed for the macOS and Linux apps.
