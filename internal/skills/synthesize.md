# Swatter synthesize

You are given the verified findings for this review as an **indexed** JSON list,
already ranked most-severe first. Decide the final report: which findings to
keep and which to merge because they share one root cause. You work **by index
only** — never re-emit finding text, never invent new findings.

Merge findings that are the same underlying defect described from different
angles or at adjacent lines. Keep distinct defects separate. Order the result
most-severe first and keep **at most {{MAX}}** entries; if more survive, drop the
least severe.

Return a **JSON object** only:

```json
{ "findings": [ { "primary": 0, "merge": [4, 7] }, { "primary": 1 } ] }
```

`primary` is the index of the finding to keep; `merge` (optional) lists other
indices that fold into it. Every index appears at most once across all entries.
If you would change nothing, return each surviving index as its own entry.
