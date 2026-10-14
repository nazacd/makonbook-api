import { Link, useSearchParams } from 'react-router';

import { useCorrections } from '../api/hooks';
import { Empty, ErrorNotice, Loading, MarkChip, OfficeSelect, useOfficeParam } from '../components/ui';
import { addDays, dateTime, isDate, schoolToday, shortDate } from '../lib/dates';
import { useNow } from '../lib/now';

const STATE_LABEL: Record<string, string> = { pending: 'Not scanned', present: 'Present', late: 'Late', absent: 'Absent' };

// Teachers' corrections: the main way attendance could be faked (a friend
// marked present), so the report puts "made present" first.
export function CorrectionsPage() {
  const [params, setParams] = useSearchParams();
  const [officeId, setOfficeId] = useOfficeParam();
  const today = schoolToday(new Date(useNow()));
  const to = isDate(params.get('to')) ? params.get('to')! : today;
  const from = isDate(params.get('from')) ? params.get('from')! : addDays(to, -29);
  const rawTeacher = params.get('teacher');
  const teacherId = rawTeacher && /^\d+$/.test(rawTeacher) ? Number(rawTeacher) : undefined;
  const report = useCorrections({ from, to, teacherId, officeId });

  const set = (key: string, value: string | undefined) =>
    setParams(
      (p) => {
        if (value === undefined) p.delete(key);
        else p.set(key, value);
        return p;
      },
      { replace: true },
    );

  const teachers = report.data?.teachers ?? [];
  const selected = teachers.find((t) => t.teacher_id === teacherId);

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Teacher corrections</h1>
          <p className="muted">Marks teachers changed by hand, for lessons in the period.</p>
        </div>
        <div className="toolbar">
          <OfficeSelect value={officeId} onChange={setOfficeId} />
          <label className="toolbar">
            <span className="small muted">From</span>
            <input className="input" type="date" value={from} max={to} onChange={(e) => isDate(e.target.value) && set('from', e.target.value)} />
          </label>
          <label className="toolbar">
            <span className="small muted">to</span>
            <input className="input" type="date" value={to} min={from} onChange={(e) => isDate(e.target.value) && set('to', e.target.value)} />
          </label>
        </div>
      </div>

      {report.isPending ? (
        <Loading />
      ) : report.isError ? (
        <ErrorNotice error={report.error} title="Couldn’t load the report" />
      ) : (
        <>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Teacher</th>
                  <th className="num" title="Students who were absent or hadn’t scanned, marked present or late by the teacher">
                    Made present
                  </th>
                  <th className="num">All corrections</th>
                  <th className="num">Lessons corrected</th>
                  <th className="num">Lessons taken</th>
                </tr>
              </thead>
              <tbody>
                {teachers.map((t) => (
                  <tr
                    key={t.teacher_id}
                    className="clickable"
                    tabIndex={0}
                    aria-selected={t.teacher_id === teacherId}
                    onClick={() => set('teacher', t.teacher_id === teacherId ? undefined : String(t.teacher_id))}
                    onKeyDown={(e) => e.key === 'Enter' && set('teacher', String(t.teacher_id))}
                    style={t.teacher_id === teacherId ? { background: 'var(--accent-soft)' } : undefined}>
                    <td>{t.teacher_name}</td>
                    <td className="num">
                      {t.made_present > 0 ? <span className="chip warning plain">{t.made_present}</span> : 0}
                    </td>
                    <td className="num">{t.corrections}</td>
                    <td className="num">{t.lessons_corrected}</td>
                    <td className="num">{t.lessons_opened}</td>
                  </tr>
                ))}
                {teachers.length === 0 && (
                  <tr>
                    <td colSpan={5}>
                      <Empty>No teacher changed a mark in this period.</Empty>
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>

          <section className="stack" style={{ gap: 10 }}>
            <div className="card-head" style={{ margin: 0 }}>
              <h2>{selected ? `Changes by ${selected.teacher_name}` : 'All changes, newest first'}</h2>
              {selected && (
                <button className="btn link" onClick={() => set('teacher', undefined)}>
                  Show everyone’s
                </button>
              )}
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>When</th>
                    <th>Lesson</th>
                    <th>Student</th>
                    <th>Change</th>
                    <th>By</th>
                  </tr>
                </thead>
                <tbody>
                  {report.data.changes.map((c) => (
                    <tr key={c.id}>
                      <td className="small muted nowrap">{dateTime(c.at)}</td>
                      <td>
                        <Link to={`/sessions/${c.session_id}`}>{c.group_name}</Link>
                        <div className="small faint">{shortDate(c.date)}</div>
                      </td>
                      <td>{c.student_name}</td>
                      <td className="nowrap">
                        <span className="small muted">{STATE_LABEL[c.from] ?? c.from} →</span> <MarkChip state={c.to} />
                      </td>
                      <td>
                        {c.actor_name}
                        <div className="small faint">{c.actor_kind === 'admin' ? 'Office' : 'Teacher'}</div>
                      </td>
                    </tr>
                  ))}
                  {report.data.changes.length === 0 && (
                    <tr>
                      <td colSpan={5}>
                        <Empty>No changes.</Empty>
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
            {report.data.changes.length === 500 && <p className="small faint">Showing the newest 500. Narrow the dates to see older ones.</p>}
          </section>
        </>
      )}
    </div>
  );
}
