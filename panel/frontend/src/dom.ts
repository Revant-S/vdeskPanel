type Child = Node | string | null | undefined | false;
type Props = Record<string, string | number | boolean | EventListener | undefined>;

export function h<K extends keyof HTMLElementTagNameMap>(tag: K, props: Props = {}, ...children: Child[]): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (value === undefined || value === false) continue;
    if (typeof value === "function") element.addEventListener(key.slice(2), value);
    else element.setAttribute(key, value === true ? "" : String(value));
  }
  element.append(...children.filter((child): child is Node | string => child !== null && child !== undefined && child !== false));
  return element;
}

export function svg(tag: string, attributes: Record<string, string | number>): SVGElement {
  const element = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [key, value] of Object.entries(attributes)) element.setAttribute(key, String(value));
  return element;
}

// Wails rejects with the Go error's text as a plain string.
export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

let toastTimer = 0;

export function showError(err: unknown): void {
  showToast(errorText(err), true);
}

export function showNotice(text: string): void {
  showToast(text, false);
}

function showToast(text: string, error: boolean): void {
  const toast = document.getElementById("toast") ?? document.body.appendChild(h("div", { id: "toast", role: "status" }));
  toast.textContent = text;
  toast.classList.toggle("notice", !error);
  toast.classList.add("visible");
  clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => toast.classList.remove("visible"), 6000);
}

export function confirmDialog(message: string, confirmLabel: string): Promise<boolean> {
  return new Promise((resolve) => {
    const dialog = h("dialog", { class: "confirm" },
      h("p", {}, message),
      h("form", { method: "dialog" },
        h("button", { value: "cancel" }, "Cancel"),
        h("button", { value: "ok", class: "danger" }, confirmLabel)));
    dialog.addEventListener("close", () => {
      resolve(dialog.returnValue === "ok");
      dialog.remove();
    });
    document.body.append(dialog);
    dialog.showModal();
  });
}

export function contextMenu(event: MouseEvent, items: [string, () => void][]): void {
  event.preventDefault();
  document.querySelector(".context-menu")?.remove();
  const menu = h("ul", { class: "context-menu", role: "menu" },
    ...items.map(([label, run]) => h("li", { role: "menuitem", onclick: () => { menu.remove(); run(); } }, label)));
  menu.style.left = `${event.clientX}px`;
  menu.style.top = `${event.clientY}px`;
  document.body.append(menu);
  const rect = menu.getBoundingClientRect();
  if (rect.right > innerWidth) menu.style.left = `${innerWidth - rect.width - 4}px`;
  if (rect.bottom > innerHeight) menu.style.top = `${innerHeight - rect.height - 4}px`;
  setTimeout(() => document.addEventListener("click", () => menu.remove(), { once: true }));
}
