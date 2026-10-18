import { useEffect, useState } from 'react';
import { Link, useBlocker, useParams, useSearchParams } from 'react-router';

import { ApiError, describe } from '../api/client';
import {
  usePracticeQuestion,
  usePracticeTest,
  useApproveQuestion,
  useSaveQuestion,
  useTaxonomy,
  type Figure,
  type Question,
  type QuestionEdit,
} from '../api/hooks';
import { ErrorNotice, Loading, Notice } from '../components/ui';
import { dateTime } from '../lib/dates';
import { AlsoIn, placeLabel, RiskChip, StatusChip, SUBJECT } from './common';
import { figuresIn } from './format';
import { RichEditor } from './RichEditor';
import { figureUrl, RichText } from './RichText';

const LETTERS = ['A', 'B', 'C', 'D'] as const;

// One question: the editor on the left; how a student sees it, what the AI
// found, the bank's original screenshots and the history on the right.
export function PracticeQuestionPage() {
  const { id = '' } = useParams();
  const q = usePracticeQuestion(id);
  // Here, not in the form: saving remounts the form, and "Saved" must stay.
  const save = useSaveQuestion(id);
  if (q.isPending) return <Loading />;
  if (q.isError) return <ErrorNotice error={q.error} title="Couldn’t load this question" />;
  // A new version (after saving) starts a fresh form.
  return <Editor key={`${q.data.id}-${q.data.version}`} question={q.data} save={save} />;
}

type Form = {
  passage: string;
  stem: string;
  choices: Record<string, string>;
  correct: string;
  accepted: string; // comma-separated, as typed
  explanation: string;
  domain: string;
  skill: string;
  difficulty: string;
  figures: Figure[];
};

function formOf(c: {
  passage?: string | null;
  stem?: string;
  choices?: Record<string, string> | null;
  correct?: string | null;
  accepted?: string[] | null;
  explanation?: string;
  domain?: string;
  skill?: string;
  difficulty?: string | null;
  figures?: Figure[] | null;
}): Form {
  return {
    passage: c.passage ?? '',
    stem: c.stem ?? '',
    choices: { A: '', B: '', C: '', D: '', ...(c.choices ?? {}) },
    correct: c.correct ?? '',
    accepted: (c.accepted ?? []).join(', '),
    explanation: c.explanation ?? '',
    domain: c.domain ?? '',
    skill: c.skill ?? '',
    difficulty: c.difficulty ?? '',
    figures: c.figures ?? [],
  };
}

