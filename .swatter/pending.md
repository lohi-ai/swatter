# Swatter pending observations

<!-- Managed by Swatter: evidence collected from merged-PR feedback. Entries are promoted into rules.md when a pattern repeats, and age out otherwise. -->

- id: o-2026-07-15-1
  kind: repeat
  pr: 20   date: 2026-07-15
  path: internal/doctorcmd.go
  note: doctor resolves originRepo twice, spawning duplicate git remote lookups for the same repository.
- id: o-2026-07-15-2
  kind: repeat
  pr: 20   date: 2026-07-15
  path: internal/doctorcmd.go
  note: doctor's advertised cheap model round-trip uses the strong model instead of the configured cheap model.
- id: o-2026-07-15-3
  kind: repeat
  pr: 20   date: 2026-07-15
  path: internal/filecfg.go
  note: Layering the config file by mutating process environment creates stale global state instead of keeping file defaults local to LoadConfig.
