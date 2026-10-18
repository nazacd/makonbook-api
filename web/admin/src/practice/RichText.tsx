import katex from 'katex';
import { Fragment, type ReactNode } from 'react';

import { INLINE } from './format';

// Renders the question content format (makonbook-questionbank's
// tools/prompts/format.md): paragraphs, \( \) and \[ \] math, *italic*,
// **bold**, <u>underline</u>, "- " lists, pipe tables, ![alt](r2:key)
// figures. Everything is built as React elements (so text is escaped), except
// math: KaTeX's MathML, which escapes its input. MathML needs no inline
// styles, so the panel's strict Content-Security-Policy stays as it is.

export function figureUrl(key: string): string {
  return `/v1/admin/practice/figures?key=${encodeURIComponent(key)}`;
}

function math(tex: string, display: boolean, key: number): ReactNode {
  const html = katex.renderToString(tex, { output: 'mathml', displayMode: display, throwOnError: false, trust: false, strict: false });
  return <span key={key} className={display ? 'math display' : 'math'} dangerouslySetInnerHTML={{ __html: html }} />;
}


function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  text.split(INLINE).forEach((part, i) => {
    if (!part) return;
    if (i % 2 === 0) {
      out.push(<Fragment key={i}>{part}</Fragment>);
    } else if (part === '\n') {
      out.push(<br key={i} />);
    } else if (part.startsWith('\\(')) {
      out.push(math(part.slice(2, -2), false, i));
    } else if (part.startsWith('\\[')) {
      out.push(math(part.slice(2, -2), true, i));
    } else if (part.startsWith('![')) {
      const m = /^!\[([^\]]*)\]\(([^)\s]*)\)$/.exec(part)!;
      const key = m[2]!.startsWith('r2:') ? m[2]!.slice(3) : '';
      out.push(key ? <img key={i} className="figure" src={figureUrl(key)} alt={m[1]} title={m[1]} loading="lazy" /> : <span key={i}>[{m[1]}]</span>);
    } else if (part.startsWith('***')) {
      out.push(
        <b key={i}>
          <i>{inline(part.slice(3, -3))}</i>
        </b>,
      );
    } else if (part.startsWith('**')) {
      out.push(<b key={i}>{inline(part.slice(2, -2))}</b>);
    } else if (part.startsWith('<u>')) {
      out.push(<u key={i}>{inline(part.slice(3, -4))}</u>);
    } else {
      out.push(<i key={i}>{inline(part.slice(1, -1))}</i>);
    }
  });
  return out;
}

function cells(line: string): string[] {
  return line.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
}

export function RichText({ text, className }: { text: string | null | undefined; className?: string }) {
  if (!text) return null;
  // Paragraphs are split by a blank line; each further blank line is an
  // empty line of space (as the editor writes it).
  const blocks = text.trim().split(/\n[ \t]*\n/);
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
                <tr>{head!.map((c, j) => <th key={j}>{inline(c)}</th>)}</tr>
              </thead>
              <tbody>
                {body.map((r, k) => (
                  <tr key={k}>{r.map((c, j) => <td key={j}>{inline(c)}</td>)}</tr>
                ))}
              </tbody>
            </table>
          );
        }
        if (lines.every((l) => l.startsWith('- '))) {
          return (
            <ul key={i}>
              {lines.map((l, j) => (
                <li key={j}>{inline(l.slice(2))}</li>
              ))}
            </ul>
          );
        }
        return <p key={i}>{inline(block)}</p>;
      })}
    </div>
  );
}