function Editor({ question: q, save }: { question: Question; save: ReturnType<typeof useSaveQuestion> }) {
  const [initial] = useState(() => formOf(q));
  const [form, setForm] = useState<Form>(initial);
  const taxonomy = useTaxonomy();
  const [params] = useSearchParams();
  const testCode = params.get('test') ?? undefined;
  const mcq = q.type === 'mcq';

  const dirty = JSON.stringify(form) !== JSON.stringify(initial);
  const blocker = useBlocker(({ currentLocation, nextLocation }) => dirty && currentLocation.pathname !== nextLocation.pathname);
  useEffect(() => {
    if (blocker.state !== 'blocked') return;
    if (window.confirm('Leave without saving your changes?')) blocker.proceed();
    else blocker.reset();
  }, [blocker]);
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [dirty]);

  const set = <K extends keyof Form>(key: K, value: Form[K]) => setForm((f) => ({ ...f, [key]: value }));
  const approval = useApproveQuestion(q.id);
  const fields = {
    ...(save.error instanceof ApiError ? (save.error.fields ?? {}) : {}),
    ...(approval.error instanceof ApiError ? (approval.error.fields ?? {}) : {}),
  };
  const conflict = [save.error, approval.error].some((e) => e instanceof ApiError && e.code === 'version_conflict');
  const approved = q.status === 'approved';

  // The figures are the images placed in the text; each keeps the kind the
  // review gave it (graph, chart…), new uploads are "other".
  function figuresOf(f: Form): Figure[] {
    const kinds = new Map(q.figures.map((x) => [x.key, x.kind]));
    const seen = new Set<string>();
    const out: Figure[] = [];
    for (const t of [f.passage, f.stem, ...(mcq ? LETTERS.map((l) => f.choices[l] ?? '') : [])]) {
      for (const x of figuresIn(t)) {
        if (seen.has(x.key)) continue;
        seen.add(x.key);
        out.push({ key: x.key, alt: x.alt, kind: kinds.get(x.key) ?? 'other' });
      }
    }
    return out;
  }

  function submit() {
    const body: QuestionEdit = {
      version: q.version,
      passage: form.passage.trim() ? form.passage : null,
      stem: form.stem,
      choices: mcq ? form.choices : null,
      figures: figuresOf(form),
      correct: mcq ? form.correct : '',
      accepted: mcq ? null : form.accepted.split(',').map((a) => a.trim()).filter(Boolean),
      explanation: form.explanation,
      domain: form.domain,
      skill: form.skill,
      difficulty: (form.difficulty || '') as QuestionEdit['difficulty'],
    };
    save.mutate(body);
  }

  const domains = taxonomy.data?.subjects[q.subject] ?? {};
  const preview: Form = form;

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <div className="small">
            <Link to="/practice">Practice tests</Link>
            {testCode && (
              <>
                {' · '}
                <Link to={`/practice/tests/${testCode}`}>{testCode}</Link>
              </>
            )}
            {' · '}
            <Link to="/practice/queue">Questions to fix</Link>
          </div>
          <h1>{q.places.length > 0 ? placeLabel(q.places.find((p) => p.test === testCode) ?? q.places[0]!) : 'Question'}</h1>
          <div className="toolbar" style={{ marginTop: 8 }}>
            <RiskChip risk={q.ai_risk} />
            <StatusChip status={q.status} auto={q.auto_approved} />
            <span className="chip plain">{SUBJECT[q.subject]}</span>
            <span className="chip plain">{mcq ? 'Multiple choice' : 'Grid-in'}</span>
            <span className="small faint">Version {q.version}</span>
          </div>
          <AlsoIn places={q.places} test={testCode ?? q.places[0]?.test} />
        </div>
        <div className="toolbar">
          {testCode && <PrevNext test={testCode} id={q.id} />}
          <button className="btn secondary" disabled={!dirty || save.isPending} onClick={() => setForm(initial)}>
            Undo changes
          </button>
          <button className="btn" disabled={!dirty || save.isPending} onClick={submit}>
            {save.isPending ? 'Saving…' : 'Save'}
          </button>
          {approved ? (
            <button
              className="btn secondary"
              disabled={dirty || approval.isPending}
              onClick={() => approval.mutate({ version: q.version, approved: false })}>
              Take back approval
            </button>
          ) : (
            <button
              className="btn success"
              disabled={dirty || approval.isPending}
              title={dirty ? 'Save your changes first' : 'It’s right and ready for students'}
              onClick={() => approval.mutate({ version: q.version, approved: true })}>
              {approval.isPending ? 'Approving…' : 'Approve'}
            </button>
          )}
        </div>
      </div>

      {q.places.length > 1 && (
        <Notice tone="info">This question is in {q.places.length} tests; a change here changes it in all of them.</Notice>
      )}
      {approved && (
        <Notice tone="success">
          {q.auto_approved
            ? 'Approved automatically: its AI review is green.'
            : `Approved${q.approved_by ? ` by ${q.approved_by}` : ''}${q.approved_at ? `, ${dateTime(q.approved_at)}` : ''}.`}{' '}
          Saving a change sends it back to review.
        </Notice>
      )}
      {save.isSuccess && !dirty && save.data.id === q.id && !approved && (
        <Notice tone="info">Saved as version {q.version}. When it’s right, approve it.</Notice>
      )}
      {approval.isError && !conflict && (
        <Notice tone="error" title={Object.keys(fields).length ? 'It can’t be approved yet: fix the marked fields' : 'Couldn’t change the approval'}>
          {Object.keys(fields).length ? null : describe(approval.error)}
        </Notice>
      )}
      {conflict && (
        <Notice tone="error" title="Someone saved this question after you opened it">
          Copy what you changed, then{' '}
          <button className="btn link" onClick={() => window.location.reload()}>
            reload
          </button>{' '}
          to see their version.
        </Notice>
      )}
      {save.isError && !conflict && (
        <Notice tone="error" title={Object.keys(fields).length ? 'Fix the marked fields' : 'Couldn’t save'}>
          {Object.keys(fields).length ? null : describe(save.error)}
          {Object.entries(fields)
            .filter(([k]) => k.startsWith('figures'))
            .map(([k, v]) => (
              <div key={k} className="field-error">
                {k === 'figures' ? v : `Image ${Number(k.split('.')[1]) + 1}: ${v}`}
              </div>
            ))}
          {fields.choices && <div className="field-error">{fields.choices}</div>}
        </Notice>
      )}

      <div className="editor">
        <div className="stack">
          <div className="panel">
            {(q.subject === 'english' || form.passage) && (
              <RichField label="Passage" value={form.passage} onChange={(v) => set('passage', v)} error={fields.passage} />
            )}
            <RichField label="Question" value={form.stem} onChange={(v) => set('stem', v)} error={fields.stem} />
            {mcq ? (
              <div className="field">
                <label>Choices (pick the correct one)</label>
                {LETTERS.map((l) => (
                  <div key={l} className="choice-edit">
                    <label className="check" title="Correct answer">
                      <input type="radio" name="correct" checked={form.correct === l} onChange={() => set('correct', l)} /> {l}
                    </label>
                    <div className="field" style={{ flex: 1 }}>
                      <RichEditor
                        compact
                        rich={false}
                        label={`Choice ${l}`}
                        value={form.choices[l] ?? ''}
                        invalid={!!fields[`choices.${l}`]}
                        onChange={(v) => setForm((f) => ({ ...f, choices: { ...f.choices, [l]: v } }))}
                      />
                      {fields[`choices.${l}`] && <span className="field-error">{fields[`choices.${l}`]}</span>}
                    </div>
                  </div>
                ))}
                {fields.correct && <span className="field-error">{fields.correct}</span>}
              </div>
            ) : (
              <div className="field">
                <label htmlFor="accepted">Correct answers (every form a student may enter, separated by commas)</label>
                <input id="accepted" className="input" value={form.accepted} onChange={(e) => set('accepted', e.target.value)} placeholder="17/2, 8.5" />
                {fields.accepted && <span className="field-error">{fields.accepted}</span>}
              </div>
            )}
            <RichField label="Explanation" value={form.explanation} onChange={(v) => set('explanation', v)} error={fields.explanation} images={false} />

            <div className="grid-3">
              <div className="field">
                <label htmlFor="domain">Domain</label>
                <select
                  id="domain"
                  className="select"
                  value={form.domain}
                  onChange={(e) => setForm((f) => ({ ...f, domain: e.target.value, skill: '' }))}>
                  <option value="">—</option>
                  {Object.keys(domains).map((d) => (
                    <option key={d}>{d}</option>
                  ))}
                </select>
                {fields.domain && <span className="field-error">{fields.domain}</span>}
              </div>
              <div className="field">
                <label htmlFor="skill">Skill</label>
                <select id="skill" className="select" value={form.skill} onChange={(e) => set('skill', e.target.value)}>
                  <option value="">—</option>
                  {(domains[form.domain] ?? []).map((s) => (
                    <option key={s}>{s}</option>
                  ))}
                </select>
                {fields.skill && <span className="field-error">{fields.skill}</span>}
              </div>
              <div className="field">
                <label htmlFor="difficulty">Difficulty</label>
                <select id="difficulty" className="select" value={form.difficulty} onChange={(e) => set('difficulty', e.target.value)}>
                  <option value="">—</option>
                  <option value="easy">Easy</option>
                  <option value="medium">Medium</option>
                  <option value="hard">Hard</option>
                </select>
              </div>
            </div>

            <EditorHelp />
          </div>
        </div>

        <div className="stack">
          <div className="panel">
            <h2>Student view</h2>
            <QuestionView form={preview} mcq={mcq} />
            <details>
              <summary className="small muted">Explanation</summary>
              <RichText text={preview.explanation} />
            </details>
          </div>
          {q.reviews.map((r) => (
            <AIReview key={`${r.test}-${r.qid}`} review={r} />
          ))}
          <History question={q} onLoad={(content) => setForm(formOf(content as Parameters<typeof formOf>[0]))} />
        </div>
      </div>
    </div>
  );
}

