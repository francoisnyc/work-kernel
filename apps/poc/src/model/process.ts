// The process map: a Type's states as boxes, its moves as arrows, and the
// automatic steps it declares (checks, timers, reactions, links the kernel
// sets) as system steps. Read from GetType only, so it is generic over the
// Type: nothing here names a vacation state.
//
// Layout, kept simple (no diagram library):
//   - one column per state category, in category order (triage, backlog,
//     todo, active, waiting); done, canceled and none share a last "end"
//     column, done first. Inside a column, states in declared order.
//   - arrows are right-angled lines. A move to the next column turns in the
//     gap between the two; one that skips columns runs along a track below
//     the boxes; one that goes back runs along a track above them; one
//     inside a column loops in the gap on its right. Every arrow has its
//     own lane in a gap, its own track, and its own port on a box's side,
//     so no two arrows that share no end run along each other.
//   - labels sit on the arrow's longest free stretch, and move aside when
//     they would cover a box or another label.
import { ReactionKind, ReactionTrigger, StateCategory, type ItemEvent, type Transition, type Type } from "@gen/kernel/v1/kernel_pb";
import { ItemEventKind } from "@gen/kernel/v1/kernel_pb";
import { fieldLabel, humanize, stateLabel, transitionLabel, typeIdOf } from "./labels";
import { personName } from "./people";

export type StepKind = "check" | "timer" | "reminder" | "reaction" | "link";
export type SystemStep = { kind: StepKind; text: string };
export type Status = "current" | "done" | "ahead";
export type Route = "next" | "skip" | "back" | "same";
export type Point = { x: number; y: number };

export type MapNode = {
  state: string;
  label: string;
  category: StateCategory;
  column: number;
  row: number;
  x: number;
  y: number;
  width: number;
  height: number;
  status: Status;
  steps: SystemStep[];
};

export type MapEdge = {
  from: string;
  to: string;
  label: string;
  who: string;
  route: Route;
  // Only the system makes this move (allowed names principal:system:...).
  system: boolean;
  // The Item has made this move.
  taken: boolean;
  steps: SystemStep[];
  points: Point[];
  // The label's centre.
  labelAt: Point;
};

export type ProcessMap = { width: number; height: number; nodes: MapNode[]; edges: MapEdge[] };

// Where an Item stands: its state, the states it has been in (first visit
// order) and the moves it made ("from>to").
export type Progress = { current: string; visited: string[]; taken: string[] };

// --- Words ---

// "120h" -> "5 days", "36h" -> "1 day 12 h", "90m" -> "1 h 30 min".
export function durationLabel(d: string): string {
  let secs = 0;
  for (const [, n, unit] of d.matchAll(/(\d+(?:\.\d+)?)(h|ms|m|s|us|µs|ns)/g)) {
    const v = Number(n);
    secs += unit === "h" ? v * 3600 : unit === "m" ? v * 60 : unit === "s" ? v : 0;
  }
  const days = Math.floor(secs / 86_400);
  const h = Math.floor((secs % 86_400) / 3600);
  const min = Math.floor((secs % 3600) / 60);
  const s = Math.round(secs % 60);
  const parts: string[] = [];
  if (days > 0) parts.push(`${days} ${days === 1 ? "day" : "days"}`);
  if (h > 0) parts.push(`${h} h`);
  if (min > 0) parts.push(`${min} min`);
  if (s > 0 || parts.length === 0) parts.push(`${s} s`);
  return parts.join(" ");
}

// "every day" rather than "every 1 day".
function everyLabel(d: string): string {
  const l = durationLabel(d);
  return l === "1 day" ? "day" : l;
}

const onlySystem = (t: Transition) => t.allowed.length > 0 && t.allowed.every((a) => a.startsWith("principal:system:"));

// Who may take a move, from its allowed and except lists.
export function whoMay(t: Transition): string {
  const parts: string[] = [];
  for (const a of t.allowed) {
    const word =
      a === "creator"
        ? "Requester"
        : a.startsWith("field:")
          ? fieldLabel(a.slice("field:".length))
          : a.startsWith("role:")
            ? humanize(a.slice("role:".length))
            : a.startsWith("principal:system:")
              ? "System"
              : a.startsWith("principal:")
                ? personName(a.slice("principal:".length))
                : a;
    if (!parts.includes(word)) parts.push(word);
  }
  const who = parts.length === 0 ? "Anyone" : parts.join(" or ");
  return t.except.includes("creator") ? `${who} (not the requester)` : who;
}

