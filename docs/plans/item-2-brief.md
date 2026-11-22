# Brief: item 2 of `ideas-from-ventoux.md`

For the session that builds it. Read `docs/plans/ideas-from-ventoux.md`
first (item 2 is the task), then this, then follow `CLAUDE.md`: questions
first, and wait for answers.

## The task

**Structured check results for Types, a dry-run check, and a JSON Schema
for the Type format.**

It is **not** about check hooks or their outcomes (allow, correct, deny,
error). Those stay as they are.

1. **Check a Type definition and report every problem.** Today `typedef`
   returns the first problem as a Go error string, and `ParseDir` stops at
   the first bad file. Instead, return every problem in a Type (or a folder
   of Types) as `{path, code, message}`:
   - `path` points into the YAML: `transitions[3].allowed[0]`.
   - `code` is stable and documented, like the API's refusal reasons.
   - `message` says what is wrong, then what to do.
2. **A dry run of `RegisterType`.** Validate a draft Type against the Types
   already loaded (links, inverses, `accepts_reactions`, targets) without
   registering it. It is not a dry run of `Transition`.
3. **The Type format as JSON Schema**, checked in CI against what `typedef`
   accepts, so the two cannot drift.

## Answers to the first round of questions

- **Scope.** Item 2 is this task's scope. It is not in the build order in
  `docs/design.md` yet: propose where it goes.
- **API contract.** The dry run changes the API contract, so propose its
  shape as a question before building it.
- **`policy.Project`.** The dry run reads no Items. Say whether you think
  `Project` applies, and who may call it.
- **Codes.** They are the kernel's, documented like the refusal reasons, not
  declared in Type YAML.
- **JSON Schema.** Say in your questions if you think it should be a step of
  its own.

## Then

Send the full list of questions, and wait for the answers before writing
tests.
