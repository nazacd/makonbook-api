// Word-by-word comparison of two texts in the content format: the editor
// shows what an AI fix changes, and the Telegram pictures highlight it.

export type Seg = { kind: 'same' | 'del' | 'ins'; text: string };

/** The longest common subsequence of two token lists, as segments: what
 * stays, what goes, what comes (removed before added). */
function lcsDiff(a: string[], b: string[]): Seg[] {
  const n = a.length;
  const m = b.length;
  const lcs = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--)
    for (let j = m - 1; j >= 0; j--) lcs[i]![j] = a[i] === b[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!);
  const out: Seg[] = [];
  const push = (kind: Seg['kind'], text: string) => {
    const last = out[out.length - 1];
    if (last?.kind === kind) last.text += text;
    else out.push({ kind, text });
  };
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && a[i] === b[j]) {
      push('same', a[i++]!);
      j++;
    } else if (i < n && (j >= m || lcs[i + 1]![j]! >= lcs[i]![j + 1]!)) push('del', a[i++]!);
    else push('ins', b[j++]!);
  }
  return out;
}

/** The changed words, for reading: long runs that stay are cut to a few
 * words either side. */
export function diffWords(before: string, after: string): Seg[] {
  const out = lcsDiff(before.split(/(\s+)/), after.split(/(\s+)/));
  return out.map((sg, k) => {
    if (sg.kind !== 'same') return sg;
    const words = sg.text.split(/(\s+)/);
    if (words.length <= 13) return sg;
    const head = k > 0 ? words.slice(0, 6).join('') : '';
    const tail = k < out.length - 1 ? words.slice(-6).join('') : '';
    return { kind: 'same', text: `${head.trimEnd()}${head ? ' ' : ''}…${tail ? ' ' : ''}${tail.trimStart()}` };
  });
}

// Marks around changed words: private-use characters RichText shows as a
// highlight (see its `marked` option). They never go into stored text.
export const MARK_OPEN = '\uE000';
export const MARK_CLOSE = '\uE001';

// Tokens that keep the format whole: a formula or an image is one token,
// punctuation is apart from its word (so "y." becoming "\(y\)." marks only
// the y), and table pipes, line breaks and a list's "- " are never marked.
const TOKENS = /(\\\([\s\S]*?\\\)|\\\[[\s\S]*?\\\]|!\[[^\]]*\]\([^)\s]*\)|\||\s+|[.,;:!?])/;

/** `after`, with every word, formula or image that isn't in `before`
 * between MARK_OPEN and MARK_CLOSE. */
export function markChanges(before: string, after: string): string {
  const tokens = (t: string) => t.split(TOKENS).filter(Boolean);
  let out = '';
  for (const sg of lcsDiff(tokens(before), tokens(after))) {
    if (sg.kind === 'del') continue;
    if (sg.kind === 'same') {
      out += sg.text;
      continue;
    }
    for (const tok of tokens(sg.text)) {
      const lineStart = out === '' || /\n[ \t]*$/.test(out);
      const plain = /^\s+$/.test(tok) || tok === '|' || (lineStart && tok === '-') || /^[.,;:!?]$/.test(tok);
      out += plain ? tok : MARK_OPEN + tok + MARK_CLOSE;
    }
  }
  return out;
}
