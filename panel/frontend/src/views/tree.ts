import { Download, Free, Open, ShowInFiles, Tree } from "../../wailsjs/go/main/App";
import { api } from "../../wailsjs/go/models";
import { contextMenu, h, showError, showNotice } from "../dom";
import { formatBytes } from "../format";
import { View } from "../view";

const stateIcons: Record<string, [string, string]> = {
  cloud: ["☁", "Cloud only"],
  partial: ["◐", "Partly on this device"],
  local: ["●", "On this device"],
  dirty: ["✎", "Changed, not uploaded yet"],
  corrupt: ["⚠", "Failed an integrity check"],
};

// A folder's own state says nothing about its contents, so its icon comes from how much of
// it is on this device.
function displayState(entry: api.TreeEntry): string {
  if (!entry.folder) return entry.state;
  if (entry.localSize === 0 && entry.size > 0) return "cloud";
  return entry.localSize < entry.size ? "partial" : "local";
}

function sizeText(entry: api.TreeEntry): string {
  if (!entry.folder) return formatBytes(entry.size);
  return `${formatBytes(entry.size)} · ${formatBytes(entry.localSize)} on this device`;
}

export function createTreeView(): View {
  const root = h("div", { class: "tree" });
  const element = h("section", {},
    h("div", { class: "toolbar" }, h("span", { class: "spacer" }), h("button", { onclick: () => load() }, "Refresh")),
    root);
  const expanded = new Set<string>();

  async function load(): Promise<void> {
    await renderFolder("/", root);
  }

  async function renderFolder(folder: string, container: HTMLElement): Promise<void> {
    let entries: api.TreeEntry[];
    try {
      entries = (await Tree(folder)) ?? [];
    } catch (err) {
      showError(err);
      return;
    }
    entries.sort((a, b) => Number(b.folder) - Number(a.folder) || a.name.localeCompare(b.name));
    if (folder === "/" && entries.length === 0) {
      container.replaceChildren(h("p", { class: "muted" }, "The vault is empty."));
      return;
    }
    container.replaceChildren(...entries.map((entry) => node(entry, folder, container)));
  }

  function node(entry: api.TreeEntry, parent: string, parentContainer: HTMLElement): HTMLElement {
    const [icon, label] = stateIcons[displayState(entry)] ?? ["", ""];
    const open = expanded.has(entry.path);
    const children = h("div", { class: "tree-children", hidden: !open });
    const twisty = h("span", { class: "twisty" }, entry.folder ? (open ? "▾" : "▸") : "");
    const row = h("div", { class: "tree-row", title: entry.path },
      twisty,
      h("span", { class: "file-state", "data-state": displayState(entry), title: label }, icon),
      h("span", { class: "name" }, entry.name),
      h("span", { class: "size" }, sizeText(entry)));
    const refreshParent = () => renderFolder(parent, parentContainer);
    row.addEventListener("contextmenu", (event) => contextMenu(event, [
      ["Open", () => Open(entry.path).catch(showError)],
      ["Show in Files", () => ShowInFiles(entry.path).catch(showError)],
      ["Download", () => {
        showNotice(`Downloading ${entry.name}…`);
        run(Download(entry.path), `${entry.name} is on this device`, refreshParent);
      }],
      ["Free up space", () => run(Free(entry.path), `${entry.name} is now cloud only`, refreshParent)],
    ]));
    if (entry.folder) {
      row.addEventListener("click", () => {
        if (expanded.delete(entry.path)) {
          children.hidden = true;
          twisty.textContent = "▸";
          return;
        }
        expanded.add(entry.path);
        children.hidden = false;
        twisty.textContent = "▾";
        renderFolder(entry.path, children);
      });
      if (open) renderFolder(entry.path, children);
    } else {
      row.addEventListener("dblclick", () => Open(entry.path).catch(showError));
    }
    return h("div", {}, row, children);
  }

  async function run(action: Promise<void>, done: string, refresh: () => Promise<void>): Promise<void> {
    try {
      await action;
      showNotice(done);
    } catch (err) {
      showError(err);
    }
    await refresh();
  }

  return { element, show: load };
}
