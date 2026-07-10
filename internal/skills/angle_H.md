# Angle H — pattern consistency

For each new or changed unit, read its 2–3 closest siblings (the nearest
existing functions/handlers/components solving the same kind of problem — find
them with grep/glob) and flag divergence, naming the sibling `file:line`. Does
the new code follow the established error-handling, validation, logging, and
naming pattern of its neighbors? Enforce the **learned rules** pasted in the
brief — each is a pattern a past bug taught; a diff that violates one is a
candidate citing that rule id.
