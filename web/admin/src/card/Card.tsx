import type { ReactNode } from 'react';

import logo from '../../public/logo.png';
import { markChanges } from '../practice/diff';
import { RichText } from '../practice/RichText';
import { BankText } from './bank';
import type { CardData, Content } from './types';

const LETTERS = ['A', 'B', 'C', 'D'] as const;

const LABEL: Record<CardData['kind'], { text: string; cls: string } | null> = {
  original: { text: 'Original', cls: 'label original' },
  fix: { text: 'AI fix', cls: 'label fix' },
  question: null,
};

/** One question as a picture, laid out like Bluebook: R&W with the passage
 * left and the question right, Math centred; the correct answer marked. */
export function Card({ data }: { data: CardData }) {
  const label = LABEL[data.kind];
  const body = data.kind === 'original' ? bankParts(data) : contentParts(data);
  const english = data.subject === 'english' && body.passage != null;
  return (
    <div id="card" className={`card ${english ? 'two' : 'one'}`}>
      <header>
        <div className="brand">
          <img src={logo} alt="" />
          <span>MakonBook</span>
        </div>
        <div className="where">
          <span>{data.place}</span>
          {label && <span className={label.cls}>{label.text}</span>}
        </div>
      </header>
      <main>
        {english ? (
          <>
            <section className="passage">{body.passage}</section>
            <div className="divider" />
            <section className="question">
              {body.stem}
              {body.answers}
            </section>
          </>
        ) : (
          <section className="question centred">
            {body.passage}
            {body.stem}
            {body.answers}
          </section>
        )}
      </main>
    </div>
  );
}

type Parts = { passage: ReactNode | null; stem: ReactNode; answers: ReactNode };

// The fixed version (changes highlighted) or a question from the editor.
function contentParts(data: CardData): Parts {
  const c = data.content!;
  const base: Content | undefined = data.kind === 'fix' ? data.base : undefined;
  const figure = (key: string) => data.figures[key] ?? '';
  const text = (now: string | null | undefined, was: string | null | undefined, choice = false) => {
    if (!now) return null;
    const changed = base != null && (was ?? '') !== now;
    return <RichText html displayStyle={choice} figure={figure} marked={changed} text={changed ? markChanges(was ?? '', now) : now} />;
  };
  const answers =
    data.type === 'mcq' ? (
      <Choices
        correct={c.correct}
        keyChanged={base != null && base.correct !== c.correct}
        render={(l) => text(c.choices?.[l], base?.choices?.[l], true)}
      />
    ) : (
      <GridIn answers={c.accepted ?? []} changed={base != null && (base.accepted ?? []).join(',') !== (c.accepted ?? []).join(',')} />
    );
  return { passage: text(c.passage, base?.passage), stem: text(c.stem, base?.stem), answers };
}

// The bank's own text, with its screenshots where they belong.
function bankParts(data: CardData): Parts {
  const b = data.bank!;
  const math = data.subject === 'math';
  const shot = (slot: string) => (b.images[slot] ? <img className="shot" src={b.images[slot]} alt="" /> : null);
  const answers =
    data.type === 'mcq' ? (
      <Choices
        correct={b.answer.trim().toUpperCase()}
        render={(l) => (
          <>
            <BankText text={b.choices?.[l]} math={math} />
            {shot(l)}
          </>
        )}
      />
    ) : (
      <GridIn answers={b.answer.split(',').map((a) => a.trim()).filter(Boolean)} />
    );
  return {
    passage: b.passage?.trim() ? <BankText text={b.passage} math={math} /> : null,
    stem: (
      <>
        <BankText text={b.question} math={math} />
        {shot('question')}
      </>
    ),
    answers,
  };
}

function Choices({ correct, keyChanged = false, render }: { correct: string | null; keyChanged?: boolean; render: (letter: string) => ReactNode }) {
  return (
    <ol className="choices">
      {LETTERS.map((l) => {
        const right = l === correct;
        return (
          <li key={l} className={right ? 'right' : undefined}>
            <span className={`letter${right && keyChanged ? ' changed' : ''}`}>{l}</span>
            <div className="text">{render(l)}</div>
            {right && (
              <svg className="check" viewBox="0 0 24 24" aria-label="correct">
                <path d="M4 12.5l5 5L20 6.5" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
              </svg>
            )}
          </li>
        );
      })}
    </ol>
  );
}

function GridIn({ answers, changed = false }: { answers: string[]; changed?: boolean }) {
  return (
    <div className="gridin">
      <div className="gridin-label">Correct answer{answers.length > 1 ? 's' : ''}</div>
      <div className={`gridin-box${changed ? ' changed' : ''}`}>{answers.join(', ') || '—'}</div>
    </div>
  );
}