// What an action hook is, said for a timer that runs it.
const hookWords: Record<string, string> = { remind_approver: "Reminder to the approver" };

const article = (w: string) => (/^[aeiou]/i.test(w) ? `an ${w}` : `a ${w}`);
const typeWord = (typeName: string) => humanize(typeName.includes("/") ? typeIdOf(typeName) || typeName.split("/").pop()! : typeName).toLowerCase();
const exprWord = (e: string) => (/^f_\w+$/.test(e) ? fieldLabel(e) : e);

function reactionText(type: Type, r: Type["reactions"][number]): string {
  switch (r.kind) {
    case ReactionKind.HELD: {
      const adds = Object.entries(r.add).map(([f, e]) => `${exprWord(e)} to ${fieldLabel(f)}`);
      return `Adds ${adds.join(", ")} on the ${humanize(r.linkType).toLowerCase()}, before the move applies`;
    }
    case ReactionKind.AFTER:
      return `Gives back what ${humanize(r.undoes)} took`;
    case ReactionKind.MOVE:
      if (r.trigger === ReactionTrigger.CREATED_INSIDE)
        return `Moves here when ${article(r.ofTypes.map(typeWord).join(" or "))} is added inside`;
      return `Moves the ${humanize(r.linkType || "linked items").toLowerCase()} to ${stateLabel(type.typeId, r.toState)}`;
    case ReactionKind.CREATE:
      return `Creates ${article(typeWord(r.createType))}`;
    case ReactionKind.SET:
      return `Sets ${Object.keys(r.setFields).map((f) => fieldLabel(f).toLowerCase()).join(", ")} on the ${r.target || "target"}`;
    default:
      return `Runs ${humanize(r.id)}`;
  }
}

// The system steps of every arrow ("from>to") and every box (state).
function stepsOf(type: Type): { edges: Map<string, SystemStep[]>; nodes: Map<string, SystemStep[]> } {
  const edges = new Map<string, SystemStep[]>();
  const nodes = new Map<string, SystemStep[]>();
  const has = (from: string, to: string) => type.transitions.some((t) => t.fromState === from && t.toState === to);
  const onEdge = (from: string, to: string, s: SystemStep) => edges.set(`${from}>${to}`, [...(edges.get(`${from}>${to}`) ?? []), s]);
  const onNode = (state: string, s: SystemStep) => nodes.set(state, [...(nodes.get(state) ?? []), s]);

  for (const t of type.transitions) if (t.checked) onEdge(t.fromState, t.toState, { kind: "check", text: "Checked by the system before it applies" });

  for (const st of type.states) {
    if (st.timer) {
      const s: SystemStep = { kind: "timer", text: `After ${durationLabel(st.timer.after)}` };
      if (has(st.id, st.timer.toState)) onEdge(st.id, st.timer.toState, s);
      else onNode(st.id, { ...s, text: `${s.text}, moves to ${stateLabel(type.typeId, st.timer.toState)}` });
    }
    if (st.at) {
      const s: SystemStep = { kind: "timer", text: `When ${fieldLabel(st.at.field).toLowerCase()} passes` };
      if (has(st.id, st.at.toState)) onEdge(st.id, st.at.toState, s);
      else onNode(st.id, s);
    }
  }

  // Links the kernel sets on entering a state, then the state's acting timers.
  for (const st of type.states) {
    for (const l of type.linkTypes)
      if (l.resolve?.onEnter === st.id)
        onNode(st.id, {
          kind: "link",
          text: `Links the ${l.resolve.equals === "creator" ? "requester's " : ""}${l.targets.map(typeWord).join(" or ")}`,
        });
    for (const a of st.acting) {
      const what = hookWords[a.hook] ?? humanize(a.hook || a.id);
      const text = a.atField
        ? `${what} when ${fieldLabel(a.atField).toLowerCase()} passes`
        : `${what} after ${durationLabel(a.after)}${a.max > 1 ? `, then every ${everyLabel(a.every)}, ${a.max} at most` : ""}`;
      onNode(st.id, { kind: "reminder", text });
    }
  }

  for (const r of type.reactions) {
    const s: SystemStep = { kind: "reaction", text: reactionText(type, r) };
    const inside = r.kind === ReactionKind.MOVE && r.trigger === ReactionTrigger.CREATED_INSIDE;
    const to = inside ? r.toState : r.state;
    const froms = inside ? r.targetFromStates : r.fromStates;
    const declared = froms.filter((f) => has(f, to));
    if (to !== "" && declared.length > 0) for (const f of declared) onEdge(f, to, s);
    else if (to !== "") onNode(to, s);
  }
  return { edges, nodes };
}

