// Converts between the stored content format (makonbook-questionbank's
// tools/prompts/format.md) and the visual editor's document (TipTap JSON):
//
//   paragraphs (blank line between; each further blank line is an empty
//   line), "\n" line breaks, \( \) and \[ \] math,
//   *italic*, **bold**, ***bold italic***, <u>underline</u>, "- " lists,
//   pipe tables (header, |---|, rows), ![description](r2:key) figures.
//
// Teachers only ever see the editor; the API, the checks and the apps keep
// reading the text format. No dependencies, so it can be tested on its own.

export type Mark = { type: 'bold' | 'italic' | 'underline' };
export type Node = {
  type: string;
  attrs?: Record<string, unknown>;
  content?: Node[];
  marks?: Mark[];
  text?: string;
};

// Italic may hold bold (*a **b** c*): its closing * is one not followed by another.
export const INLINE =
  /(\\\([\s\S]*?\\\)|\\\[[\s\S]*?\\\]|!\[[^\]]*\]\([^)\s]*\)|\*\*\*[\s\S]+?\*\*\*|\*\*[\s\S]+?\*\*|<u>[\s\S]*?<\/u>|\*(?![\s*])(?:[^*]|\*\*[^*]+?\*\*)*?\*(?!\*)|\n)/;
const IMAGE = /^!\[([^\]]*)\]\(r2:([^)\s]*)\)$/;
const IMAGE_ANYWHERE = /!\[[^\]]*\]\(r2:[^)\s]*\)/;

// ---- text → document ----

function inline(text: string, marks: Mark[] = []): Node[] {
  const out: Node[] = [];
  const withMarks = (n: Node): Node => (marks.length ? { ...n, marks } : n);
  const add = (...more: Mark[]) => [...marks, ...more.filter((m) => !marks.some((x) => x.type === m.type))];
  text.split(INLINE).forEach((part, i) => {
    if (!part) return;
    if (i % 2 === 0) out.push(withMarks({ type: 'text', text: part }));
    else if (part === '\n') out.push({ type: 'hardBreak' });
    else if (part.startsWith('\\(')) out.push(withMarks({ type: 'math', attrs: { latex: part.slice(2, -2), display: false } }));
    else if (part.startsWith('\\[')) out.push(withMarks({ type: 'math', attrs: { latex: part.slice(2, -2), display: true } }));
    else if (part.startsWith('![')) out.push(withMarks({ type: 'text', text: part })); // an image inside a line: kept as written
    else if (part.startsWith('***')) out.push(...inline(part.slice(3, -3), add({ type: 'bold' }, { type: 'italic' })));
    else if (part.startsWith('**')) out.push(...inline(part.slice(2, -2), add({ type: 'bold' })));
    else if (part.startsWith('<u>')) out.push(...inline(part.slice(3, -4), add({ type: 'underline' })));
    else out.push(...inline(part.slice(1, -1), add({ type: 'italic' })));
  });
  return out;
}

function paragraph(text: string): Node {
  const content = inline(text);
  return content.length ? { type: 'paragraph', content } : { type: 'paragraph' };
}

function cells(line: string): string[] {
  return line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim());
}

/** Parses stored text into the editor's document. */
export function parse(text: string | null | undefined): Node {
  const blocks: Node[] = [];
  // Paragraphs are split by a blank line; each further blank line is an
  // empty line of space.
  for (const block of (text ?? '').trim().split(/\n[ \t]*\n/)) {
    if (block.trim() === '') {
      blocks.push({ type: 'paragraph' });
      continue;
    }
    const lines = block.split('\n');
    if (lines.length > 1 && lines.every((l) => l.trim().startsWith('|'))) {
      const rows = lines.filter((l) => !/^\s*\|?\s*:?-{2,}/.test(l)).map(cells);
      const width = Math.max(...rows.map((r) => r.length));
      blocks.push({
        type: 'table',
        content: rows.map((r, i) => ({
          type: 'tableRow',
          content: Array.from({ length: width }, (_, j) => ({
            type: i === 0 ? 'tableHeader' : 'tableCell',
            content: [paragraph(r[j] ?? '')],
          })),
        })),
      });
      continue;
    }
    if (lines.every((l) => l.startsWith('- '))) {
      blocks.push({ type: 'bulletList', content: lines.map((l) => ({ type: 'listItem', content: [paragraph(l.slice(2))] })) });
      continue;
    }
    // Figures stand on their own line; the lines around them stay paragraphs.
    let run: string[] = [];
    const flush = () => {
      if (run.length) blocks.push(paragraph(run.join('\n')));
      run = [];
    };
    for (const line of lines) {
      const m = IMAGE.exec(line.trim());
      if (m) {
        flush();
        blocks.push({ type: 'figure', attrs: { alt: m[1], key: m[2] } });
      } else run.push(line);
    }
    flush();
  }
  return { type: 'doc', content: blocks.length ? blocks : [{ type: 'paragraph' }] };
}

