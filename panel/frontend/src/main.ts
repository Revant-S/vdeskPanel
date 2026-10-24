import "./style.css";
import { Status } from "../wailsjs/go/main/App";
import { api } from "../wailsjs/go/models";
import { EventsOn } from "../wailsjs/runtime/runtime";
import { errorText, h } from "./dom";
import { createShell } from "./shell";
import { View } from "./view";
import { createLockedScreen, createSetupScreen } from "./views/locked";
import { createTrashView } from "./views/trash";

function placeholder(name: string): View {
  return { element: h("p", {}, name), show() {} };
}

const root = document.getElementById("app")!;
const shell = createShell(
  { Tree: placeholder("Tree"), History: placeholder("History"), Trash: createTrashView() },
  placeholder("Settings"),
);
let screen = "";

function screenFor(status: api.Status): string {
  switch (status.state) {
    case "needs-signin":
    case "needs-setup":
    case "locked":
    case "unlocking":
      return status.state;
  }
  return "shell";
}

function buildScreen(kind: string, status: api.Status): HTMLElement {
  switch (kind) {
    case "needs-signin":
      return createSetupScreen("This device is not signed in to Google. Sign in from a terminal:", "vault signin");
    case "needs-setup":
      return status.vaultExists
        ? createSetupScreen("This Google account has a vault. Join it from a terminal:", "vault join")
        : createSetupScreen("This Google account has no vault yet. Create one from a terminal:", "vault create");
    case "locked":
    case "unlocking":
      return createLockedScreen(kind === "unlocking");
  }
  shell.reopen();
  return shell.element;
}

function render(status: api.Status): void {
  const kind = screenFor(status);
  if (kind !== screen) {
    screen = kind;
    root.replaceChildren(buildScreen(kind, status));
  }
  if (kind === "shell") shell.update(status);
}

async function start(): Promise<void> {
  try {
    render(await Status());
  } catch (err) {
    screen = "";
    root.replaceChildren(h("div", { class: "locked" }, h("p", {}, "Cannot reach the vault daemon."), h("p", { class: "error" }, errorText(err))));
    setTimeout(start, 3000);
  }
}

EventsOn("status", render);
start();