// --- Progress ---

export function progressOf(type: Type, state: string, events: ItemEvent[]): Progress {
  const visited: string[] = [];
  const taken: string[] = [];
  const visit = (s: string) => s !== "" && !visited.includes(s) && visited.push(s);
  for (const ev of events) {
    if (ev.kind === ItemEventKind.CREATED) visit(ev.toState || type.initialState);
    if (ev.kind === ItemEventKind.TRANSITIONED) {
      visit(ev.fromState);
      visit(ev.toState);
      const key = `${ev.fromState}>${ev.toState}`;
      if (!taken.includes(key)) taken.push(key);
    }
  }
  visit(state);
  return { current: state, visited, taken };
}

// --- Layout ---

const M = 24; // margin
const BOX_W = 168;
const BOX_MIN_H = 56;
const PORT = 12; // between two ports on a box's side
const LANE = 12; // between two lanes in a gap
const TRACK = 16; // between two tracks above or below
const ROW_GAP = 28;
const LABEL_ROOM = 104; // a gap's stretch before the next column, for labels
const LABEL_H = 14;

// The column of a category: its rank among the categories present, the
// end states last.
function rank(c: StateCategory): number {
  switch (c) {
    case StateCategory.TRIAGE:
      return 1;
    case StateCategory.BACKLOG:
      return 2;
    case StateCategory.TODO:
      return 3;
    case StateCategory.ACTIVE:
      return 4;
    case StateCategory.WAITING:
      return 5;
    default:
      return 9;
  }
}

type Port = { edge: number; box: string; side: "left" | "right"; other: number };