// ---- document → text ----

const has = (n: Node, t: Mark['type']) => !!n.marks?.some((m) => m.type === t);

function leaf(n: Node): string {
  if (n.type === 'text') return n.text ?? '';
  if (n.type === 'hardBreak') return '\n';
  if (n.type === 'math') {
    const latex = String(n.attrs?.latex ?? '');
    if (!latex.trim()) return '';
    return n.attrs?.display ? `\\[${latex}\\]` : `\\(${latex}\\)`;
  }
  return '';
}

/** Wraps text in stars, keeping spaces outside (`* x*` wouldn't read back). */
function stars(s: string, bold: boolean, italic: boolean): string {
  const lead = /^\s*/.exec(s)![0];
  const trail = /\s*$/.exec(s.slice(lead.length))![0];
  const core = s.slice(lead.length, s.length - trail.length);
  if (!core || (!bold && !italic)) return s;
  const mark = bold && italic ? '***' : bold ? '**' : '*';
  return lead + mark + core + mark + trail;
}

/** Bold and italic as runs: neighbours with the same marks are one run (**ab**). */
function starRuns(nodes: Node[]): string {
  let out = '';
  let i = 0;
  while (i < nodes.length) {
    const b = has(nodes[i]!, 'bold');
    const it = has(nodes[i]!, 'italic');
    let j = i;
    let s = '';
    while (j < nodes.length && has(nodes[j]!, 'bold') === b && has(nodes[j]!, 'italic') === it) {
      // A line break can't sit inside stars; it ends the run.
      if (nodes[j]!.type === 'hardBreak' && (b || it)) break;
      s += leaf(nodes[j]!);
      j++;
    }
    if (j === i) {
      out += leaf(nodes[i]!);
      j = i + 1;
    } else out += stars(s, b, it);
    i = j;
  }
  return out;
}

function inlineText(nodes: Node[] = []): string {
  // Underline wraps whole stretches (spaces included), bold and italic go inside.
  let out = '';
  let i = 0;
  while (i < nodes.length) {
    const u = has(nodes[i]!, 'underline');
    let j = i;
    while (j < nodes.length && has(nodes[j]!, 'underline') === u && !(u && nodes[j]!.type === 'hardBreak')) j++;
    if (j === i) j = i + 1;
    const s = starRuns(nodes.slice(i, j));
    out += u && s.trim() ? `<u>${s}</u>` : s;
    i = j;
  }
  return out;
}

function blockText(n: Node): string {
  switch (n.type) {
    case 'paragraph':
      return inlineText(n.content);
    case 'figure':
      return `![${String(n.attrs?.alt ?? '').replace(/[\]\n]/g, ' ').trim()}](r2:${String(n.attrs?.key ?? '')})`;
    case 'bulletList':
      return (n.content ?? [])
        .map((li) => '- ' + (li.content ?? []).map((p) => (p.type === 'paragraph' ? inlineText(p.content) : blockText(p))).join(' ').replace(/\n/g, ' '))
        .join('\n');
    case 'table': {
      const rows = (n.content ?? []).map((row) =>
        (row.content ?? []).map((cell) =>
          (cell.content ?? []).map((p) => inlineText(p.content)).join(' ').replace(/\n/g, ' ').replace(/\|/g, '/').trim(),
        ),
      );
      if (rows.length === 0) return '';
      const line = (r: string[]) => `|${r.map((c) => (c ? ` ${c} ` : ' ')).join('|')}|`;
      return [line(rows[0]!), `|${rows[0]!.map(() => '---').join('|')}|`, ...rows.slice(1).map(line)].join('\n');
    }
    default:
      return (n.content ?? []).map(blockText).join('\n\n');
  }
}

/** Writes the editor's document back as stored text. */
export function serialize(doc: Node): string {
  // An empty paragraph between others is kept as an extra blank line.
  return (doc.content ?? [])
    .map((b) => (blockText(b).trim() === '' ? '' : blockText(b)))
    .join('\n\n')
    .trim();
}

/** The figures a text uses: description and key, in order. */
export function figuresIn(text: string): { key: string; alt: string }[] {
  const out: { key: string; alt: string }[] = [];
  for (const m of text.matchAll(/!\[([^\]]*)\]\(r2:([^)\s]*)\)/g)) out.push({ alt: m[1]!, key: m[2]! });
  return out;
}

/** Whether a text has an image inside a line of text (the editor keeps it as plain text). */
export function hasInlineImage(text: string): boolean {
  return text.split('\n').some((l) => IMAGE_ANYWHERE.test(l) && !IMAGE.test(l.trim()));
}
