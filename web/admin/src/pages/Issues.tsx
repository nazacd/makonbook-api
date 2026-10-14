import { Link } from 'react-router';

import type { Schemas } from '../api/client';
import { useDataQuality } from '../api/hooks';
import { Empty, ErrorNotice, Loading, OfficeSelect, useOfficeParam } from '../components/ui';
import { shortDate } from '../lib/dates';

type GroupIssue = Schemas['GroupIssue'];

const PHONE: Record<string, string> = {
  phone_missing: 'No phone number',
  phone_invalid: 'Phone number isn’t valid',
  phone_duplicate: 'Phone number shared',
};

function problem(g: GroupIssue): { label: string; detail: string; fix: string } {
  if (g.kind === 'no_teacher') {
    return g.fired_teacher
      ? {
          label: 'Teacher fired',
          detail: 'Lessons are still scheduled, but the only teacher on the schedule is fired.',
          fix: 'Put the new teacher on the schedule, or close the group.',
        }
      : {
          label: 'No teacher',
          detail: 'Lessons are scheduled, but no teacher is on the schedule.',
          fix: 'Add the teacher to the schedule, or close the group.',
        };
  }
  return {
    label: 'No students',
    detail: g.last_attended
      ? `Nobody is on the list; the last lesson anyone attended was ${shortDate(g.last_attended)}.`
      : 'Nobody is on the list, and the group isn’t new.',
    fix: 'If it has finished, end its schedule in HolliHop. If not, add its students.',
  };
}

// What admins should fix in HolliHop: groups, then people's phone numbers.
// MakonBook only reads HolliHop; each fix shows here after the next sync
// (within 15 minutes).
export function IssuesPage() {
  const [officeId, setOfficeId] = useOfficeParam();
  const data = useDataQuality(officeId);
  const groups = data.data?.groups ?? [];
  const people = data.data?.issues ?? [];

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>HolliHop issues</h1>
          <p className="muted">Fix these in HolliHop; they clear here after the next sync, within 15 minutes.</p>
        </div>
        <div className="toolbar">
          <OfficeSelect value={officeId} onChange={setOfficeId} />
        </div>
      </div>

      {data.isPending ? (
        <Loading />
      ) : data.isError ? (
        <ErrorNotice error={data.error} title="Couldn’t load the issues" />
      ) : (
        <>
          <section className="stack" style={{ gap: 10 }}>
            <div>
              <h2>Groups to fix · {groups.length}</h2>
              <p className="small muted" style={{ margin: '4px 0 0' }}>
                Groups with lessons still scheduled that nobody can take attendance for. They’re left out of{' '}
                <Link to="/lessons">Lessons</Link>. Most are finished groups whose schedule was never closed.
              </p>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Group</th>
                    <th>Problem</th>
                    <th>Teacher</th>
                    <th>What to do</th>
                    <th>Next lesson</th>
                  </tr>
                </thead>
                <tbody>
                  {groups.map((g) => {
                    const p = problem(g);
                    return (
                      <tr key={g.group_id}>
                        <td>
                          {g.group_name}
                          <div className="small faint">
                            {[g.office, g.individual ? 'Individual' : 'Group', g.schedule_since && `since ${shortDate(g.schedule_since)}`]
                              .filter(Boolean)
                              .join(' · ')}
                          </div>
                        </td>
                        <td>
                          <span className="chip warning">{p.label}</span>
                        </td>
                        <td>{g.teachers.join(', ') || <span className="faint">—</span>}</td>
                        <td className="small">
                          <div className="muted">{p.detail}</div>
                          <div>{p.fix}</div>
                        </td>
                        <td className="small muted nowrap">{shortDate(g.next_lesson)}</td>
                      </tr>
                    );
                  })}
                  {groups.length === 0 && (
                    <tr>
                      <td colSpan={5}>
                        <Empty>Every group with lessons has a teacher and students.</Empty>
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </section>

          <section className="stack" style={{ gap: 10 }}>
            <div>
              <h2>People who can’t sign in · {people.length}</h2>
              <p className="small muted" style={{ margin: '4px 0 0' }}>
                Students and teachers whose phone number in HolliHop is missing, invalid, or shared with someone else.
              </p>
            </div>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Person</th>
                    <th>Problem</th>
                    <th>Phone in HolliHop</th>
                    <th>Details</th>
                    <th>Since</th>
                  </tr>
                </thead>
                <tbody>
                  {people.map((i) => (
                    <tr key={i.id}>
                      <td>
                        {i.last_name} {i.first_name}
                        <div className="small faint">
                          {i.person_kind === 'student' ? 'Student' : 'Teacher'} · HolliHop id {i.person_id}
                        </div>
                      </td>
                      <td>
                        <span className="chip warning">{PHONE[i.kind] ?? i.kind}</span>
                      </td>
                      <td className="tabular">{i.phone || <span className="faint">—</span>}</td>
                      <td className="small muted">{i.detail}</td>
                      <td className="small muted nowrap">{shortDate(i.since.slice(0, 10))}</td>
                    </tr>
                  ))}
                  {people.length === 0 && (
                    <tr>
                      <td colSpan={5}>
                        <Empty>Everyone’s phone number is fine.</Empty>
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </div>
  );
}
