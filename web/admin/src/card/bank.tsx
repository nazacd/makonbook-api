import katex from 'katex';
import { Fragment, type ReactNode } from 'react';

// Draws the bank's own text, as SAT MAKON typed it: plain text with line
// breaks, $…$ math in Math questions, and a few HTML tags in R&W (<em>,
// <strong>, <u>…). Everything is escaped except those tags and the math.

const TAGS: Record<string, 'i' | 'b' | 'u' | 'sup' | 'sub'> = { em: 'i', i: 'i', strong: 'b', b: 'b', u: 'u', sup: 'sup', sub: 'sub' };

/** Splits text at $…$ math. A $ opens math only when a non-space follows it
 * and the next $ closes it with a non-space before it, so money ("costs $29
 * and $30") stays text. */
export function splitDollars(text: string): { math: boolean; text: string }[] {
  const out: { math: boolean; text: string }[] = [];
  let plain = '';
  let i = 0;
  while (i < text.length) {
    const open = text.indexOf('$', i);
    if (open < 0) break;
    const close = text.indexOf('$', open + 1);
    const ok =
      close > open + 1 &&
      !/\s/.test(text[open + 1]!) &&
      !/\s/.test(text[close - 1]!) &&
      !text.slice(open + 1, close).includes('\n') &&
      !/^\d[\d,]*(\.\d+)?$/.test(text.slice(open + 1, close).trim()); // "$5$" is still money-like text
    if (!ok) {
      plain += text.slice(i, open + 1);
      i = open + 1;
      continue;
    }
    plain += text.slice(i, open);
    if (plain) out.push({ math: false, text: plain });
    plain = '';
    out.push({ math: true, text: text.slice(open + 1, close) });
    i = close + 1;
  }
  plain += text.slice(i);
  if (plain) out.push({ math: false, text: plain });
  return out;
}

function math(tex: string, key: number): ReactNode {
  const html = katex.renderToString(tex, { output: 'html', throwOnError: false, trust: false, strict: false });
  return <span key={key} className="math" dangerouslySetInnerHTML={{ __html: html }} />;
}

// One line or paragraph: tags open and close elements; text and math fill them.
function line(src: string, mathOn: boolean): ReactNode[] {
  type Frame = { tag: string; children: ReactNode[] };
  const stack: Frame[] = [{ tag: '', children: [] }];
  let k = 0;
  const add = (n: ReactNode) => stack[stack.length - 1]!.children.push(n);
  const flushText = (t: string) => {
    for (const part of mathOn ? splitDollars(t) : [{ math: false, text: t }]) {
      add(part.math ? math(part.text, k++) : <Fragment key={k++}>{part.text}</Fragment>);
    }
  };
  for (const piece of src.split(/(<\/?[a-zA-Z]+\s*\/?>)/)) {
    const m = /^<(\/?)([a-zA-Z]+)\s*\/?>$/.exec(piece);
    const tag = m ? m[2]!.toLowerCase() : '';
    if (m && tag === 'br') {
      add(<br key={k++} />);
    } else if (m && TAGS[tag] && !m[1]) {
      stack.push({ tag, children: [] });
    } else if (m && TAGS[tag] && m[1] && stack.length > 1 && stack[stack.length - 1]!.tag === tag) {
      const f = stack.pop()!;
      const El = TAGS[tag]!;
      add(<El key={k++}>{f.children}</El>);
    } else if (piece) {
      flushText(piece);
    }
  }
  while (stack.length > 1) {
    const f = stack.pop()!; // an unclosed tag: keep its text
    add(<Fragment key={k++}>{f.children}</Fragment>);
  }
  return stack[0]!.children;
}

export function BankText({ text, math: mathOn }: { text: string | null | undefined; math: boolean }) {
  if (!text?.trim()) return null;
  return (
    <div className="rich">
      {text
        .trim()
        .split(/\n\s*\n/)
        .map((para, i) => (
          <p key={i}>
            {para.split('\n').map((l, j) => (
              <Fragment key={j}>
                {j > 0 && <br />}
                {line(l, mathOn)}
              </Fragment>
            ))}
          </p>
        ))}
    </div>
  );
}
