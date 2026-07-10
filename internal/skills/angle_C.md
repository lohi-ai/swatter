# Angle C — cross-file tracer

Check every caller and callee of the changed symbols (grep for the symbol
names across the repo). A changed signature, return shape, error contract, or
side effect must be honored at every call site. Wrappers must route to the
wrapped instance (not back through a registry/global) and forward every method
callers use. A changed data shape must be read consistently everywhere it is
consumed. Flag any call site the diff left stale.
