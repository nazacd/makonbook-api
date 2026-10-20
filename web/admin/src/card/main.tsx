import 'katex/dist/katex.min.css';
import './card.css';

import interLatinItalic from '@fontsource-variable/inter/files/inter-latin-wght-italic.woff2';
import interLatin from '@fontsource-variable/inter/files/inter-latin-wght-normal.woff2';
import interExtItalic from '@fontsource-variable/inter/files/inter-latin-ext-wght-italic.woff2';
import interExt from '@fontsource-variable/inter/files/inter-latin-ext-wght-normal.woff2';
import { flushSync } from 'react-dom';
import { createRoot } from 'react-dom/client';

import { Card } from './Card';
import type { CardData } from './types';

// The question picture page. The API opens it in a headless browser, calls
// renderCard with the question, and takes a picture of #card. Everything it
// needs is inside the page (fonts, logo, images as data URLs): it loads
// nothing, so the browser needs no network.

const LATIN = 'U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD';
const LATIN_EXT =
  'U+0100-02BA,U+02BD-02C5,U+02C7-02CC,U+02CE-02D7,U+02DD-02FF,U+0304,U+0308,U+0329,U+1D00-1DBF,U+1E00-1E9F,U+1EF2-1EFF,U+2020,U+20A0-20AB,U+20AD-20C0,U+2113,U+2C60-2C7F,U+A720-A7FF';
for (const [url, style, unicodeRange] of [
  [interLatin, 'normal', LATIN],
  [interLatinItalic, 'italic', LATIN],
  [interExt, 'normal', LATIN_EXT],
  [interExtItalic, 'italic', LATIN_EXT],
] as const) {
  document.fonts.add(new FontFace('Inter', `url(${url})`, { weight: '100 900', style, unicodeRange }));
}

const root = createRoot(document.getElementById('root')!);

declare global {
  interface Window {
    renderCard: (data: CardData) => Promise<{ width: number; height: number }>;
  }
}

/** Draws a question and resolves with the picture's size once its fonts and
 * images are ready. */
window.renderCard = async (data) => {
  flushSync(() => root.render(<Card data={data} />));
  await Promise.all([...document.fonts].map((f) => f.load().catch(() => undefined)));
  await document.fonts.ready;
  await Promise.all([...document.images].map((img) => img.decode().catch(() => undefined)));
  const r = document.getElementById('card')!.getBoundingClientRect();
  return { width: Math.ceil(r.width), height: Math.ceil(r.height) };
};