export function layoutProcess(type: Type, progress?: Progress): ProcessMap {
  const steps = stepsOf(type);
  const ranks = [...new Set(type.states.map((s) => rank(s.category)))].sort((a, b) => a - b);
  const colOf = new Map<string, number>();
  const rowOf = new Map<string, number>();
  const perCol: string[][] = ranks.map(() => []);
  for (const s of type.states) {
    const c = ranks.indexOf(rank(s.category));
    colOf.set(s.id, c);
    rowOf.set(s.id, perCol[c]!.length);
    perCol[c]!.push(s.id);
  }
  const cols = perCol.length;
  const rows = Math.max(1, ...perCol.map((c) => c.length));

  // Routes.
  const transitions = type.transitions.filter((t) => colOf.has(t.fromState) && colOf.has(t.toState));
  const routes: Route[] = transitions.map((t) => {
    const a = colOf.get(t.fromState)!;
    const b = colOf.get(t.toState)!;
    return b === a ? "same" : b === a + 1 ? "next" : b > a ? "skip" : "back";
  });

  // Ports: every arrow end on a box's side, grouped by the gap the side
  // faces and the box's row, so that no two stubs in a gap share a height.
  // A box's right side faces gap c, its left side gap c - 1.
  const ports = new Map<string, Port[]>(); // `${gap}:${row}`
  const addPort = (edge: number, box: string, side: "left" | "right", other: string) => {
    const gap = side === "right" ? colOf.get(box)! : colOf.get(box)! - 1;
    const key = `${gap}:${rowOf.get(box)}`;
    ports.set(key, [...(ports.get(key) ?? []), { edge, box, side, other: rowOf.get(other)! }]);
  };
  transitions.forEach((t, i) => {
    switch (routes[i]) {
      case "next":
      case "skip":
        addPort(i, t.fromState, "right", t.toState);
        addPort(i, t.toState, "left", t.fromState);
        break;
      case "back":
        addPort(i, t.fromState, "left", t.toState);
        addPort(i, t.toState, "right", t.fromState);
        break;
      case "same":
        addPort(i, t.fromState, "right", t.toState);
        addPort(i, t.toState, "right", t.fromState);
        break;
    }
  });
  const most = Math.max(1, ...[...ports.values()].map((p) => p.length));
  const boxH = Math.max(BOX_MIN_H, (most + 1) * PORT);

  // Lanes: the vertical stretch of an arrow in a gap, one per arrow.
  const lanes = new Map<number, { edge: number; order: number }[]>();
  const addLane = (gap: number, edge: number, order: number) => lanes.set(gap, [...(lanes.get(gap) ?? []), { edge, order }]);
  const backs: number[] = [];
  const skips: number[] = [];
  transitions.forEach((t, i) => {
    const a = colOf.get(t.fromState)!;
    const b = colOf.get(t.toState)!;
    const down = rowOf.get(t.toState)! - rowOf.get(t.fromState)!;
    switch (routes[i]) {
      case "next":
        // Arrows fanning down take lanes nearest their source first, the
        // farthest target nearest, so they do not cross each other.
        addLane(a, i, -down);
        break;
      case "same":
        addLane(a, i, 100 + Math.abs(down));
        break;
      case "skip":
        addLane(a, i, -50);
        addLane(b - 1, i, 50);
        skips.push(i);
        break;
      case "back":
        addLane(a - 1, i, 60);
        addLane(b, i, 60);
        backs.push(i);
        break;
    }
  });
  for (const l of lanes.values()) l.sort((x, y) => x.order - y.order || x.edge - y.edge);

  // Column positions: each gap is wide enough for its lanes and a label.
  const gapWidth = (g: number) => 16 + (lanes.get(g)?.length ?? 0) * LANE + LABEL_ROOM;
  const colX: number[] = [];
  let x = M;
  for (let c = 0; c < cols; c++) {
    colX.push(x);
    x += BOX_W + gapWidth(c);
  }
  const rightEdge = x;
  const top = M + backs.length * TRACK + (backs.length > 0 ? 8 : 0);
  const rowY = (r: number) => top + r * (boxH + ROW_GAP);
  const boxesBottom = rowY(rows - 1) + boxH;
  const height = boxesBottom + (skips.length > 0 ? 16 + skips.length * TRACK : 0) + M;

  const nodes: MapNode[] = type.states.map((s) => ({
    state: s.id,
    label: stateLabel(type.typeId, s.id),
    category: s.category,
    column: colOf.get(s.id)!,
    row: rowOf.get(s.id)!,
    x: colX[colOf.get(s.id)!]!,
    y: rowY(rowOf.get(s.id)!),
    width: BOX_W,
    height: boxH,
    status: !progress ? "ahead" : s.id === progress.current ? "current" : progress.visited.includes(s.id) ? "done" : "ahead",
    steps: steps.nodes.get(s.id) ?? [],
  }));
  const box = new Map(nodes.map((n) => [n.state, n]));

  const portY = (edge: number, boxId: string, side: "left" | "right"): number => {
    const n = box.get(boxId)!;
    const gap = side === "right" ? n.column : n.column - 1;
    const list = [...(ports.get(`${gap}:${n.row}`) ?? [])].sort((p, q) => p.other - q.other || p.edge - q.edge);
    const i = list.findIndex((p) => p.edge === edge && p.box === boxId && p.side === side);
    return n.y + boxH / 2 + (i - (list.length - 1) / 2) * PORT;
  };
  const laneX = (gap: number, edge: number): number => {
    const l = lanes.get(gap)!;
    const left = colX[gap]! + BOX_W;
    return left + 16 + l.findIndex((e) => e.edge === edge) * LANE;
  };

  const edges: MapEdge[] = transitions.map((t, i) => {
    const a = box.get(t.fromState)!;
    const b = box.get(t.toState)!;
    let points: Point[];
    switch (routes[i]) {
      case "next": {
        const y1 = portY(i, a.state, "right");
        const y2 = portY(i, b.state, "left");
        const lx = laneX(a.column, i);
        points = [
          { x: a.x + a.width, y: y1 },
          { x: lx, y: y1 },
          { x: lx, y: y2 },
          { x: b.x, y: y2 },
        ];
        break;
      }
      case "same": {
        const y1 = portY(i, a.state, "right");
        const y2 = portY(i, b.state, "right");
        const lx = laneX(a.column, i);
        points = [
          { x: a.x + a.width, y: y1 },
          { x: lx, y: y1 },
          { x: lx, y: y2 },
          { x: b.x + b.width, y: y2 },
        ];
        break;
      }
      case "skip": {
        const y1 = portY(i, a.state, "right");
        const y2 = portY(i, b.state, "left");
        const l1 = laneX(a.column, i);
        const l2 = laneX(b.column - 1, i);
        const ty = boxesBottom + 16 + skips.indexOf(i) * TRACK;
        points = [
          { x: a.x + a.width, y: y1 },
          { x: l1, y: y1 },
          { x: l1, y: ty },
          { x: l2, y: ty },
          { x: l2, y: y2 },
          { x: b.x, y: y2 },
        ];
        break;
      }
      case "back": {
        const y1 = portY(i, a.state, "left");
        const y2 = portY(i, b.state, "right");
        const l1 = laneX(a.column - 1, i);
        const l2 = laneX(b.column, i);
        const ty = M + backs.indexOf(i) * TRACK;
        points = [
          { x: a.x, y: y1 },
          { x: l1, y: y1 },
          { x: l1, y: ty },
          { x: l2, y: ty },
          { x: l2, y: y2 },
          { x: b.x + b.width, y: y2 },
        ];
        break;
      }
    }
    // Drop a corner that makes no turn (two stubs at one height).
    points = points!.filter((p, k, all) => {
      if (k === 0 || k === all.length - 1) return true;
      const prev = all[k - 1]!;
      const next = all[k + 1]!;
      return !((prev.x === p.x && p.x === next.x) || (prev.y === p.y && p.y === next.y));
    });
    return {
      from: t.fromState,
      to: t.toState,
      label: transitionLabel(type.typeId, t.fromState, t.toState),
      who: whoMay(t),
      route: routes[i]!,
      system: onlySystem(t),
      taken: progress?.taken.includes(`${t.fromState}>${t.toState}`) ?? false,
      steps: steps.edges.get(`${t.fromState}>${t.toState}`) ?? [],
      points,
      labelAt: { x: 0, y: 0 },
    };
  });

  // Labels: the first free spot along the arrow's horizontal stretches,
  // preferring the track (back, skip) or the stretch into the target.
  const placed: { x: number; y: number; w: number; h: number }[] = [];
  const free = (r: { x: number; y: number; w: number; h: number }) =>
    r.x >= 0 &&
    r.y >= 0 &&
    ![...nodes.map((n) => ({ x: n.x - 2, y: n.y - 2, w: n.width + 4, h: n.height + 4 })), ...placed].some(
      (o) => r.x < o.x + o.w && r.x + r.w > o.x && r.y < o.y + o.h && r.y + r.h > o.y,
    );
  let widest = rightEdge;
  for (const e of edges) {
    const w = labelWidth(e.label);
    const segs = e.points.slice(1).map((p, k) => [e.points[k]!, p] as const);
    const horizontal = segs.filter(([p, q]) => p.y === q.y);
    const vertical = segs.filter(([p, q]) => p.x === q.x);
    const preferred = e.route === "back" || e.route === "skip" ? [horizontal[Math.floor(horizontal.length / 2)]!, ...horizontal] : [...horizontal].reverse();
    const spots: Point[] = [];
    for (const [p, q] of preferred) for (const f of [0.5, 0.3, 0.7, 0.15, 0.85]) spots.push({ x: p.x + (q.x - p.x) * f, y: p.y - LABEL_H / 2 - 2 });
    for (const [p, q] of vertical) for (const f of [0.5, 0.25, 0.75]) spots.push({ x: p.x + 4 + w / 2, y: p.y + (q.y - p.y) * f });
    for (let k = 1; k <= 4; k++) for (const [p, q] of preferred) spots.push({ x: (p.x + q.x) / 2, y: p.y - LABEL_H / 2 - 2 - k * LABEL_H });
    const rect = (s: Point) => ({ x: s.x - w / 2, y: s.y - LABEL_H / 2, w, h: LABEL_H });
    const spot = spots.find((s) => free(rect(s))) ?? spots[0]!;
    const at = { x: Math.max(w / 2, spot.x), y: Math.max(LABEL_H / 2, spot.y) };
    placed.push(rect(at));
    e.labelAt = at;
    widest = Math.max(widest, at.x + w / 2 + 4);
  }

  return { width: Math.ceil(Math.max(widest, rightEdge)), height, nodes, edges };
}

// A label's width at the map's 12 px type, roughly.
export function labelWidth(text: string): number {
  return Math.ceil(text.length * 6.6) + 10;
}
