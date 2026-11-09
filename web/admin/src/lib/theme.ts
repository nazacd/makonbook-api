import { useEffect, useState } from 'react';

// Light or dark: the computer's setting until the sidebar's toggle chooses,
// kept in this browser (the web app's appearance setting works the same way).

type Mode = 'light' | 'dark' | 'system';
const KEY = 'mb.admin.theme';

function read(): Mode {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'light' || v === 'dark' ? v : 'system';
  } catch {
    return 'system';
  }
}

export function useTheme() {
  const [mode, setMode] = useState<Mode>(read);
  const [systemDark, setSystemDark] = useState(() => window.matchMedia('(prefers-color-scheme: dark)').matches);
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const on = () => setSystemDark(mq.matches);
    mq.addEventListener('change', on);
    return () => mq.removeEventListener('change', on);
  }, []);
  useEffect(() => {
    if (mode === 'system') document.documentElement.removeAttribute('data-theme');
    else document.documentElement.setAttribute('data-theme', mode);
  }, [mode]);
  const dark = mode === 'dark' || (mode === 'system' && systemDark);
  const toggle = () => {
    const next: Mode = dark ? 'light' : 'dark';
    setMode(next);
    try {
      localStorage.setItem(KEY, next);
    } catch {
      // Not kept: the computer's setting next time.
    }
  };
  return { dark, toggle };
}
