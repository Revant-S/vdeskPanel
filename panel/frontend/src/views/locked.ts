import { Unlock } from "../../wailsjs/go/main/App";
import { errorText, h } from "../dom";

export function createLockedScreen(unlocking: boolean): HTMLElement {
  const password = h("input", { type: "password", placeholder: "Vault password", autocomplete: "current-password", required: true, disabled: unlocking });
  const button = h("button", { type: "submit", disabled: unlocking }, unlocking ? "Unlocking…" : "Unlock");
  const message = h("p", { class: "error", role: "alert" });
  const form = h("form", { class: "locked" }, password, button, message);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    password.disabled = button.disabled = true;
    message.textContent = "";
    try {
      await Unlock(password.value);
    } catch (err) {
      message.textContent = errorText(err);
      password.disabled = button.disabled = false;
      password.focus();
    }
    password.value = "";
  });
  setTimeout(() => password.focus());
  return form;
}

export function createSetupScreen(text: string, command: string): HTMLElement {
  return h("div", { class: "locked" }, h("p", {}, text), h("code", {}, command));
}
