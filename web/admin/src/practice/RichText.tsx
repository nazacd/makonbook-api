import katex from 'katex';
import { Fragment, type ReactNode } from 'react';

import { MARK_CLOSE, MARK_OPEN } from './diff';
import { INLINE } from './format';

// Renders the question content format (makonbook-questionbank's
// tools/prompts/format.md): paragraphs, \( \) and \[ \] math, *italic*,
// **bold**, <u>underline</u>, "- " lists, pipe tables, ![alt](r2:key)
// figures. Everything is built as React elements (so text is escaped), except
// math: KaTeX's MathML, which escapes its input. MathML needs no inline
// styles, so the panel's strict Content-Security-Policy stays as it is. The
// question pictures (../card) ask for KaTeX's HTML instead, which draws with
// its own fonts, give the images, and highlight changes (diff.ts's marks).

export function figureUrl(key: string): string {
  return `/v1/admin/practice/figures?key=${encodeURIComponent(key)}`;
}

// How a text is drawn: math as MathML (the panel) or KaTeX HTML (pictures),
// where images come from, and whether changes are highlighted. `mark` is
// whether a highlight is open, carried along as the text is drawn in order.
type Opts = { html: boolean; display: boolean; figure: (key: string) => string; lazy: boolean; mark: { open: boolean } | null };

function math(tex: string, display: boolean, key: number, o: Opts): ReactNode {
  if (o.display && !display) tex = `\\displaystyle ${tex}`; // full-size fractions, as in answer choices
  const html = katex.renderToString(tex, { output: o.html ? 'html' : 'mathml', displayMode: display, throwOnError: false, trust: false, strict: false });
  return <span key={key} className={display ? 'math display' : 'math'} dangerouslySetInnerHTML={{ __html: html }} />;
}

// highlighted wraps what is drawn while a highlight is open.
const highlighted = (node: ReactNode, key: number | string, o: Opts) => (o.mark?.open ? <mark key={key}>{node}</mark> : node);

// text draws plain text, opening and closing highlights at the marks.
function text(part: string, key: number, o: Opts): ReactNode {
  if (!o.mark) return <Fragment key={key}>{part}</Fragment>;
  const mark = o.mark;
  return (
    <Fragment key={key}>
      {part.split(/([\uE000\uE001])/).map((t, j) => {
        if (t === MARK_OPEN) mark.open = true;
        else if (t === MARK_CLOSE) mark.open = false;
        else if (t) return highlighted(t, j, o);
        return null;
      })}
    </Fragment>
  );
}

function inline(src: string, o: Opts): ReactNode[] {
  const out: ReactNode[] = [];
  src.split(INLINE).forEach((part, i) => {
    if (!part) return;
    if (i % 2 === 0) {
      out.push(text(part, i, o));
    } else if (part === '\n') {
      out.push(<br key={i} />);
    } else if (part.startsWith('\\(')) {
      out.push(highlighted(math(part.slice(2, -2), false, i, o), i, o));
    } else if (part.startsWith('\\[')) {
      out.push(highlighted(math(part.slice(2, -2), true, i, o), i, o));
    } else if (part.startsWith('![')) {
      const m = /^!\[([^\]]*)\]\(([^)\s]*)\)$/.exec(part)!;
      const key = m[2]!.startsWith('r2:') ? m[2]!.slice(3) : '';
      const img = key ? (
        <img key={i} className="figure" src={o.figure(key)} alt={m[1]} title={m[1]} loading={o.lazy ? 'lazy' : undefined} />
      ) : (
        <span key={i}>[{m[1]}]</span>
      );
      out.push(highlighted(img, i, o));
    } else if (part.startsWith('***')) {
      out.push(
        <b key={i}>
          <i>{inline(part.slice(3, -3), o)}</i>
        </b>,
      );
    } else if (part.startsWith('**')) {
      out.push(<b key={i}>{inline(part.slice(2, -2), o)}</b>);
    } else if (part.startsWith('<u>')) {
      out.push(<u key={i}>{inline(part.slice(3, -4), o)}</u>);
    } else {
      out.push(<i key={i}>{inline(part.slice(1, -1), o)}</i>);
    }
  });
  return out;
}

function cells(line: string): string[] {
  return line.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
}

export function RichText({
  text: src,
  className,
  html = false,
  figure = figureUrl,
  marked = false,
  displayStyle = false,
}: {
  text: string | null | undefined;
  className?: string;
  html?: boolean; // KaTeX's HTML for math (pictures), not MathML
  figure?: (key: string) => string; // an image's address from its key
  marked?: boolean; // highlight what's between diff.ts's marks
  displayStyle?: boolean; // inline math at full size (answer choices)
}) {
  if (!src) return null;
  const o: Opts = { html, display: displayStyle, figure, lazy: !html, mark: marked ? { open: false } : null };
  const inl = (t: string) => inline(t, o);
  // Paragraphs are split by a blank line; each further blank line is an
  // empty line of space (as the editor writes it).
  const blocks = src.trim().split(/\n[ \t]*\n/);
  return (
    <div className={`rich ${className ?? ''}`}>
      {blocks.map((block, i) => {
        if (block.trim() === '') return <p key={i} className="blank" aria-hidden="true"><br /></p>;
        const lines = block.split('\n');
        if (lines.length > 1 && lines.every((l) => l.trim().startsWith('|'))) {
          const rows = lines.filter((l) => !/^\s*\|?\s*:?-{2,}/.test(l)).map(cells);
          const [head, ...body] = rows;
          return (
            <table key={i} className="rich-table">
              <thead>
                <tr>{head!.map((c, j) => <th key={j}>{inl(c)}</th>)}</tr>
              </thead>
              <tbody>
                {body.map((r, k) => (
                  <tr key={k}>{r.map((c, j) => <td key={j}>{inl(c)}</td>)}</tr>
                ))}
              </tbody>
            </table>
          );
        }
        if (lines.every((l) => l.startsWith('- '))) {
          return (
            <ul key={i}>
              {lines.map((l, j) => (
                <li key={j}>{inl(l.slice(2))}</li>
              ))}
            </ul>
          );
        }
        return <p key={i}>{inl(block)}</p>;
      })}
    </div>
  );
}
