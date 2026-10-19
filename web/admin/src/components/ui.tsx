import type { ReactNode } from 'react';
import { useSearchParams } from 'react-router';

import { describe } from '../api/client';
import { useMe, type MarkState, type WriteStatus } from '../api/hooks';

export function Notice({ tone = 'info', title, children }: { tone?: 'info' | 'success' | 'warning' | 'error'; title?: string; children?: ReactNode }) {
  return (
    <div className={`notice ${tone}`} role={tone === 'error' ? 'alert' : 'status'}>
      {title && <b>{title}</b>}
      {children}
    </div>
  );
}

export function ErrorNotice({ error, title = 'Couldn’t load this' }: { error: unknown; title?: string }) {
  return (
    <Notice tone="error" title={title}>
      {describe(error)}
    </Notice>
  );
}

export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="empty" aria-live="polite">
      <span className="spinner" aria-hidden /> <span className="muted">{label}</span>
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

const MARK: Record<MarkState, { label: string; tone: string }> = {
  present: { label: 'Present', tone: 'success' },
  late: { label: 'Late', tone: 'warning' },
  absent: { label: 'Absent', tone: 'danger' },
  pending: { label: 'Not scanned yet', tone: '' },
};

export function MarkChip({ state }: { state: string }) {
  const m = MARK[state as MarkState] ?? { label: state, tone: '' };
  return <span className={`chip ${m.tone}`}>{m.label}</span>;
}

export const WRITE_STATUS: Record<WriteStatus, { label: string; tone: string; help: string }> = {
  pending: { label: 'Waiting', tone: 'accent', help: 'Queued; the worker sends it within a minute.' },
  sent: { label: 'Sent', tone: 'success', help: 'HolliHop has it.' },
  unchanged: { label: 'Already there', tone: 'success', help: 'HolliHop already had this mark.' },
  set_manually: { label: 'Set by hand', tone: 'warning', help: 'Someone had set this day by hand in HolliHop, so theirs stands. Check it there.' },
  dry_run: { label: 'Dry run', tone: '', help: 'Writing to HolliHop is switched off; nothing was sent.' },
  superseded: { label: 'Replaced', tone: '', help: 'A later correction of this mark replaced it.' },
  failed: { label: 'Failed', tone: 'danger', help: 'HolliHop kept refusing it. Retry, or set it in HolliHop.' },
};

export function WriteChip({ status }: { status: WriteStatus }) {
  const w = WRITE_STATUS[status];
  return (
    <span className={`chip ${w.tone}`} title={w.help}>
      {w.label}
    </span>
  );
}

/** The branch filter, kept in the URL (?office=3). */
export function useOfficeParam(): [number | undefined, (id: number | undefined) => void] {
  const [params, setParams] = useSearchParams();
  const raw = params.get('office');
  const id = raw && /^\d+$/.test(raw) ? Number(raw) : undefined;
  const set = (next: number | undefined) =>
    setParams(
      (p) => {
        if (next === undefined) p.delete('office');
        else p.set('office', String(next));
        return p;
      },
      { replace: true },
    );
  return [id, set];
}

/** Picks one of the admin's branches, or all of them. Hidden with only one. */
export function OfficeSelect({ value, onChange }: { value: number | undefined; onChange: (id: number | undefined) => void }) {
  const me = useMe().data;
  if (!me || me.offices.length < 2) return null;
  return (
    <label>
      <span className="visually-hidden">Branch</span>
      <select
        className="select"
        value={value ?? ''}
        onChange={(e) => onChange(e.target.value === '' ? undefined : Number(e.target.value))}>
        <option value="">{me.super ? 'All branches' : 'All my branches'}</option>
        {me.offices.map((o) => (
          <option key={o.id} value={o.id}>
            {o.name}
          </option>
        ))}
      </select>
    </label>
  );
}

export function Stat({ value, label }: { value: number; label: string }) {
  return (
    <div className="stat">
      <b>{value}</b>
      <span>{label}</span>
    </div>
  );
}
