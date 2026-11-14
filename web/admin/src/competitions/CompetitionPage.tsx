import { ArrowDown, ArrowLeft, ArrowUp, Trophy, X } from 'lucide-react';
import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';

import {
  useCompetition,
  useCompetitionBoard,
  useDeleteCompetition,
  useDrawCompetition,
  usePublishCompetition,
  useReservable,
  useSaveCompetition,
  useTaxonomy,
  type Competition,
  type CompetitionInput,
  type CompetitionQuestion,
} from '../api/hooks';
import { ErrorNotice, Loading, Notice, PageIcon } from '../components/ui';
import { SCHOOL_TZ } from '../lib/dates';
import { StatusChip } from '../practice/common';
import { CompetitionStatus, SUBJECT_NAME } from './CompetitionsPage';

// One competition: its details, its questions (drawn from the bank, or
// reserved questions picked one by one), publishing and the board. Once it
// has started or anyone has taken it, only the words can change.

type Subject = 'math' | 'english';
type Form = Omit<CompetitionInput, 'question_ids' | 'starts_at' | 'ends_at'> & { starts_at: string; ends_at: string };

// <input type="datetime-local"> in school time (UTC+5, no daylight saving).
const localFmt = new Intl.DateTimeFormat('sv-SE', { timeZone: SCHOOL_TZ, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
const toLocal = (iso: string) => localFmt.format(new Date(iso)).replace(' ', 'T');
const fromLocal = (v: string) => new Date(`${v}:00+05:00`).toISOString();

export function CompetitionPage() {
  const { id = '' } = useParams();
  const isNew = id === 'new';
  const c = useCompetition(isNew ? '' : id);
  if (!isNew && c.isPending) return <Loading />;
  if (!isNew && c.isError) return <ErrorNotice error={c.error} />;
  return <Editor key={id} id={isNew ? '' : id} competition={c.data?.competition} questions={c.data?.questions ?? []} />;
}

function blank(): Form {
  const start = new Date(Date.now() + 2 * 86400e3);
  start.setUTCMinutes(0, 0, 0);
  return { title: '', description: '', prize: '', subject: 'math', source: 'bank', starts_at: toLocal(start.toISOString()), ends_at: toLocal(new Date(start.getTime() + 3 * 3600e3).toISOString()), minutes: 30 };
}

function Editor({ id, competition: c, questions: initial }: { id: string; competition?: Competition; questions: CompetitionQuestion[] }) {
  const navigate = useNavigate();
  const [form, setForm] = useState<Form>(() =>
    c ? { title: c.title, description: c.description, prize: c.prize, subject: c.subject, source: c.source, starts_at: toLocal(c.starts_at), ends_at: toLocal(c.ends_at), minutes: c.minutes } : blank(),
  );
  const [questions, setQuestions] = useState<CompetitionQuestion[]>(initial);
  const [saved, setSaved] = useState(false);
  const [dirty, setDirty] = useState(false);
  const save = useSaveCompetition(id);
  const publish = usePublishCompetition(id);
  const remove = useDeleteCompetition(id);
  // Started or taken: only the title, description and prize change.
  const locked = !!c && (c.status === 'live' || c.status === 'ended' || c.participants > 0);
  const set = <K extends keyof Form>(k: K, v: Form[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
    setSaved(false);
    setDirty(true);
  };
  const setQs = (next: CompetitionQuestion[]) => {
    setQuestions(next);
    setSaved(false);
    setDirty(true);
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    save.mutate(
      { ...form, starts_at: fromLocal(form.starts_at), ends_at: fromLocal(form.ends_at), question_ids: questions.map((q) => q.id) },
      {
        onSuccess: (r) => {
          setSaved(true);
          setDirty(false);
          if (!id) void navigate(`/competitions/${r.competition.id}`, { replace: true });
        },
      },
    );
  };

  return (
    <div className="stack">
      <Link to="/competitions" className="btn link" style={{ justifySelf: 'start' }}>
        <ArrowLeft size={15} aria-hidden /> Competitions
      </Link>
      <div className="page-head">
        <div>
          <h1>
            <PageIcon icon={Trophy} />
            {c ? c.title : 'New competition'}
          </h1>
          {c && (
            <p className="muted">
              <CompetitionStatus status={c.status} /> {c.participants} taken part
            </p>
          )}
        </div>
        {c && (
          <div className="toolbar">
            {c.status === 'draft' ? (
              <button className="btn success" disabled={publish.isPending || dirty} onClick={() => publish.mutate(true)} title={dirty ? 'Save your changes first' : 'Checks the questions first'}>
                Publish
              </button>
            ) : c.status === 'upcoming' ? (
              <button className="btn secondary" disabled={publish.isPending} onClick={() => publish.mutate(false)}>
                Take back to draft
              </button>
            ) : null}
            {c.participants === 0 && (
              <button
                className="btn danger"
                disabled={remove.isPending}
                onClick={() => window.confirm(`Delete “${c.title}”? This can’t be undone.`) && remove.mutate(undefined, { onSuccess: () => void navigate('/competitions', { replace: true }) })}>
                Delete
              </button>
            )}
          </div>
        )}
      </div>
      {publish.isError && <ErrorNotice error={publish.error} title="Not published" />}
      {remove.isError && <ErrorNotice error={remove.error} title="Not deleted" />}
      {locked && <Notice tone="info">It has started or someone has taken it, so only the title, description and prize can change.</Notice>}

      <form className="panel" onSubmit={submit}>
        <h2>Details</h2>
        <div className="field">
          <label htmlFor="c-title">Title</label>
          <input id="c-title" className="input" required maxLength={120} value={form.title} onChange={(e) => set('title', e.target.value)} placeholder="Weekly Math Sprint" />
        </div>
        <div className="field">
          <label htmlFor="c-desc">Description</label>
          <textarea id="c-desc" className="textarea" style={{ fontFamily: 'inherit' }} rows={3} maxLength={2000} value={form.description} onChange={(e) => set('description', e.target.value)} placeholder="What it covers, who it suits" />
        </div>
        <div className="field">
          <label htmlFor="c-prize">Prize (optional)</label>
          <input id="c-prize" className="input" maxLength={300} value={form.prize} onChange={(e) => set('prize', e.target.value)} placeholder="Leave empty for none" />
        </div>
        <div className="grid-3">
          <div className="field">
            <label htmlFor="c-subject">Subject</label>
            <select id="c-subject" className="select" disabled={locked} value={form.subject} onChange={(e) => set('subject', e.target.value as Form['subject'])}>
              <option value="math">Math</option>
              <option value="english">Reading & Writing</option>
              <option value="mixed">Mixed</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor="c-source">Questions</label>
            <select id="c-source" className="select" disabled={locked} value={form.source} onChange={(e) => set('source', e.target.value as Form['source'])}>
              <option value="bank">From the bank</option>
              <option value="reserved">Reserved for it</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor="c-minutes">Time limit (minutes)</label>
            <input id="c-minutes" className="input" type="number" min={1} max={300} required disabled={locked} value={form.minutes} onChange={(e) => set('minutes', Number(e.target.value))} />
          </div>
        </div>
        <p className="small muted">
          {form.source === 'bank'
            ? 'Bank questions are drawn by topic and difficulty. Students may have seen them in practice or a downloaded mock test, so these suit friendly contests.'
            : 'Reserved questions are ones written for this competition and in no mock test: fair enough for prizes. They leave the bank as soon as you choose them.'}
        </p>
        <div className="grid-3">
          <div className="field">
            <label htmlFor="c-start">Opens (Tashkent time)</label>
            <input id="c-start" className="input" type="datetime-local" required disabled={locked} value={form.starts_at} onChange={(e) => set('starts_at', e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="c-end">Closes (Tashkent time)</label>
            <input id="c-end" className="input" type="datetime-local" required disabled={locked} value={form.ends_at} onChange={(e) => set('ends_at', e.target.value)} />
          </div>
        </div>

        <h2>
          Questions <span className="muted tabular">{questions.length}</span>
        </h2>
        <QuestionList questions={questions} locked={locked} onChange={setQs} />
        {!locked && (form.source === 'bank' ? <Draw subject={form.subject} have={questions} onAdd={(qs) => setQs([...questions, ...qs])} /> : <Reserve id={id} subject={form.subject} have={questions} onAdd={(q) => setQs([...questions, q])} />)}

        {save.isError && <ErrorNotice error={save.error} title="Not saved" />}
        <div className="toolbar">
          <button className="btn" type="submit" disabled={save.isPending}>
            {save.isPending ? 'Saving…' : id ? 'Save changes' : 'Save as a draft'}
          </button>
          {saved && <span className="small muted">Saved.</span>}
        </div>
      </form>

      {c && c.status !== 'draft' && c.status !== 'upcoming' && <Board id={c.id} live={c.status === 'live'} />}
    </div>
  );
}

function QuestionList({ questions, locked, onChange }: { questions: CompetitionQuestion[]; locked: boolean; onChange: (q: CompetitionQuestion[]) => void }) {
  if (questions.length === 0) return <p className="small muted">None yet. Students see them in this order.</p>;
  const move = (i: number, d: number) => {
    const next = [...questions];
    [next[i], next[i + d]] = [next[i + d]!, next[i]!];
    onChange(next);
  };
  return (
    <div className="table-wrap">
      <table>
        <tbody>
          {questions.map((q, i) => (
            <tr key={q.id}>
              <td className="num tabular">{i + 1}</td>
              <td>
                <div className="clamp">{q.preview}</div>
                <div className="small muted">
                  {SUBJECT_NAME[q.subject]} · {q.skill} · {q.difficulty ?? 'no difficulty'}
                </div>
              </td>
              <td>
                <StatusChip status={q.status} />
              </td>
              <td className="nowrap">
                <Link className="btn link small" style={{ marginRight: 6 }} to={`/practice/questions/${q.id}`} target="_blank">
                  Open
                </Link>
                {!locked && (
                  <>
                    <button type="button" className="icon-btn" aria-label="Move up" disabled={i === 0} onClick={() => move(i, -1)}>
                      <ArrowUp size={15} />
                    </button>
                    <button type="button" className="icon-btn" aria-label="Move down" disabled={i === questions.length - 1} onClick={() => move(i, 1)}>
                      <ArrowDown size={15} />
                    </button>
                    <button type="button" className="icon-btn" aria-label="Remove" onClick={() => onChange(questions.filter((x) => x.id !== q.id))}>
                      <X size={15} />
                    </button>
                  </>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Draws bank questions by topic and difficulty, added to the list. */
function Draw({ subject, have, onAdd }: { subject: Form['subject']; have: CompetitionQuestion[]; onAdd: (q: CompetitionQuestion[]) => void }) {
  const [from, setFrom] = useState<Subject>(subject === 'english' ? 'english' : 'math');
  const sub: Subject = subject === 'mixed' ? from : subject;
  const taxonomy = useTaxonomy();
  const [skills, setSkills] = useState<string[]>([]);
  const [n, setN] = useState({ easy: 2, medium: 3, hard: 2 });
  const draw = useDrawCompetition();
  const domains = taxonomy.data?.subjects[sub] ?? {};
  const go = () =>
    draw.mutate(
      { subject: sub, skills: skills.filter((s) => Object.values(domains).flat().includes(s)), ...n },
      { onSuccess: (r) => onAdd(r.questions.filter((q) => !have.some((h) => h.id === q.id))) },
    );
  return (
    <div className="card stack">
      <b>Draw from the bank</b>
      {subject === 'mixed' && (
        <div className="seg" role="group" aria-label="Subject to draw from">
          {(['math', 'english'] as const).map((s) => (
            <button key={s} type="button" className={from === s ? 'on' : ''} onClick={() => setFrom(s)}>
              {SUBJECT_NAME[s]}
            </button>
          ))}
        </div>
      )}
      <p className="small muted">Topics (none ticked: the whole subject). The same questions go to everyone, easy to hard. Questions another competition holds are left out.</p>
      {Object.entries(domains).map(([domain, list]) => (
        <div key={domain}>
          <div className="small muted">{domain}</div>
          <div className="checks">
            {list.map((s) => (
              <label key={s} className="check small">
                <input type="checkbox" checked={skills.includes(s)} onChange={(e) => setSkills((x) => (e.target.checked ? [...x, s] : x.filter((y) => y !== s)))} /> {s}
              </label>
            ))}
          </div>
        </div>
      ))}
      <div className="toolbar">
        {(['easy', 'medium', 'hard'] as const).map((d) => (
          <label key={d} className="field">
            <span className="small muted">{d}</span>
            <input className="input" style={{ width: 80 }} type="number" min={0} max={100} value={n[d]} onChange={(e) => setN((x) => ({ ...x, [d]: Math.max(0, Number(e.target.value)) }))} />
          </label>
        ))}
        <button type="button" className="btn secondary" style={{ alignSelf: 'end' }} disabled={draw.isPending || n.easy + n.medium + n.hard === 0} onClick={go}>
          {draw.isPending ? 'Drawing…' : `Draw ${n.easy + n.medium + n.hard}`}
        </button>
      </div>
      {draw.isError && <ErrorNotice error={draw.error} title="Nothing drawn" />}
      {draw.isSuccess && <span className="small muted">Drew {draw.data.questions.length}; ones already in the list were skipped.</span>}
    </div>
  );
}

/** Picks reserved questions: in review or approved, in no mock test, held by no other competition. */
function Reserve({ id, subject, have, onAdd }: { id: string; subject: Form['subject']; have: CompetitionQuestion[]; onAdd: (q: CompetitionQuestion) => void }) {
  const [text, setText] = useState('');
  const [q, setQ] = useState('');
  const list = useReservable({ competition: id || undefined, subject: subject === 'mixed' ? undefined : subject, q: q || undefined });
  const rows = (list.data?.questions ?? []).filter((x) => !have.some((h) => h.id === x.id));
  return (
    <div className="card stack">
      <b>Pick reserved questions</b>
      <p className="small muted">Questions in no mock test. Write new ones in the question editor first; they need approving before you publish.</p>
      <div className="toolbar">
        <input className="input" placeholder="Search the text" value={text} onChange={(e) => setText(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), setQ(text))} />
        <button type="button" className="btn secondary" onClick={() => setQ(text)}>
          Search
        </button>
      </div>
      {list.isPending ? (
        <Loading />
      ) : list.isError ? (
        <ErrorNotice error={list.error} />
      ) : rows.length === 0 ? (
        <p className="small muted">No questions to pick.</p>
      ) : (
        <div className="table-wrap">
          <table>
            <tbody>
              {rows.map((x) => (
                <tr key={x.id}>
                  <td>
                    <div className="clamp">{x.preview}</div>
                    <div className="small muted">
                      {SUBJECT_NAME[x.subject]} · {x.skill} · {x.difficulty ?? 'no difficulty'}
                    </div>
                  </td>
                  <td>
                    <StatusChip status={x.status} />
                  </td>
                  <td>
                    <button type="button" className="btn small secondary" onClick={() => onAdd(x)}>
                      Add
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function Board({ id, live }: { id: string; live: boolean }) {
  const board = useCompetitionBoard(id, live);
  const b = board.data;
  return (
    <section className="panel">
      <h2>
        Board <span className="muted tabular">{b?.participants ?? ''}</span>
      </h2>
      {board.isPending ? (
        <Loading />
      ) : board.isError ? (
        <ErrorNotice error={board.error} />
      ) : b!.entries.length === 0 ? (
        <p className="small muted">Nobody has handed in yet.</p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th className="num">Place</th>
                <th>Student</th>
                <th className="num">Right</th>
                <th className="num">Time</th>
              </tr>
            </thead>
            <tbody>
              {b!.entries.map((e, i) => (
                <tr key={i}>
                  <td className="num tabular">{e.rank}</td>
                  <td>{e.name}</td>
                  <td className="num tabular">
                    {e.correct}/{e.total}
                  </td>
                  <td className="num tabular">
                    {Math.floor(e.seconds / 60)}:{String(e.seconds % 60).padStart(2, '0')}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
