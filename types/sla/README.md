# The `sla` package

Three Types that give any piece of work measured promises ("answered within
an hour", "resolved within a day") without the kernel knowing what an SLA is:

| Type | What it is |
| --- | --- |
| `sla_goal@1` | One promise about one Item (its **host**): a metric, a target in seconds of working time, a due instant, a breach mark, an outcome. It is an Item **contained in its host**, so it has the host's readers. |
| `calendar@1` | The working hours a goal counts on: a week, days off, a time zone. |
| `sla_policy@1` | The targets: seconds per metric (`first_response`, `resolution`) and priority (`p1` to `p4`). Only the role `sla_admin` writes them. |

The package names no host. A Type becomes a host by declaring what follows;
`incident@1` in `desk@1` (one folder up) is the worked example.

## What a goal does by itself

- States: `running` -> `paused` -> `running` ... -> `completed`, or `canceled`.
  `f_outcome` (`met` or `missed`) is set on `completed`.
- Every number comes from the time of the host's event that caused it
  (`cause.at`), never from when the worker got to it.
- At `f_due_at` a timer sets `f_breached_at` to that instant. The goal goes on
  running: breach is a mark, not an end. A met goal never carries a mark.
- It tells the host's `breach_contact` of the breach by email, once (see
  "The offer").
- It writes its due instant on its host (see "Two fields").

## The contract: what a host declares

### 1. Reactions, with these IDs

`sla_goal` consents by reaction ID (`*:<id>` in its `accepts_reactions`), from
any Type, as long as the goal is contained in the Item that reacts. The IDs
are fixed:

| ID | Kind | What the host makes it do |
| --- | --- | --- |
| `sla_start_first_response` | create | start a `first_response` goal |
| `sla_start_resolution` | create | start a `resolution` goal |
| `sla_restart_resolution` | create | start another `resolution` goal (a new cycle, after reopening); the old one stays as history, and the newest by `f_started_at` is current |
| `sla_pause` | move children to `paused` | stop the clock (from `running`) |
| `sla_resume` | move children to `running` | start it again (from `paused`) |
| `sla_stop` | move children to `completed` | end the goals: met or missed |
| `sla_answered` | move children to `completed` | end a goal on an answer (an Item created inside the host) |
| `sla_cancel` | move children to `canceled` | end the goals with no outcome |

*When* each runs is the host's business: which state pauses, what counts as
an answer. A host uses the IDs it needs.

A create reaction sets, and only sets: `f_metric`, `f_target_s` (seconds),
`f_started_at` (`cause.at`), the calendar (`f_week`, `f_days_off`, `f_zone`)
and `f_due_at`, which is
`working_after(cause.at, <target>, calendar(<week>, <days off>, <zone>))`.
The target and the calendar are **copied**: a running goal keeps what it
started with.

### 2. Where the target and the calendar come from

A create reaction may read the Items linked from the host's container
(`container.linked.<link>.<field>`). So the host's container links to a
`calendar` and an `sla_policy` (each at most one, written by `sla_admin`
only: whoever asks for work must not choose its hours or its targets), and
the reaction reads `container.linked.calendar.f_week`, `...f_days_off`,
`...f_zone` and the policy's field for its priority, for example
`container.linked.sla_policy.f_resolution_s_p1`. Both Types list those fields
in `readable_by_reactions`; nothing else of them is readable.

### 3. Two fields

The host declares, as `datetime`, `writable_by: kernel`:

- `f_due_first_response`
- `f_due_resolution`

and consents, in its `accepts_reactions`, to the goal's set reactions, each
for its one field:

```yaml
accepts_reactions:
  sla_goal:show_first_response:   {sets: [f_due_first_response]}
  sla_goal:hide_first_response:   {sets: [f_due_first_response]}
  sla_goal:clear_first_response:  {sets: [f_due_first_response]}
  sla_goal:cancel_first_response: {sets: [f_due_first_response]}
  sla_goal:show_resolution:       {sets: [f_due_resolution]}
  sla_goal:hide_resolution:       {sets: [f_due_resolution]}
  sla_goal:clear_resolution:      {sets: [f_due_resolution]}
  sla_goal:cancel_resolution:     {sets: [f_due_resolution]}
```

A goal sets its field to `f_due_at` while it runs and unsets it when it
pauses, completes or is canceled. The copy lags the goal by one reaction
step; the goal is the truth. A queue sorts the hosts by one of the two
fields (`order_by`); "overdue now" is a range on it; "ever breached" is a
search for goals whose `f_breached_at` is not empty.

### 4. The offer

```yaml
offers:
  breach_contact: f_assignee   # any principal field of the host, holding one value
```

The goal's breach hook reads `container:breach_contact`: the value of that
field **when the hook runs**. With nobody to tell (the field is unset, or
that person cannot see the host) no mail is sent and the goal records
`hook_failed` / `no_recipient` once; nothing is retried.

## Not in the package

A third metric (it would be `sla_goal@2`), recomputing a running goal when
the priority or the policy changes, a clock that pauses during a hold, and
sorting by the nearest deadline of any metric are Later.
