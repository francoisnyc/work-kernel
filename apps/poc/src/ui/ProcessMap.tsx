// The process map, drawn from model/process.ts: boxes, right-angled
// arrows, the current step, the steps done, and the system steps numbered
// on the map and listed beside it. The drawing is an image with a name;
// "The process as a list" says the same in text.
import { useId, type ReactElement } from "react";
import { labelWidth, type MapEdge, type MapNode, type ProcessMap as MapData, type SystemStep } from "../model/process";

const statusWords = { current: "Current step", done: "Done", ahead: "Not reached" } as const;

type Numbered = { n: number; where: string; step: SystemStep };

// The system steps in reading order: boxes left to right, then arrows.
function numberSteps(map: MapData): { list: Numbered[]; ofNode: (s: string) => number[]; ofEdge: (e: MapEdge) => number[] } {
  const list: Numbered[] = [];
  const nodeNums = new Map<string, number[]>();
  const edgeNums = new Map<MapEdge, number[]>();
  const nodes = [...map.nodes].sort((a, b) => a.column - b.column || a.row - b.row);
  for (const n of nodes)
    for (const step of n.steps) {
      list.push({ n: list.length + 1, where: `In ${n.label}`, step });
      nodeNums.set(n.state, [...(nodeNums.get(n.state) ?? []), list.length]);
    }
  const label = (s: string) => map.nodes.find((n) => n.state === s)?.label ?? s;
  for (const e of map.edges)
    for (const step of e.steps) {
      list.push({ n: list.length + 1, where: `${e.label} (${label(e.from)} to ${label(e.to)})`, step });
      edgeNums.set(e, [...(edgeNums.get(e) ?? []), list.length]);
    }
  return { list, ofNode: (s) => nodeNums.get(s) ?? [], ofEdge: (e) => edgeNums.get(e) ?? [] };
}

function Marker({ x, y, n }: { x: number; y: number; n: number }): ReactElement {
  return (
    <g className="pm-marker">
      <circle cx={x} cy={y} r={8} />
      <text x={x} y={y + 3.5} textAnchor="middle">
        {n}
      </text>
    </g>
  );
}

function Box({ node, nums }: { node: MapNode; nums: number[] }): ReactElement {
  const cx = node.x + node.width / 2;
  const cy = node.y + node.height / 2;
  return (
    <g className={`pm-node pm-${node.status}`}>
      <rect x={node.x} y={node.y} width={node.width} height={node.height} rx={8} />
      <text className="pm-node-label" x={cx} y={node.status === "ahead" ? cy + 4 : cy - 2} textAnchor="middle">
        {node.label}
      </text>
      {node.status !== "ahead" && (
        <text className="pm-node-status" x={cx} y={cy + 14} textAnchor="middle">
          {node.status === "done" ? "✓ Done" : "● Current step"}
        </text>
      )}
      {nums.map((n, i) => (
        <Marker key={n} x={node.x + node.width - 10 - i * 18} y={node.y} n={n} />
      ))}
    </g>
  );
}

function Arrow({ edge, nums, arrowId }: { edge: MapEdge; nums: number[]; arrowId: string }): ReactElement {
  const d = edge.points.map((p, i) => `${i === 0 ? "M" : "L"}${p.x} ${p.y}`).join(" ");
  const w = labelWidth(edge.label);
  return (
    <g className={`pm-edge${edge.taken ? " pm-taken" : ""}${edge.system ? " pm-system" : ""}`}>
      <path d={d} markerEnd={`url(#${arrowId})`} />
      <text className="pm-edge-label" x={edge.labelAt.x} y={edge.labelAt.y + 4} textAnchor="middle">
        {edge.label}
      </text>
      {nums.map((n, i) => (
        <Marker key={n} x={edge.labelAt.x + w / 2 + 6 + i * 18} y={edge.labelAt.y} n={n} />
      ))}
    </g>
  );
}

export function ProcessMap({ map, name }: { map: MapData; name: string }): ReactElement {
  const id = useId();
  const arrowId = `${id}-arrow`.replace(/:/g, "");
  const { list, ofNode, ofEdge } = numberSteps(map);
  const current = map.nodes.find((n) => n.status === "current");
  const label = (s: string) => map.nodes.find((n) => n.state === s)?.label ?? s;
  return (
    <div className="process">
      {/* Focusable, so a keyboard can scroll a map wider than the page. */}
      <div className="process-scroll" tabIndex={0} role="group" aria-label="Process map, scrolls sideways">
        <svg
          className="process-map"
          role="img"
          aria-labelledby={`${id}-title`}
          width={map.width + (list.length > 0 ? 40 : 0)}
          height={map.height}
          viewBox={`0 0 ${map.width + (list.length > 0 ? 40 : 0)} ${map.height}`}
        >
          <title id={`${id}-title`}>{`Process map: ${name}${current ? `, now at ${current.label}` : ""}`}</title>
          <defs>
            <marker id={arrowId} viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
              <path d="M0 0 L10 5 L0 10 z" className="pm-arrowhead" />
            </marker>
          </defs>
          {map.edges.map((e) => (
            <Arrow key={`${e.from}>${e.to}`} edge={e} nums={ofEdge(e)} arrowId={arrowId} />
          ))}
          {map.nodes.map((n) => (
            <Box key={n.state} node={n} nums={ofNode(n.state)} />
          ))}
        </svg>
      </div>
      <div className="process-side">
        <ul className="plain process-legend" aria-label="Legend">
          <li>
            <span className="legend-swatch legend-current" aria-hidden="true" /> Current step
          </li>
          <li>
            <span className="legend-swatch legend-done" aria-hidden="true" /> Done
          </li>
          <li>
            <span className="legend-line legend-taken" aria-hidden="true" /> A move made
          </li>
          <li>
            <span className="legend-line legend-system" aria-hidden="true" /> Only the system makes it
          </li>
        </ul>
        {list.length > 0 && (
          <section aria-labelledby={`${id}-system`}>
            <h3 id={`${id}-system`} className="process-side-title">
              System steps
            </h3>
            <ol className="plain process-steps">
              {list.map((s) => (
                <li key={s.n}>
                  <span className="pm-number" aria-hidden="true">
                    {s.n}
                  </span>
                  <span>
                    <span className="muted">{s.where}:</span> {s.step.text}
                  </span>
                </li>
              ))}
            </ol>
          </section>
        )}
      </div>
      <details className="process-list">
        <summary>The process as a list</summary>
        <ul aria-label="Steps">
          {[...map.nodes]
            .sort((a, b) => a.column - b.column || a.row - b.row)
            .map((n) => {
              const moves = map.edges.filter((e) => e.from === n.state);
              return (
                <li key={n.state}>
                  <strong>{n.label}</strong> — {statusWords[n.status]}
                  {(n.steps.length > 0 || moves.length > 0) && (
                    <ul>
                      {n.steps.map((s, i) => (
                        <li key={`s${i}`}>System: {s.text}</li>
                      ))}
                      {moves.map((e) => (
                        <li key={e.to}>
                          {e.label} → {label(e.to)} ({e.who}){e.taken ? ", made" : ""}
                          {e.steps.map((s) => `; system: ${s.text}`).join("")}
                        </li>
                      ))}
                    </ul>
                  )}
                </li>
              );
            })}
        </ul>
      </details>
    </div>
  );
}
