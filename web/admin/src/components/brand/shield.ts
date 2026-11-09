// The SATMAKON shield (SAT, the stripes and the star), redrawn from the logo
// as paths in one colour, so it takes any colour (currentColor) on any
// background. viewBox 0 0 1669 2000.
export const SHIELD_VIEWBOX = '0 0 1669 2000';

// The outer ring: the shield's outline minus its inside.
export const SHIELD_RING =
  'M58 0H1611Q1669 0 1669 58V1170C1669 1640 1295 2000 835 2000C375 2000 0 1640 0 1170V58Q0 0 58 0Z' +
  'M65 65V1170C65 1600 410 1935 835 1935C1260 1935 1604 1600 1604 1170V65Z';

export const SHIELD_LETTERS =
  // S
  'M140 125H534V270H392V225H285V520H534V899L140 1082V871H290V903L392 857V621H140Z' +
  // A, with its counter
  'M637 125H1032V667L886 735V481H783V783L637 851Z M783 225V380H886V225Z' +
  // T
  'M1135 125H1529V225H1383V503L1280 551V225H1135Z';

// Below the diagonal: the panel with the star cut out, and three stripes,
// clipped to the shield's inner curve (SHIELD_INNER).
export const SHIELD_LOWER =
  'M140 1193L783 894V2000H140Z' +
  'M509 1185L466 1321L322 1320L439 1403L394 1538L509 1453L624 1538L578 1403L696 1320L552 1321Z' +
  'M886 845L1032 777V2000H886Z' +
  'M1135 730L1280 662V2000H1135Z' +
  'M1383 613L1529 546V2000H1383Z';

export const SHIELD_INNER = 'M140 0H1529V1170C1529 1555 1220 1860 835 1860C450 1860 140 1555 140 1170Z';

/** The whole mark as a standalone SVG document (favicon, social images). */
export function shieldSvg(fill: string, background?: string): string {
  const bg = background ? `<rect x="-300" y="-150" width="2269" height="2300" rx="420" fill="${background}"/>` : '';
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${background ? '-300 -150 2269 2300' : SHIELD_VIEWBOX}">${bg}<defs><clipPath id="i"><path d="${SHIELD_INNER}"/></clipPath></defs><g fill="${fill}"><path fill-rule="evenodd" d="${SHIELD_RING}"/><path fill-rule="evenodd" d="${SHIELD_LETTERS}"/><path fill-rule="evenodd" clip-path="url(#i)" d="${SHIELD_LOWER}"/></g></svg>`;
}
