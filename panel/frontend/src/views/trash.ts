import { EmptyTrash, PurgeTrash, RestoreTrash, Trash } from "../../wailsjs/go/main/App";
import { api } from "../../wailsjs/go/models";
import { confirmDialog, h, showError, showNotice } from "../dom";
import { expiresIn, formatBytes, formatDate, plural } from "../format";
import { View } from "../view";

function cell(text: string, detail: string): HTMLElement {
  return h("td", { title: `${text}\n${detail}` }, h("div", {}, text), h("div", { class: "detail" }, detail));
}

export function createTrashView(): View {
  const summary = h("span", { class: "muted" });
  const emptyButton = h("button", { class: "danger", onclick: () => emptyTrash() }, "Empty trash");
  const body = h("tbody");
  const table = h("table", { class: "trash" },
    h("thead", {}, h("tr", {}, ...["Item", "Reason", "Deleted on", ""].map((title) => h("th", {}, title)))),
    body);
  const element = h("section", {}, h("div", { class: "toolbar" }, summary, h("span", { class: "spacer" }), emptyButton), table);
  let items: api.TrashItem[] = [];

  async function load(): Promise<void> {
    try {
      items = (await Trash()) ?? [];
    } catch (err) {
      showError(err);
      return;
    }
    items.sort((a, b) => Date.parse(b.deletedAt) - Date.parse(a.deletedAt));
    render();
  }

  function render(): void {
    const total = items.reduce((sum, item) => sum + item.size, 0);
    summary.textContent = items.length === 0 ? "Trash is empty" : `${plural(items.length, "item")} · ${formatBytes(total)}`;
    emptyButton.disabled = items.length === 0;
    table.hidden = items.length === 0;
    body.replaceChildren(...items.map(row));
  }

  function row(item: api.TrashItem): HTMLElement {
    const contents = item.fileCount === 0 ? "empty" : plural(item.fileCount, "file");
    const name = item.folder ? `${item.name}/ (${contents})` : item.name;
    const reason = item.reason.replace("_", " ");
    const device = item.deviceName || item.device.slice(0, 8);
    return h("tr", {},
      cell(name, item.originalPath),
      cell(reason, `on ${device}`),
      cell(formatDate(item.deletedAt), `expires in ${expiresIn(item.expiresAt)}`),
      h("td", { class: "actions" },
        h("button", { onclick: () => restore(item) }, "Restore"),
        h("button", { class: "danger", onclick: () => purge(item) }, "Delete now")));
  }

  async function restore(item: api.TrashItem): Promise<void> {
    try {
      const restored = await RestoreTrash(item.id);
      showNotice(`Restored to ${restored}`);
    } catch (err) {
      showError(err);
    }
    await load();
  }

  async function purge(item: api.TrashItem): Promise<void> {
    if (!(await confirmDialog(`Delete “${item.name}” (${formatBytes(item.size)}) permanently? This cannot be undone.`, "Delete now"))) return;
    await PurgeTrash(item.id).catch(showError);
    await load();
  }

  async function emptyTrash(): Promise<void> {
    const total = items.reduce((sum, item) => sum + item.size, 0);
    const question = `Delete all ${plural(items.length, "item")} (${formatBytes(total)}) in the trash permanently? This cannot be undone.`;
    if (!(await confirmDialog(question, "Empty trash"))) return;
    try {
      const result = await EmptyTrash();
      showNotice(`Deleted ${plural(result.items, "item")} · ${formatBytes(result.bytes)}`);
    } catch (err) {
      showError(err);
    }
    await load();
  }

  return { element, show: load };
}
