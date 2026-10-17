import { Link, useNavigate, useSearchParams } from 'react-router';

import { usePracticeQueue, type QueueFilter } from '../api/hooks';
import { Empty, ErrorNotice, Loading } from '../components/ui';
import { placeLabel, RiskChip, StatusChip, SUBJECT } from './common';

const RISKS = ['red', 'yellow', 'green'] as const;
const SUBJECTS = ['english', 'math'] as const;

function pick<T extends string>(v: string | null, options: readonly T[]): T | undefined {
  return options.find((o) => o === v);
}

// Every question across tests, red first: what editors work through.
export function PracticeQueuePage() {
  const [params, setParams] = useSearchParams();
  const filter: QueueFilter = {
    risk: pick(params.get('risk'), RISKS),
    subject: pick(params.get('subject'), SUBJECTS),
    status: params.get('status') === 'all' ? undefined : (pick(params.get('status'), ['draft', 'review', 'approved'] as const) ?? 'review'),
    offset: Math.max(0, Number(params.get('offset')) || 0),
  };
  const queue = usePracticeQueue(filter);
  const navigate = useNavigate();

  const set = (key: string, value: string | undefined) =>
    setParams(
      (p) => {
        if (value === undefined) p.delete(key);
        else p.set(key, value);
        if (key !== 'offset') p.delete('offset');
        return p;
      },
      { replace: true },
    );

  const total = queue.data?.total ?? 0;
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <div className="small">
            <Link to="/practice">Practice tests</Link>
          </div>
          <h1>Questions to fix</h1>
          <p className="muted">Red first, then yellow. A question several tests share appears once.</p>
        </div>
        <div className="toolbar">
          <select className="select" value={filter.risk ?? ''} onChange={(e) => set('risk', e.target.value || undefined)} aria-label="AI risk">
            <option value="">Any risk</option>
            {RISKS.map((r) => (
              <option key={r} value={r}>
                {r[0]!.toUpperCase() + r.slice(1)}
              </option>
            ))}
          </select>
          <select className="select" value={filter.subject ?? ''} onChange={(e) => set('subject', e.target.value || undefined)} aria-label="Subject">
            <option value="">Both subjects</option>
            {SUBJECTS.map((s) => (
              <option key={s} value={s}>
                {SUBJECT[s]}
              </option>
            ))}
          </select>
          <select className="select" value={params.get('status') ?? 'review'} onChange={(e) => set('status', e.target.value)} aria-label="Status">
            <option value="review">In review</option>
            <option value="approved">Approved</option>
            <option value="all">Any status</option>
          </select>
        </div>
      </div>

      {queue.isPending ? (
        <Loading />
      ) : queue.isError ? (
        <ErrorNotice error={queue.error} />
      ) : queue.data.questions.length === 0 ? (
        <div className="table-wrap">
          <Empty>Nothing here.</Empty>
        </div>
      ) : (
        <>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>AI</th>
                  <th>Question</th>
                  <th>Where</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {queue.data.questions.map((q) => {
                  const open = () => navigate(`/practice/questions/${q.id}`);
                  return (
                    <tr key={q.id} className="clickable" tabIndex={0} onClick={open} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && open()}>
                      <td>
                        <RiskChip risk={q.ai_risk} />
                      </td>
                      <td>
                        <div className="clamp">{q.preview || <span className="faint">(empty)</span>}</div>
                      </td>
                      <td className="small muted">{q.places.map((p) => placeLabel(p)).join(', ')}</td>
                      <td>
                        <StatusChip status={q.status} />
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          <div className="toolbar" style={{ justifyContent: 'space-between' }}>
            <span className="small muted tabular">
              {filter.offset + 1}–{filter.offset + queue.data.questions.length} of {total}
            </span>
            <span className="toolbar">
              <button className="btn secondary small" disabled={filter.offset === 0} onClick={() => set('offset', String(Math.max(0, filter.offset - 50)))}>
                Previous
              </button>
              <button className="btn secondary small" disabled={filter.offset + 50 >= total} onClick={() => set('offset', String(filter.offset + 50))}>
                Next
              </button>
            </span>
          </div>
        </>
      )}
    </div>
  );
}
