import { History } from "../../wailsjs/go/main/App";
import { api, layout } from "../../wailsjs/go/models";
import { h, showError, svg } from "../dom";
import { formatDate } from "../format";
import { drawGap, drawRow, graphWidth, Line, resolveLines } from "../graph";
import { View } from "../view";

// Same words as the daemon's commit summaries.
const opLabels: Record<string, string> = {
  create: "created", modify: "modified", rename: "renamed", move: "moved", delete: "deleted",
  moved_out: "moved out", superseded: "superseded", trash_restore: "restored", trash_purge: "purged",
};

function opText(op: api.OpView): string {
  const where = op.path || `${op.from} → ${op.to}`;
  return `${opLabels[op.kind] ?? op.kind} ${where}`;
}

interface Item {
  graph: SVGElement;
  ops: HTMLElement | null;
  gap: SVGElement | null;
}

export function createHistoryView(): View {
  const filter = h("input", { type: "search", placeholder: "Show only commits that touched a path, e.g. /Photos" });
  const list = h("div", { class: "history" });
  const end = h("p", { class: "history-end muted" });
  const element = h("section", {}, h("div", { class: "toolbar" }, filter), list, end);
  let rows: layout.Row[] = [];
  let items: Item[] = [];
  let lines: Line[] = [];
  let width = 0;
  let activeFilter = "";
  let nextBefore = "";
  let complete = false;
  let loading = false;
  let generation = 0;
  let filterTimer = 0;

  const observer = new IntersectionObserver((entries) => {
    if (entries.some((entry) => entry.isIntersecting)) loadMore();
  });
  observer.observe(end);
  filter.addEventListener("input", () => {
    clearTimeout(filterTimer);
    filterTimer = window.setTimeout(reset, 300);
  });

  function reset(): void {
    generation++;
    activeFilter = filter.value.trim();
    rows = [];
    items = [];
    list.replaceChildren();
    nextBefore = "";
    complete = false;
    loading = false;
    loadMore();
  }

  async function loadMore(): Promise<void> {
    if (loading || complete) return;
    loading = true;
    const mine = generation;
    end.textContent = "Loading…";
    let page;
    try {
      page = await History(nextBefore, activeFilter);
    } catch (err) {
      if (mine === generation) {
        loading = false;
        end.textContent = "";
        showError(err);
      }
      return;
    }
    if (mine !== generation) return;
    loading = false;
    const start = rows.length;
    rows.push(...(page.rows ?? []));
    nextBefore = page.nextBefore;
    complete = nextBefore === "";
    for (let k = start; k < rows.length; k++) list.append(createItem(k));
    redraw();
    end.textContent = !complete ? "" : rows.length > 0 ? "Start of history" : activeFilter ? "No commits touched this path." : "No commits yet.";
    // Observing again reports the current position, so a short page that leaves the end in
    // view loads the next one without a scroll.
    observer.unobserve(end);
    observer.observe(end);
  }

  function redraw(): void {
    lines = resolveLines(rows);
    width = graphWidth(rows);
    items.forEach((item, k) => {
      const graph = drawRow(k, rows, lines, width, complete, activeFilter === "");
      item.graph.replaceWith(graph);
      item.graph = graph;
      if (item.ops && item.gap) redrawGap(item, k);
    });
  }

  function redrawGap(item: Item, k: number): void {
    const gap = drawGap(k, lines, width, complete, activeFilter === "");
    item.gap!.replaceWith(gap);
    item.gap = gap;
    item.ops!.style.paddingLeft = `${width}px`;
  }

  function createItem(k: number): HTMLElement {
    const row = rows[k];
    const commit = row.commit;
    const item: Item = { graph: svg("svg", {}), ops: null, gap: null };
    items.push(item);
    const line = h("div", { class: `commit lane-${row.colour}`, title: commit.id },
      item.graph,
      h("span", { class: "time" }, formatDate(commit.time)),
      h("span", { class: "device" }, (commit.deviceName || commit.device.slice(0, 8)) + (row.retired ? " (retired)" : "")),
      h("span", { class: "summary" }, commit.summary));
    const wrapper = h("div", {}, line);
    line.addEventListener("click", () => {
      if (item.ops) {
        item.ops.remove();
        item.ops = item.gap = null;
        return;
      }
      item.gap = svg("svg", {});
      item.ops = h("div", { class: "ops-row" }, item.gap, h("ul", { class: "ops" }, ...(commit.ops ?? []).map((op) => h("li", {}, opText(op)))));
      redrawGap(item, k);
      wrapper.append(item.ops);
    });
    return wrapper;
  }

  return { element, show: reset };
}
