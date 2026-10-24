import { Settings, SignOut, UpdateSettings } from "../../wailsjs/go/main/App";
import { api } from "../../wailsjs/go/models";
import { confirmDialog, h, showError, showNotice } from "../dom";
import { View } from "../view";

type NumberKey = "uploads" | "downloads" | "activePollSeconds" | "idlePollSeconds" | "trashRetentionDays";

const numberFields: [NumberKey, string][] = [
  ["uploads", "Parallel uploads"],
  ["downloads", "Parallel downloads"],
  ["activePollSeconds", "Poll Drive while active (seconds)"],
  ["idlePollSeconds", "Poll Drive while idle (seconds)"],
  ["trashRetentionDays", "Keep trash for (days)"],
];

export function createSettingsView(): View {
  const mountPoint = h("input", { required: true });
  const deviceName = h("input", { required: true });
  const numbers = Object.fromEntries(numberFields.map(([key]) => [key, h("input", { type: "number", min: 1, required: true })])) as Record<NumberKey, HTMLInputElement>;
  const denyList = h("textarea", { rows: 4, spellcheck: "false" });
  const save = h("button", { type: "submit" }, "Save");
  const form = h("form", { class: "settings" },
    h("label", {}, "Mount point"), mountPoint,
    h("label", {}, "Device name"), deviceName,
    ...numberFields.flatMap(([key, label]) => [h("label", {}, label), numbers[key]]),
    h("label", {}, "Thumbnailers kept out (one per line)"), denyList,
    h("p", { class: "wide muted" }, "Most changes apply at the next unlock."),
    h("div", { class: "wide" }, save));
  const signOut = h("button", { class: "danger", onclick: () => signOutOfGoogle() }, "Sign out of Google");
  const element = h("section", {}, form, h("hr"), signOut);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const settings = api.Settings.createFrom({
      mountPoint: mountPoint.value.trim(),
      deviceName: deviceName.value.trim(),
      ...Object.fromEntries(numberFields.map(([key]) => [key, numbers[key].valueAsNumber])),
      thumbnailerDenyList: denyList.value.split("\n").map((line) => line.trim()).filter(Boolean),
    });
    save.disabled = true;
    try {
      await UpdateSettings(settings);
      showNotice("Settings saved");
    } catch (err) {
      showError(err);
    }
    save.disabled = false;
  });

  async function load(): Promise<void> {
    let settings: api.Settings;
    try {
      settings = await Settings();
    } catch (err) {
      showError(err);
      return;
    }
    mountPoint.value = settings.mountPoint;
    deviceName.value = settings.deviceName;
    for (const [key] of numberFields) numbers[key].valueAsNumber = settings[key];
    denyList.value = (settings.thumbnailerDenyList ?? []).join("\n");
  }

  async function signOutOfGoogle(): Promise<void> {
    if (!(await confirmDialog("Sign out of Google? The vault locks and needs sign-in again.", "Sign out"))) return;
    await SignOut().catch(showError);
  }

  return { element, show: load };
}
