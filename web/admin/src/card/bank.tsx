import { RichText } from '../practice/RichText';

// The bank's own text, as SAT MAKON typed it, is close to our content format
// (../practice/format.ts): \( \) and \[ \] math, **bold**, <u>, "- " lists.
// It differs in a few ways, turned here so it draws exactly like our own
// text: <em>/<strong>, \$ for a dollar sign ($ itself is money, never math),
// a lone * as a times sign in Math, lists right under their first line, and
// in Reading and Writing, plain words set as a formula only to style them
// (\( \textit{Night and Day} \), \( \underline{an\ ill-balanced\ axe} \)).

const MATH = /(\\\([\s\S]*?\\\)|\\\[[\s\S]*?\\\])/;

const STYLES: Record<string, [string, string]> = {
  textit: ['*', '*'],
  emph: ['*', '*'],
  textbf: ['**', '**'],
  underline: ['<u>', '</u>'],
  text: ['', ''],
};
const ACCENTS: Record<string, string> = { "'": '\u0301', '`': '\u0300', '"': '\u0308', '~': '\u0303', '^': '\u0302' };

/** A formula that only styles words, as those words in the content format;
 * null for real math (or anything else), which stays a formula. */
function styledWords(formula: string): string | null {
  let t = formula.slice(2, -2);
  t = t.replace(/\\(['`"~^])\{?([A-Za-z])\}?/g, (_, a: string, c: string) => (c + ACCENTS[a]).normalize('NFC'));
  t = t.replace(/\{,\}/g, ',').replace(/\\[ ,;]/g, ' ');
  for (;;) {
    const next = t.replace(/\\(textit|emph|textbf|underline|text)\{([^{}]*)\}/g, (_, cmd: string, body: string) => {
      const [open, close] = STYLES[cmd] ?? ['', ''];
      const inner = body.trim();
      return inner ? open + inner + close : '';
    });
    if (next === t) break;
    t = next;
  }
  t = t.replace(/\{([^{}]*)\}/g, '$1').trim();
  if (!t || /[\\^_{}]/.test(t)) return null;
  return t;
}

/** The bank's text in the content format. */
export function bankToContent(text: string | null | undefined, math: boolean): string {
  if (!text) return '';
  const converted = text
    .replace(/\r\n?/g, '\n')
    .split(MATH)
    .map((part, i) => {
      if (i % 2 === 1) return math ? part : (styledWords(part) ?? part);
      let t = part
        .replace(/\\\$/g, '$')
        .replace(/<br\s*\/?>/gi, '\n')
        .replace(/<(em|i)>([\s\S]*?)<\/\1>/gi, '*$2*')
        .replace(/<(strong|b)>([\s\S]*?)<\/\1>/gi, '**$2**');
      if (math) t = t.replace(/(?<!\*)\*(?!\*)/g, '∗'); // 1/2 * a: a times sign, not italics
      return t;
    })
    .join('');
  // A list starts a block of its own (format.ts reads a block whose lines all
  // start with "- " as a list).
  const lines = converted.split('\n');
  const out: string[] = [];
  lines.forEach((line, i) => {
    const item = line.startsWith('- ');
    const prev = lines[i - 1];
    if (i > 0 && prev !== undefined && prev.trim() !== '' && item !== prev.startsWith('- ')) out.push('');
    out.push(line);
  });
  return out.join('\n');
}

export function BankText({ text, math, choice = false }: { text: string | null | undefined; math: boolean; choice?: boolean }) {
  const content = bankToContent(text, math);
  return content.trim() ? <RichText html displayStyle={choice} text={content} /> : null;
}
