import { useId } from 'react';

import { SHIELD_INNER, SHIELD_LETTERS, SHIELD_LOWER, SHIELD_RING, SHIELD_VIEWBOX } from './shield';

// The SATMAKON shield and name, as makonbook-web draws them
// (app/src/components/brand/Logo.tsx; keep in step).

/** The shield alone, in the current text colour. */
export function ShieldMark({ size = 32 }: { size?: number }) {
  const clip = useId();
  return (
    <svg viewBox={SHIELD_VIEWBOX} height={size} width={(size * 1669) / 2000} aria-hidden>
      <defs>
        <clipPath id={clip}>
          <path d={SHIELD_INNER} />
        </clipPath>
      </defs>
      <g fill="currentColor">
        <path fillRule="evenodd" d={SHIELD_RING} />
        <path fillRule="evenodd" d={SHIELD_LETTERS} />
        <path fillRule="evenodd" clipPath={`url(#${clip})`} d={SHIELD_LOWER} />
      </g>
    </svg>
  );
}

/** The shield, the name and "Admin", as at the top of the sidebar. */
export function Wordmark() {
  return (
    <span className="logo">
      <ShieldMark size={34} />
      <span className="logo-name">
        <b>SATMAKON</b>
        <span>Admin panel</span>
      </span>
    </span>
  );
}
