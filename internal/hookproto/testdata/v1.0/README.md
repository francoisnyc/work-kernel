# pli.hooks/v1, frozen fixtures

These files are the protocol as first released. **Never edit or delete
them.** `TestV1Fixtures_OnlyGrow` reads each into the current Go types and
writes it back: every field here must come back with the same value. A
field may be added to the envelopes (add a new fixture showing it); none
may be removed, renamed or retyped within v1.