function RichField({ label, value, onChange, error, images = true }: { label: string; value: string; onChange: (v: string) => void; error?: string; images?: boolean }) {
  return (
    <div className="field">
      <label>{label}</label>
      <RichEditor label={label} value={value} onChange={onChange} images={images} invalid={!!error} />
      {error && <span className="field-error">{error}</span>}
    </div>
  );
}

function QuestionView({ form, mcq }: { form: Form; mcq: boolean }) {
  return (
    <div className="student">
      {form.passage && <RichText text={form.passage} className="passage" />}
      <RichText text={form.stem} />
      {mcq ? (
        <ol className="choices">
          {LETTERS.map((l) => (
            <li key={l} className={form.correct === l ? 'correct' : ''}>
              <span className="letter">{l}</span>
              <RichText text={form.choices[l]} />
            </li>
          ))}
        </ol>
      ) : (
        <p className="small muted">Answer: {form.accepted || '—'}</p>
      )}
    </div>
  );
}

function EditorHelp() {
  return (
    <details className="small muted">
      <summary>How to edit</summary>
      <ul>
        <li>Select words and press B, I or U (or Ctrl+B, Ctrl+I, Ctrl+U).</li>
        <li>
          √x adds math (or Ctrl+M). Type as on a calculator: <b>/</b> makes a fraction, <b>^</b> a power, <b>sqrt</b> a root,{' '}
          <b>pi</b> gives π. Click any math to change it.
        </li>
        <li>Paste a screenshot straight into the text, or use the picture button; then describe what it shows.</li>
        <li>___ adds a blank to fill. Lists and tables have their own buttons.</li>
        <li>&lt;/&gt; shows the text code behind it, if you ever need it.</li>
      </ul>
    </details>
  );
}

