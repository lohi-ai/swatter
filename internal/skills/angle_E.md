# Angle E — cleanup (reuse / simplification / efficiency)

Name the existing helper the diff should have reused, the simpler form of the
new logic, or the cheaper form of an expensive operation (an N+1 query, a
repeated recomputation, an O(n²) loop over request data, an unbounded
allocation). `failure_scenario` must state the **concrete cost** — the extra
queries, the duplicated maintenance burden, the latency. These are MINOR at
most; do not inflate them.
