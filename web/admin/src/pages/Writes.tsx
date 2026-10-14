import { Link, useSearchParams } from 'react-router';

import { describe } from '../api/client';
import { useRetryWrite, useWrites, type WriteStatus } from '../api/hooks';
import { Empty, ErrorNotice, Loading, Notice, OfficeSelect, useOfficeParam, WRITE_STATUS, WriteChip } from '../components/ui';
import { dateTime, shortDate } from '../lib/dates';

const ORDER: WriteStatus[] = ['failed', 'set_manually', 'pending', 'sent', 'unchanged', 'dry_run', 'superseded'];
const NEEDS_A_PERSON: WriteStatus[] = ['failed', 'set_manually'];

// What the app sent (or will send) to HolliHop, and what needs a person.
export function WritesPage() {
  const [params, setParams] = useSearchParams();
  const [officeId, setOfficeId] = useOfficeParam();
  const raw = params.get('status');
  const statuses = raw && ORDER.includes(raw as WriteStatus) ? [raw as WriteStatus] : NEEDS_A_PERSON;
  const writes = useWrites(statuses, officeId);
  const retry = useRetryWrite();

  const show = (s: WriteStatus | undefined) =>
    setParams(
      (p) => {
        if (s === undefined) p.delete('status');
        else p.set('status', s);
        return p;
      },
      { replace: true },
    );

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>HolliHop writes</h1>
          <p className="muted">Attendance the app sends to HolliHop after each lesson.</p>
        </div>
        <div className="toolbar">
          <OfficeSelect value={officeId} onChange={setOfficeId} />
        </div>
      </div>

      {writes.isPending ? (
        <Loading />
      ) : writes.isError ? (
        <ErrorNotice error={writes.error} title="Couldn’t load the writes" />
      ) : (
        <>
          {!writes.data.writing_enabled && (
            <Notice tone="warning" title="Dry run: nothing is sent to HolliHop yet">
              Writing switches on after the supervised test lesson. Until then, mark attendance in HolliHop as usual.
            </Notice>
          )}
          <div className="toolbar" role="group" aria-label="Show writes">
            <button className={`chip plain ${raw ? '' : 'selected'}`} onClick={() => show(undefined)}>
              Needs a person · {(writes.data.counts.failed ?? 0) + (writes.data.counts.set_manually ?? 0)}
            </button>
            {ORDER.map((s) => (
              <button
                key={s}
                className={`chip ${WRITE_STATUS[s].tone} ${raw === s ? 'selected' : ''}`}
                title={WRITE_STATUS[s].help}
                onClick={() => show(s)}>
                {WRITE_STATUS[s].label} · {writes.data.counts[s] ?? 0}
              </button>
            ))}
          </div>

          {retry.isError && (
            <Notice tone="error" title="Couldn’t retry">
              {describe(retry.error)}
            </Notice>
          )}

          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Lesson</th>
                  <th>Student</th>
                  <th>Sends</th>
                  <th>Status</th>
                  <th>Details</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {writes.data.writes.map((w) => (
                  <tr key={w.id}>
                    <td>
                      <Link to={`/sessions/${w.session_id}`}>{w.group_name}</Link>
                      <div className="small faint">{shortDate(w.date)}</div>
                    </td>
                    <td>{w.student_name}</td>
                    <td>{w.absent ? 'Absent' : 'Attended'}</td>
                    <td>
                      <WriteChip status={w.status} />
                    </td>
                    <td className="small muted">
                      {w.error ?? WRITE_STATUS[w.status].help}
                      <div className="faint">
                        {w.done_at ? dateTime(w.done_at) : `queued ${dateTime(w.created_at)}`}
                        {w.attempts > 1 && ` · ${w.attempts} attempts`}
                      </div>
                    </td>
                    <td>
                      {w.status === 'failed' && (
                        <button
                          className="btn secondary small"
                          disabled={retry.isPending && retry.variables === w.id}
                          onClick={() => retry.mutate(w.id)}>
                          Retry
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
                {writes.data.writes.length === 0 && (
                  <tr>
                    <td colSpan={6}>
                      <Empty>{raw ? 'None.' : 'Nothing needs a person. 🎉'}</Empty>
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}
