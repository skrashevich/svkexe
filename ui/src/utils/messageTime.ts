// Framework-agnostic timestamp formatting helpers, extracted from
// components/MessageTimestamp.tsx so the Vue port and the React component (and
// the existing test components/MessageTimestamp.test.ts) share one
// implementation. The React file re-exports formatDay/formatRelative from here.

const timeFormatter = new Intl.DateTimeFormat([], {
  hour: "numeric",
  minute: "2-digit",
});
const absoluteFormatter = new Intl.DateTimeFormat([], {
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
  second: "2-digit",
});
const currentYearDayFormatter = new Intl.DateTimeFormat([], {
  weekday: "short",
  month: "short",
  day: "numeric",
});
const otherYearDayFormatter = new Intl.DateTimeFormat([], {
  weekday: "short",
  month: "short",
  day: "numeric",
  year: "numeric",
});

export function formatTime(d: Date): string {
  return timeFormatter.format(d);
}

export function formatAbsolute(d: Date): string {
  return absoluteFormatter.format(d);
}

export function formatDay(d: Date, now: Date): string {
  const formatter =
    d.getFullYear() === now.getFullYear() ? currentYearDayFormatter : otherYearDayFormatter;
  return formatter.format(d);
}

export function formatRelative(deltaMs: number): string {
  // Guard against clock skew / future timestamps.
  if (deltaMs < 0) deltaMs = 0;
  const sec = Math.round(deltaMs / 1000);
  if (sec < 5) return "just now";
  if (sec < 60) return `${sec}s ago`;
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 30) return `${day}d ago`;
  const mo = Math.round(day / 30);
  if (mo < 12) return `${mo}mo ago`;
  const yr = Math.round(mo / 12);
  return `${yr}y ago`;
}