// ---- What the AI found ----

type Rec = Record<string, unknown>;
const obj = (v: unknown): Rec => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Rec) : {});
const arr = (v: unknown): Rec[] => (Array.isArray(v) ? v.map(obj) : []);
const str = (v: unknown): string => (typeof v === 'string' ? v : v == null ? '' : String(v));

// The headline of a review: what the AI concluded, in a sentence.
function verdictLine(r: Rec): string {
  const key = str(r.key);
  const answer = key || (Array.isArray(r.accepted) ? r.accepted.map(String).join(', ') : ''); // grid-in: every accepted form
  const solves = arr(r.blind_solves);
  const agree = solves.length > 0 && solves.every((s) => s.matches_key === true);
  switch (str(r.verdict)) {
    case 'key_correct':
      return `The answer ${answer ? `(${answer}) ` : ''}is right${agree ? ': both AI solvers got it' : ''}.`;
    case 'key_wrong':
      return `The bank’s answer ${str(r.bank_key) ? `(${str(r.bank_key)}) ` : ''}looks wrong${str(r.proposed_key) || key ? `; the AI’s answer is ${str(r.proposed_key) || key}` : ''}. Check it and fix the key.`;
    case 'ambiguous':
      return 'More than one answer can be argued for. The question needs a fix so only one is right.';
    case 'broken':
      return 'The question can’t be answered as written (something is missing or wrong). It needs a fix.';
    case 'unsolvable':
      return 'The AI couldn’t solve it. Check it by hand.';
    default:
      return str(r.verdict).replace(/_/g, ' ');
  }
}

// Why a review isn't green, in plain words (its risk reasons; issues and the
// verdict are shown on their own).
function whyLines(r: Rec): string[] {
  const out = new Set<string>();
  for (const reason of Array.isArray(r.risk_reasons) ? r.risk_reasons.map(String) : []) {
    if (reason.startsWith('transcription')) out.add('The text was copied from the bank’s screenshots with some doubt: compare it with them.');
    else if (reason.endsWith('disagrees with the key')) out.add('One AI solver got a different answer: check the key.');
    else if (/^(opus|sonnet) (medium|low)$/.test(reason)) out.add('An AI solver wasn’t fully sure of its answer.');
  }
  return [...out];
}

