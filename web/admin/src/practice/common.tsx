import type { QuestionSummary } from '../api/hooks';

type Place = QuestionSummary['places'][number];

export const SUBJECT: Record<string, string> = { english: 'R&W', math: 'Math' };

/** "DAY12 · R&W M2 #14" */
export function placeLabel(p: Place, withTest = true): string {
  return `${withTest ? `${p.test} · ` : ''}${SUBJECT[p.subject] ?? p.subject} M${p.module} #${p.position}`;
}

const RISK: Record<string, { label: string; tone: string; help: string }> = {
  red: { label: 'Red', tone: 'danger', help: 'The AI found a real problem (wrong key, broken or ambiguous question): a teacher decides.' },
  yellow: { label: 'Yellow', tone: 'warning', help: 'A solver disagreed or wasn’t sure, or a smaller problem was found.' },
  green: { label: 'Green', tone: 'success', help: 'Both blind solvers agreed with the key; nothing important found.' },
};

export function RiskChip({ risk }: { risk: string | null | undefined }) {
  if (!risk) return null;
  const r = RISK[risk] ?? { label: risk, tone: '', help: '' };
  return (
    <span className={`chip ${r.tone}`} title={r.help}>
      {r.label}
    </span>
  );
}

const STATUS: Record<string, { label: string; tone: string }> = {
  draft: { label: 'Draft', tone: 'plain' },
  review: { label: 'In review', tone: 'plain' },
  approved: { label: 'Approved', tone: 'success' },
};

export function StatusChip({ status, auto }: { status: string; auto?: boolean }) {
  const s = STATUS[status] ?? { label: status, tone: 'plain' };
  return (
    <span className={`chip ${s.tone}`} title={auto ? 'Approved automatically: its AI review was green' : undefined}>
      {auto && status === 'approved' ? 'Approved by AI review' : s.label}
    </span>
  );
}

/** Other tests that use the question too. */
export function AlsoIn({ places, test }: { places: Place[]; test?: string }) {
  const others = places.filter((p) => p.test !== test);
  if (others.length === 0) return null;
  return <div className="small faint">Also in {others.map((p) => placeLabel(p)).join(', ')}</div>;
}
