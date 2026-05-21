# Archive

Historical design specs and dated feature inventories kept for context. These
do not reflect current behaviour — the README and the runbooks under `docs/`
are the source of truth.

- `2026-05-21-feature-inventory.md` — point-in-time inventory mixing the
  audiobooks and ebooks plugins. Captured at the end of the ABS-mobile
  push; the audiobooks side is largely accurate but the ebooks section is
  out of scope for this repo and the wording (Tier 3, "audited and
  dispositioned") is a process artifact, not a description of the code.
- `2026-05-21-standalone-abs-login.md` — design spec for the body-creds
  login path on the standalone listener. Implemented; the current
  behaviour lives in `setup-debug-flows.md` and `troubleshooting.md`. Spec
  is kept here because it explains *why* the header strip and the
  per-user opt-in exist.