/** The AI review, summed up; everything it recorded is one click away. */
function AIReview({ review }: { review: Question['reviews'][number] }) {
  const r = obj(review.record);
  const adj = obj(r.adjudication);
  const ex = obj(r.explanation);
  const images = obj(obj(r.source).images);
  const issues = arr(r.issues);
  const toFix = issues.filter((i) => str(i.severity) !== 'cosmetic');
  const keyChanged = str(r.bank_key) !== str(r.key);
  return (
    <div className="panel">
      <div className="card-head" style={{ margin: 0 }}>
        <h2>AI review · {review.test}</h2>
        <RiskChip risk={review.risk} />
      </div>
      <p className="ai-summary">{verdictLine(r)}</p>
      {whyLines(r).length > 0 && (
        <ul className="issues small">
          {whyLines(r).map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      )}
      {toFix.length > 0 && (
        <ul className="issues">
          {toFix.map((i, k) => (
            <li key={k}>
              <span className={`chip ${str(i.severity) === 'blocking' ? 'danger' : 'warning'}`}>{str(i.severity) === 'blocking' ? 'must fix' : 'fix'}</span>{' '}
              {str(i.detail)}
            </li>
          ))}
        </ul>
      )}
      <details>
        <summary className="small muted">Full review</summary>
        <dl className="facts">
          <dt>Verdict</dt>
          <dd>{str(r.verdict).replace('_', ' ')}</dd>
          <dt>Bank’s key</dt>
          <dd>
            {str(r.bank_key) || '—'}
            {keyChanged && (
              <>
                {' '}
                → AI proposes <b>{str(r.key)}</b>
              </>
            )}
          </dd>
          <dt>Blind solves</dt>
          <dd>
            {arr(r.blind_solves).map((s, i) => (
              <div key={i}>
                <b>{str(s.answer)}</b> <span className="faint">({str(s.solver)}, {str(s.confidence)})</span> {str(s.reasoning)}
              </div>
            ))}
          </dd>
          {str(adj.reason) && (
            <>
              <dt>Decision</dt>
              <dd>
                {str(adj.reason)}
                {str(adj.proof) && <div className="small muted pre">{str(adj.proof)}</div>}
              </dd>
            </>
          )}
          <dt>Bank explanation</dt>
          <dd>
            {str(ex.bank_status).replace('_', ' ') || '—'}
            {str(ex.disputed_quote) && <div className="small muted">Disputed: “{str(ex.disputed_quote)}”</div>}
          </dd>
        </dl>
        {issues.length > 0 && (
          <ul className="issues">
            {issues.map((i, k) => (
              <li key={k}>
                <span className={`chip ${str(i.severity) === 'blocking' ? 'danger' : str(i.severity) === 'content' ? 'warning' : 'plain'}`}>
                  {str(i.severity)}
                </span>{' '}
                <b>{str(i.kind).replace(/_/g, ' ')}</b>: {str(i.detail)}
              </li>
            ))}
          </ul>
        )}
        {r.proposed_fix != null && (
          <div>
            <div className="small muted">Proposed fix (apply it in the editor if it’s right)</div>
            <pre className="pre small">{JSON.stringify(r.proposed_fix, null, 1)}</pre>
          </div>
        )}
      </details>
      {Object.keys(images).length > 0 && (
        <details>
          <summary className="small muted">The bank’s original screenshots</summary>
          <div className="originals">
            {Object.entries(images).map(([slot, key]) => (
              <figure key={slot}>
                <img src={figureUrl(str(key))} alt={`Original ${slot}`} loading="lazy" />
                <figcaption className="small faint">{slot === 'question' ? 'Question' : `Choice ${slot}`}</figcaption>
              </figure>
            ))}
          </div>
        </details>
      )}
    </div>
  );
}

function History({ question: q, onLoad }: { question: Question; onLoad: (content: Rec) => void }) {
  return (
    <div className="panel">
      <h2>History</h2>
      <ul className="timeline">
        {q.versions.map((v) => (
          <li key={v.version}>
            <span className="small faint">v{v.version}</span>
            <span className="toolbar" style={{ justifyContent: 'space-between' }}>
              <span>
                {v.source === 'import' ? 'Loaded from the AI review' : `Edited by ${v.created_by ?? 'an editor'}`}
                <span className="small faint"> · {dateTime(v.created_at)}</span>
              </span>
              {v.version !== q.version && (
                <button className="btn link small" onClick={() => onLoad(obj(v.content))}>
                  Load into the editor
                </button>
              )}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

// ---- Moving through a test ----

function PrevNext({ test, id }: { test: string; id: string }) {
  const t = usePracticeTest(test);
  const slots = t.data?.slots ?? [];
  const i = slots.findIndex((s) => s.id === id);
  if (i < 0) return null;
  const link = (j: number) => `/practice/questions/${slots[j]!.id}?test=${encodeURIComponent(test)}`;
  return (
    <span className="toolbar">
      {i > 0 ? (
        <Link className="btn secondary small" to={link(i - 1)}>
          ‹ Previous
        </Link>
      ) : null}
      <span className="small faint tabular">
        {i + 1}/{slots.length}
      </span>
      {i < slots.length - 1 ? (
        <Link className="btn secondary small" to={link(i + 1)}>
          Next ›
        </Link>
      ) : null}
    </span>
  );
}
