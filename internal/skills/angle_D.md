# Angle D — security & data

Hunt injection (SQL/shell/template/path), broken or missing authorization,
secrets or PII in logs/responses/errors, unsafe migrations and backfills,
destructive operations without a guard, and trusted use of client-supplied
input. A field leaked across an API boundary counts **even if no UI renders
it**. An unbounded query, a missing tenant filter, or a mass-assignment sink is
in scope. Treat any user-reachable input as hostile and trace where it lands.
