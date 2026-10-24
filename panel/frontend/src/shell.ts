import { Lock } from "../wailsjs/go/main/App";
import { api } from "../wailsjs/go/models";
import { h, showError } from "./dom";
import { formatBytes, plural } from "./format";
import { View } from "./view";

export interface Shell {
  element: HTMLElement;
  update(status: api.Status): void;
  reopen(): void;
}

export function stateLabel(status: api.Status): string {
  switch (status.state) {
    case "locking":
      return "Locking…";
    case "restoring":
      return status.restore ? `Restoring ${status.restore.filesDone} of ${status.restore.filesTotal} files` : "Restoring";
  }
  if (!status.online) return "Offline";
  if (status.syncingItems > 0) return `Syncing ${plural(status.syncingItems, "item")}`;
  return "Synced";
}

// banners are the status lines that ask the owner to do something; the rest stays in the CLI.
export function banners(status: api.Status): string[] {
  const lines: string[] = [];
  if (status.corruptFiles > 0) lines.push(`${plural(status.corruptFiles, "file")} failed an integrity check and ${status.corruptFiles === 1 ? "is" : "are"} not uploaded`);
  if (status.restoreFailed > 0) lines.push(`${plural(status.restoreFailed, "file")} could not be restored from Drive; vault verify --deep names them`);
  if (status.restore?.paused) lines.push("Restoring is paused; vault restore resume continues it");
  if (!status.online && status.notBackedUpFiles > 0) {
    lines.push(`Offline: ${plural(status.notBackedUpFiles, "file")} (${formatBytes(status.notBackedUpBytes)}) not backed up yet`);
  }
  return lines;
}

export function createShell(views: Record<string, View>, settings: View): Shell {
  const label = h("span", { class: "state" });
  const speed = h("span", { class: "speed" });
  const lockButton = h("button", { onclick: () => Lock().catch(showError) }, "Lock");
  const settingsButton = h("button", { class: "icon", title: "Settings", "aria-label": "Settings", onclick: () => select("Settings") }, "⚙");
  const rail = h("nav", { class: "rail" },
    ...Object.keys(views).map((name) => h("button", { "data-view": name, onclick: () => select(name) }, name)));
  const content = h("main");
  const notices = h("div", { class: "banners", role: "status" });
  const element = h("div", { class: "shell" },
    h("header", {}, label, speed, h("span", { class: "spacer" }), lockButton, settingsButton), rail, notices, content);
  const all: Record<string, View> = { ...views, Settings: settings };
  let current = Object.keys(views)[0];

  function select(name: string): void {
    current = name;
    for (const button of rail.querySelectorAll<HTMLElement>("button")) {
      button.toggleAttribute("aria-current", button.dataset.view === name);
    }
    settingsButton.toggleAttribute("aria-current", name === "Settings");
    content.replaceChildren(all[name].element);
    all[name].show();
  }

  return {
    element,
    update(status) {
      label.textContent = stateLabel(status);
      label.dataset.state = status.online ? status.state : "offline";
      speed.textContent = status.online && status.bytesPerSecond > 0 ? `${formatBytes(status.bytesPerSecond)}/s` : "";
      lockButton.disabled = status.state === "locking";
      notices.replaceChildren(...banners(status).map((text) => h("div", { class: "banner" }, text)));
    },
    reopen: () => select(current),
  };
}
