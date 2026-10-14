import { Link, useParams } from 'react-router';

import { ApiError, describe } from '../api/client';
import { useSession, useSetMark, type Mark, type Session } from '../api/hooks';
import { ErrorNotice, Loading, MarkChip, Notice, Stat, WriteChip } from '../components/ui';
import { clock, dateTime, longDate } from '../lib/dates';

const STATES = [
  { state: 'present', label: 'Present' },
  { state: 'late', label: 'Late' },
  { state: 'absent', label: 'Absent' },
] as const;

const STATE_LABEL: Record<string, string> = { pending: 'not scanned', present: 'present', late: 'late', absent: 'absent' };

// One lesson's attendance: every mark, who set it, and corrections.
export function SessionPage() {
  const { id = '' } = useParams();
  const session = useSession(id);
  const setMark = useSetMark(id);

  if (session.isPending) return <Loading />;
  if (session.isError) {
    const notFound = session.error instanceof ApiError && session.error.status === 404;
    return (
      <div className="stack">
        <BackLink />
        {notFound ? (
          <Notice tone="warning" title="No such lesson">
            It doesn’t exist, or it’s in a branch you don’t have access to.
          </Notice>
        ) : (
          <ErrorNotice error={session.error} />
        )}
      </div>
    );
  }
  const s = session.data;
  const busy = setMark.isPending ? setMark.variables.clientId : undefined;

  return (
    <div className="stack">
      <BackLink date={s.date} />
      <div className="page-head">
        <div>
          <h1>{s.group_name}</h1>
          <p className="muted">
            {longDate(s.date)} · {clock(s.starts_at)}–{clock(s.ends_at)}
            {s.office && ` · ${s.office}`}
          </p>
        </div>
        <span className={`chip ${s.status === 'open' ? 'accent' : 'success'}`}>
          {s.status === 'open' ? 'In progress' : 'Finished'}
        </span>
      </div>

      <div className="stats">
        <Stat value={s.counts.present} label="Present" />
        <Stat value={s.counts.late} label="Late" />
        <Stat value={s.counts.absent} label="Absent" />
        {s.status === 'open' && <Stat value={s.counts.pending} label="Not scanned yet" />}
      </div>

      <p className="small muted" style={{ margin: 0 }}>
        Started by {s.opened_by} at {clock(s.opened_at)}
        {s.finished_at &&
          ` · finished ${s.finished_by === 'auto' ? 'automatically' : 'by the teacher'} at ${clock(s.finished_at)}`}
        . Late means scanned after {clock(s.late_after)}.
      </p>

      {setMark.isError && (
        <Notice tone="error" title="Couldn’t change the mark">
          {describe(setMark.error)}
        </Notice>
      )}

      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Student</th>
              <th>Mark</th>
              <th>How</th>
              <th>HolliHop</th>
            </tr>
          </thead>
          <tbody>
            {s.marks.map((m) => (
              <tr key={m.client_id}>
                <td>
                  {m.first_name} {m.last_name}
                </td>
                <td>
                  <div className="seg" role="group" aria-label={`Mark for ${m.first_name} ${m.last_name}`}>
                    {STATES.map(({ state, label }) => (
                      <button
                        key={state}
                        type="button"
                        className={m.state === state ? `on ${state}` : undefined}
                        aria-pressed={m.state === state}
                        disabled={busy !== undefined}
                        onClick={() => m.state !== state && setMark.mutate({ clientId: m.client_id, state })}>
                        {label}
                      </button>
                    ))}
                  </div>
                  {busy === m.client_id && <span className="spinner" style={{ marginLeft: 8 }} aria-label="Saving" />}
                </td>
                <td className="small muted">{how(m)}</td>
                <td>{m.hollihop ? <WriteChip status={m.hollihop.status} /> : <span className="faint small">—</span>}</td>
              </tr>
            ))}
            {s.marks.length === 0 && (
              <tr>
                <td colSpan={4} className="empty">
                  No students on this lesson’s list.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <p className="small faint" style={{ margin: 0 }}>
        Your corrections are saved with your name and sent to HolliHop; the teacher can’t change them back. Billing isn’t
        touched here: an absence stays billed unless you unbill it in HolliHop.
      </p>

      <History session={s} />
    </div>
  );
}

function how(m: Mark): string {
  switch (m.source) {
    case 'scan':
      return m.scanned_at ? `Scanned at ${clock(m.scanned_at)}` : 'Scanned';
    case 'teacher':
      return 'Marked by the teacher';
    case 'admin':
      return 'Corrected by the office';
    case 'auto':
      return 'Didn’t scan';
    default:
      return 'Waiting for a scan';
  }
}

function History({ session }: { session: Session }) {
  const changes = session.history;
  return (
    <section className="card">
      <div className="card-head">
        <h2>History</h2>
        <span className="small faint">{changes.length} changes</span>
      </div>
      {changes.length === 0 ? (
        <p className="muted" style={{ margin: 0 }}>
          Nothing yet.
        </p>
      ) : (
        <ol className="timeline">
          {changes.map((c, i) => (
            <li key={i}>
              <span className="tabular faint" title={dateTime(c.at)}>
                {clock(c.at)}
              </span>
              <span>
                <b>{c.student_name}</b>{' '}
                {c.actor_kind === 'student' ? (
                  <>
                    scanned — <MarkChip state={c.to} />
                  </>
                ) : c.actor_kind === 'system' ? (
                  <>
                    didn’t scan — <MarkChip state={c.to} /> when attendance finished
                  </>
                ) : (
                  <>
                    {STATE_LABEL[c.from] ?? c.from} → <MarkChip state={c.to} />{' '}
                    <span className="muted">
                      by {c.actor_name || 'unknown'} ({c.actor_kind === 'admin' ? 'office' : 'teacher'})
                    </span>
                  </>
                )}
              </span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function BackLink({ date }: { date?: string }) {
  return (
    <div>
      <Link to={date ? `/lessons?date=${date}` : '/lessons'}>‹ Lessons</Link>
    </div>
  );
}
