const minute = 60_000;
const hour = 60 * minute;
const day = 24 * hour;

// Matches api.FormatBytes so the panel and the CLI print the same sizes.
export function formatBytes(n: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = n;
  let unit = 0;
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000;
    unit++;
  }
  return unit === 0 ? `${n} B` : `${Math.round(value)} ${units[unit]}`;
}

export function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

export function ago(time: string, now = Date.now()): string {
  const elapsed = now - Date.parse(time);
  if (elapsed < minute) return "just now";
  if (elapsed < hour) return `${Math.floor(elapsed / minute)} min ago`;
  if (elapsed < 2 * day) return `${Math.floor(elapsed / hour)} h ago`;
  return `${Math.floor(elapsed / day)} days ago`;
}

export function expiresIn(time: string, now = Date.now()): string {
  const left = Date.parse(time) - now;
  if (left < hour) return "within the hour";
  if (left < 2 * day) return `${Math.floor(left / hour)} h`;
  return `${Math.floor(left / day)} days`;
}

export function formatDate(time: string): string {
  return new Date(time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}
