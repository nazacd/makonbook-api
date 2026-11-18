import { Trophy } from 'lucide-react';
import { Link, useNavigate } from 'react-router';

import { useCompetitions, type Competition } from '../api/hooks';
import { Empty, ErrorNotice, Loading, PageIcon } from '../components/ui';
import { dateTime } from '../lib/dates';

// Competitions (spec 26): timed contests students take once each. Editors
// make them here, from bank topics or questions reserved for them.

export const SUBJECT_NAME: Record<Competition['subject'], string> = { math: 'Math', english: 'Reading & Writing', mixed: 'Mixed' };

const STATUS: Record<Competition['status'], { label: string; tone: string }> = {
  draft: { label: 'Draft', tone: 'plain' },
  upcoming: { label: 'Published · upcoming', tone: 'accent' },
  live: { label: 'Live', tone: 'danger' },
  ended: { label: 'Ended', tone: 'plain' },
};

export function CompetitionStatus({ status }: { status: Competition['status'] }) {
  const s = STATUS[status];
  return <span className={`chip ${s.tone}`}>{s.label}</span>;
}

export function CompetitionsPage() {
  const list = useCompetitions();
  const navigate = useNavigate();
  const rows = list.data?.competitions ?? [];
  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>
            <PageIcon icon={Trophy} />
            Competitions
          </h1>
          <p className="muted">Timed contests every student with a phone can take, once. Answers stay hidden until the end.</p>
        </div>
        <Link className="btn" to="/competitions/new">
          New competition
        </Link>
      </div>
      {list.isPending ? (
        <Loading />
      ) : list.isError ? (
        <ErrorNotice error={list.error} />
      ) : rows.length === 0 ? (
        <div className="table-wrap">
          <Empty>No competitions yet. Make the first one: it stays a draft until you publish it.</Empty>
        </div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Competition</th>
                <th>Opens</th>
                <th>Closes</th>
                <th className="num">Questions</th>
                <th className="num">Taken</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => {
                const open = () => navigate(`/competitions/${c.id}`);
                return (
                  <tr key={c.id} className="clickable" tabIndex={0} onClick={open} onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && open()} aria-label={`${c.title}: open`}>
                    <td>
                      <b>{c.title}</b>
                      <div className="small muted">
                        {SUBJECT_NAME[c.subject]} · {c.source === 'reserved' ? 'reserved questions' : 'from the bank'} · {c.minutes} min{c.prize ? ' · prize' : ''}
                      </div>
                    </td>
                    <td className="nowrap small">{dateTime(c.starts_at)}</td>
                    <td className="nowrap small">{dateTime(c.ends_at)}</td>
                    <td className="num">{c.questions}</td>
                    <td className="num">{c.participants || <span className="faint">0</span>}</td>
                    <td>
                      <CompetitionStatus status={c.status} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
