// Dates and times are shown in the school's time zone, wherever the admin is.
export const SCHOOL_TZ = 'Asia/Tashkent';

const dateFmt = new Intl.DateTimeFormat('en-GB', { timeZone: SCHOOL_TZ, weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' });
const shortDateFmt = new Intl.DateTimeFormat('en-GB', { timeZone: 'UTC', day: 'numeric', month: 'short' });
const longDayFmt = new Intl.DateTimeFormat('en-GB', { timeZone: 'UTC', weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
const timeFmt = new Intl.DateTimeFormat('en-GB', { timeZone: SCHOOL_TZ, hour: '2-digit', minute: '2-digit', hour12: false });
const dayFmt = new Intl.DateTimeFormat('en-CA', { timeZone: SCHOOL_TZ, year: 'numeric', month: '2-digit', day: '2-digit' });

/** Today at the school, YYYY-MM-DD. */
export function schoolToday(now: Date = new Date()): string {
  return dayFmt.format(now);
}

/** YYYY-MM-DD plus n days. */
export function addDays(date: string, n: number): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}

/** "Monday, 12 October 2026" for a calendar date. */
export function longDate(date: string): string {
  return longDayFmt.format(new Date(`${date}T00:00:00Z`));
}

/** "12 Oct" for a calendar date. */
export function shortDate(date: string): string {
  return shortDateFmt.format(new Date(`${date}T00:00:00Z`));
}

/** "14:05", school time. */
export function clock(iso: string): string {
  return timeFmt.format(new Date(iso));
}

/** "Mon, 12 Oct 2026, 14:05", school time. */
export function dateTime(iso: string): string {
  const d = new Date(iso);
  return `${dateFmt.format(d)}, ${timeFmt.format(d)}`;
}

export function isDate(s: string | null): s is string {
  return !!s && /^\d{4}-\d{2}-\d{2}$/.test(s) && !Number.isNaN(Date.parse(`${s}T00:00:00Z`));
}
