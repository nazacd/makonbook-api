import { Link, useNavigate, useSearchParams } from 'react-router';

import { useLessons, type Lesson } from '../api/hooks';
import { Empty, ErrorNotice, Loading, OfficeSelect, Stat, useOfficeParam } from '../components/ui';
import { addDays, isDate, longDate, schoolToday } from '../lib/dates';
import { useNow } from '../lib/now';

type Phase = 'taken' | 'open' | 'missed' | 'upcoming' | 'off' | 'empty';

function phaseOf(l: Lesson, now: number): Phase {
  if (!l.attendance_enabled) return 'off';
  if (l.session) return l.session.status === 'open' ? 'open' : 'taken';
  // Only newly opened groups come with nobody on the list; nothing to take.
  if (l.students === 0) return 'empty';
  return now > Date.parse(l.ends_at) ? 'missed' : 'upcoming';
}

const PHASE: Record<Phase, { label: string; tone: string }> = {
  taken: { label: 'Taken', tone: 'success' },
  open: { label: 'In progress', tone: 'accent' },
  missed: { label: 'Not taken', tone: 'warning' },
  upcoming: { label: 'Upcoming', tone: '' },
  off: { label: 'Not in the app', tone: 'plain' },
  empty: { label: 'No students', tone: 'plain' },
};

// Every lesson of a day in the admin's branches, with how attendance went.
export function LessonsPage() {
  const [params, setParams] = useSearchParams();
  const [officeId, setOfficeId] = useOfficeParam();
  const now = useNow();
  const today = schoolToday(new Date(now));
  const raw = params.get('date');
  const date = isDate(raw) ? raw : today;
  const lessons = useLessons(date, officeId);
  const navigate = useNavigate();

  const setDate = (d: string) =>
    setParams(
      (p) => {
        if (d === today) p.delete('date');
        else p.set('date', d);
        return p;
      },
      { replace: true },
    );

  const list = lessons.data?.lessons ?? [];
  const phases = list.map((l) => phaseOf(l, now));
  const count = (p: Phase) => phases.filter((x) => x === p).length;
  const totals = list.reduce(
    (t, l) => ({
      present: t.present + (l.counts?.present ?? 0),
      late: t.late + (l.counts?.late ?? 0),
      absent: t.absent + (l.counts?.absent ?? 0),
    }),
    { present: 0, late: 0, absent: 0 },
  );

  // Group by branch, in the order the API sends (branch, then time).
  const groups: { office: string; rows: { lesson: Lesson; phase: Phase }[] }[] = [];
  list.forEach((lesson, i) => {
    const office = lesson.office || 'No branch';
    let g = groups[groups.length - 1];
    if (!g || g.office !== office) {
      g = { office, rows: [] };
      groups.push(g);
    }
    g.rows.push({ lesson, phase: phases[i]! });
  });

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>{date === today ? 'Today' : longDate(date)}</h1>
          <p className="muted">{date === today ? longDate(date) : 'Lessons and their attendance'}</p>
        </div>
        <div className="toolbar">
          <OfficeSelect value={officeId} onChange={setOfficeId} />
          <button className="btn secondary" onClick={() => setDate(addDays(date, -1))} aria-label="Previous day">
            ‹
          </button>
          <label>
            <span className="visually-hidden">Date</span>
            <input className="input" type="date" value={date} onChange={(e) => isDate(e.target.value) && setDate(e.target.value)} />
          </label>
          <button className="btn secondary" onClick={() => setDate(addDays(date, 1))} aria-label="Next day">
            ›
          </button>
          {date !== today && (
            <button className="btn secondary" onClick={() => setDate(today)}>
              Today
            </button>
          )}
        </div>
      </div>

      {lessons.isPending ? (
        <Loading />
      ) : lessons.isError ? (
        <ErrorNotice error={lessons.error} title="Couldn’t load the lessons" />
      ) : list.length === 0 ? (
        <div className="table-wrap">
          <Empty>No lessons on this day in HolliHop.</Empty>
        </div>
      ) : (
        <>
          <div className="stats">
            <Stat value={list.length} label="Lessons" />
            <Stat value={count('taken') + count('open')} label="Attendance taken" />
            <Stat value={count('missed')} label="Not taken" />
            <Stat value={totals.present + totals.late} label={`Attended${totals.late ? ` (${totals.late} late)` : ''}`} />
            <Stat value={totals.absent} label="Absent" />
          </div>

          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Group</th>
                  <th>Teacher</th>
                  <th>Room</th>
                  <th className="num">Students</th>
                  <th>Attendance</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => [
                  groups.length > 1 && (
                    <tr key={`office-${g.office}`} className="group-row">
                      <td colSpan={6}>{g.office}</td>
                    </tr>
                  ),
                  ...g.rows.map(({ lesson: l, phase }) => {
                    const open = l.session ? () => navigate(`/sessions/${l.session!.id}`) : undefined;
                    return (
                      <tr
                        key={l.group_id}
                        className={open ? 'clickable' : undefined}
                        onClick={open}
                        onKeyDown={(e) => open && (e.key === 'Enter' || e.key === ' ') && open()}
                        tabIndex={open ? 0 : undefined}
                        aria-label={open ? `${l.group_name}: open attendance` : undefined}>
                        <td className="tabular nowrap">
                          {l.begin_time}–{l.end_time}
                        </td>
                        <td>
                          <div>{l.group_name}</div>
                          <div className="small faint">{l.discipline}</div>
                        </td>
                        <td>{l.teachers.join(', ') || <span className="faint">—</span>}</td>
                        <td>{l.classroom || <span className="faint">—</span>}</td>
                        <td className="num">{l.students}</td>
                        <td>
                          <div className="toolbar">
                            <span className={`chip ${PHASE[phase].tone}`}>{PHASE[phase].label}</span>
                            {l.counts && (
                              <span className="small muted tabular">
                                {l.counts.present + l.counts.late}/{l.students} attended
                                {l.counts.late > 0 && ` · ${l.counts.late} late`}
                                {l.counts.absent > 0 && ` · ${l.counts.absent} absent`}
                              </span>
                            )}
                          </div>
                        </td>
                      </tr>
                    );
                  }),
                ])}
              </tbody>
            </table>
          </div>
          {count('missed') > 0 && (
            <p className="small faint" style={{ margin: 0 }}>
              “Not taken”: the teacher didn’t start attendance in the app. Mark that lesson in HolliHop.
            </p>
          )}
          <p className="small faint" style={{ margin: 0 }}>
            Groups with no teacher, or finished groups still on HolliHop’s schedule, aren’t listed here: see{' '}
            <Link to="/issues">HolliHop issues</Link>.
          </p>
        </>
      )}
    </div>
  );
}
