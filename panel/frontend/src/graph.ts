import { layout } from "../wailsjs/go/models";
import { h, svg } from "./dom";

export const rowHeight = 28;
const laneWidth = 14;
const middle = rowHeight / 2;
const paletteSize = 8;
const unknownLane = -1;

// A line runs from a commit down to its parent. It leaves the commit's dot, bends into the
// parent's lane, and runs down that lane; bending across lanes is what marks a merge.
export interface Line {
  from: number;
  fromLane: number;
  to: number | null;
  lane: number;
}

// resolveLines matches parents by ID across every loaded page, so a line that was off the
// page when its commit loaded joins up once the parent's page arrives.
export function resolveLines(rows: layout.Row[]): Line[] {
  const rowOf = new Map(rows.map((row, i) => [row.commit.id, i]));
  const lines: Line[] = [];
  rows.forEach((row, i) => {
    for (const edge of row.edges ?? []) {
      const to = rowOf.get(edge.parent) ?? null;
      lines.push({ from: i, fromLane: row.lane, to, lane: to === null ? edge.toLane : rows[to].lane });
    }
  });
  return lines;
}

export function graphWidth(rows: layout.Row[]): number {
  return laneX(Math.max(0, ...rows.map((row) => row.lane))) + laneWidth / 2 + 2;
}

function laneX(lane: number): number {
  return laneWidth / 2 + 2 + lane * laneWidth;
}

function laneClass(lane: number): string {
  return `lane-${lane % paletteSize}`;
}

function segment(lane: number, d: string, dashed = false): SVGElement {
  return svg("path", { d, class: laneClass(lane) + (dashed ? " dashed" : "") });
}

// drawRow draws row k's slice of the graph. Lines whose parent is not loaded run to the
// bottom while more pages may come, and end in a short dashed stub once history is complete.
export function drawRow(k: number, rows: layout.Row[], lines: Line[], width: number, complete: boolean, edges: boolean): SVGElement {
  const graph = svg("svg", { class: "graph", width, height: rowHeight });
  const row = rows[k];
  for (const line of edges ? lines : []) {
    const open = line.to === null && !complete && line.lane !== unknownLane;
    if (line.from === k) {
      const x0 = laneX(line.fromLane);
      if (line.to === null && !open) {
        graph.append(segment(row.lane, `M ${x0} ${middle} L ${x0 + (line.lane === unknownLane ? 5 : 0)} ${rowHeight}`, true));
        continue;
      }
      const x = laneX(line.lane);
      graph.append(segment(line.lane, `M ${x0} ${middle} C ${x0} ${rowHeight}, ${x} ${middle}, ${x} ${rowHeight}`));
    } else if (line.from < k && (line.to === null ? open : line.to >= k)) {
      const x = laneX(line.lane);
      graph.append(segment(line.lane, `M ${x} 0 L ${x} ${line.to === k ? middle : rowHeight}`));
    }
  }
  graph.append(svg("circle", { cx: laneX(row.lane), cy: middle, r: 4, class: laneClass(row.lane) + (row.retired ? " retired" : "") }));
  return graph;
}

// drawGap continues the lanes that pass row k through its expanded operation list. Plain
// divs stretch with the list, which a fixed-height SVG would not.
export function drawGap(k: number, lines: Line[], complete: boolean, edges: boolean): HTMLElement {
  const lanes = new Set<number>();
  for (const line of edges ? lines : []) {
    const open = line.to === null && !complete && line.lane !== unknownLane;
    if (line.from <= k && ((line.to ?? -1) > k || open)) lanes.add(line.lane);
  }
  return h("div", { class: "gap" }, ...[...lanes].map((lane) => h("span", { class: laneClass(lane), style: `left: ${laneX(lane) - 1}px` })));
}
