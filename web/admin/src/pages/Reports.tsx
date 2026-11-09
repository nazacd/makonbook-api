import { Flag } from 'lucide-react';
import { useState } from 'react';
import { Link } from 'react-router';

import { useMe, usePracticeReports, useSetReportStatus, type PracticeReport } from '../api/hooks';
import { Empty, ErrorNotice, Loading, PageIcon } from '../components/ui';
import { dateTime } from '../lib/dates';
import { SUBJECT } from '../practice/common';

// Students' issue reports on questions (spec 9.6), from the test screen's
// "Report an issue": the open ones to look at, the resolved ones kept.
// Editors open the question in the editor to fix it.

const REASON: Record<string, string> = {
  answer_key_wrong: 'Answer key looks wrong',
  typo: 'Typo or wording',
  unclear: 'Unclear or ambiguous',
  image_broken: 'Image or graph broken',
  formatting: 'Math or formatting',
  other: 'Something else',
};

type Status = 'open' | 'resolved';

export function ReportsPage() {
  const [status, setStatus] = useState<Status>('open');
  const open = usePracticeReports('open');
  const resolved = usePracticeReports('resolved');
  const list = status === 'open' ? open : resolved;
  const editor = useMe().data?.editor ?? false;
  const set = useSetReportStatus();

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>
            <PageIcon icon={Flag} />
            Issue reports
          </h1>
          <p className="muted">What students flagged on questions while taking a test. Fix the question, then resolve the report.</p>
        </div>
        <div className="seg" role="tablist" aria-label="Which reports">
          {(['open', 'resolved'] as const).map((s) => {
            const n = (s === 'open' ? open : resolved).data?.length;
            return (
              <button key={s} role="tab" aria-selected={status === s} className={status === s ? 'on' : ''} onClick={() => setStatus(s)}>
                {s === 'open' ? 'Open' : 'Resolved'}
                {n != null && (
                  <span className="count">
                    {n}
                    {n === 200 ? '+' : ''}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      </div>

      {set.isError && <ErrorNotice error={set.error} title="Couldn’t change the report" />}

      {list.isPending ? (
        <Loading />
      ) : list.isError ? (
        <ErrorNotice error={list.error} title="Couldn’t load the reports" />
      ) : list.data.length === 0 ? (
        <div className="table-wrap">
          <Empty>{status === 'open' ? 'Nothing open. Reports students send from the test screen show up here.' : 'Nothing resolved yet.'}</Empty>
        </div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Sent</th>
                <th>Question</th>
                <th>Problem</th>
                <th>Student</th>
                <th>{status === 'open' ? '' : 'Resolved'}</th>
              </tr>
            </thead>
            <tbody>
              {list.data.map((r) => (
                <Row key={r.id} r={r} editor={editor} busy={set.isPending && set.variables?.id === r.id} onStatus={(s) => set.mutate({ id: r.id, status: s })} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      {list.data?.length === 200 && <p className="small faint">The newest 200 are shown.</p>}
    </div>
  );
}

function Row({ r, editor, busy, onStatus }: { r: PracticeReport; editor: boolean; busy: boolean; onStatus: (s: Status) => void }) {
  const place = `${r.test} · ${SUBJECT[r.subject] ?? r.subject} M${r.module} #${r.position}`;
  return (
    <tr>
      <td className="small muted nowrap">{dateTime(r.created_at)}</td>
      <td>
        <div>{editor ? <Link to={`/practice/questions/${r.question_id}?test=${encodeURIComponent(r.test)}`}>{place}</Link> : place}</div>
        <div className="small faint">
          {r.test_title || 'Test removed'} · version {r.version}
        </div>
      </td>
      <td>
        <div className="toolbar" style={{ marginBottom: r.text ? 6 : 0 }}>
          <span className={`chip ${r.kind === 'technical' ? 'plain' : r.reason === 'answer_key_wrong' ? 'danger' : 'warning'}`}>{r.kind === 'technical' ? 'Technical' : r.reason ? REASON[r.reason] : 'Question'}</span>
        </div>
        {r.text && <div className="small report-text">“{r.text}”</div>}
      </td>
      <td className="nowrap">
        {r.student.last_name} {r.student.first_name}
        <div className="small faint">id {r.student.id}</div>
      </td>
      <td className="nowrap">
        {r.status === 'open' ? (
          <button className="btn small" onClick={() => onStatus('resolved')} disabled={busy}>
            Resolve
          </button>
        ) : (
          <div className="toolbar">
            <span className="small muted">
              {r.resolved_by}
              {r.resolved_at && <div className="faint">{dateTime(r.resolved_at)}</div>}
            </span>
            <button className="btn secondary small" onClick={() => onStatus('open')} disabled={busy}>
              Reopen
            </button>
          </div>
        )}
      </td>
    </tr>
  );
}

