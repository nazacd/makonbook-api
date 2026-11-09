import {
  BookOpenCheck,
  CalendarCheck,
  CircleAlert,
  Flag,
  ListChecks,
  LogOut,
  Moon,
  PenLine,
  Send,
  ShieldCheck,
  Sun,
  Users,
  type LucideIcon,
} from 'lucide-react';
import type { ReactNode } from 'react';
import { Navigate, NavLink, Outlet } from 'react-router';

import { useMe, useSignOut } from '../api/hooks';
import { useTheme } from '../lib/theme';
import { SignIn } from '../pages/SignIn';
import { Wordmark } from './brand/Logo';
import { ErrorNotice, Loading } from './ui';

// Signed out, every address shows the sign-in form; signed in, the panel:
// a floating sidebar (sections by area, as the admin may see them) beside
// the page, as on sat.makonbook.uz.

function Item({ to, icon: I, children }: { to: string; icon: LucideIcon; children: ReactNode }) {
  return (
    <NavLink to={to}>
      <I size={18} strokeWidth={1.9} aria-hidden />
      <span>{children}</span>
    </NavLink>
  );
}

export function Shell() {
  const me = useMe();
  const signOut = useSignOut();
  const { dark, toggle } = useTheme();

  if (me.isPending) return <Loading />;
  if (me.isError) {
    return (
      <div className="signin">
        <ErrorNotice error={me.error} title="Couldn’t reach SATMAKON" />
      </div>
    );
  }
  if (!me.data) return <SignIn />;

  const p = me.data;
  const branches = p.super || p.offices.length > 0;
  const scope = p.super
    ? 'Super admin · all branches'
    : [p.offices.map((o) => o.name).join(', '), p.editor ? 'Practice editor' : ''].filter(Boolean).join(' · ');
  const initials = p.name
    .split(/\s+/)
    .map((w) => w[0])
    .join('')
    .slice(0, 2)
    .toUpperCase();
  return (
    <div className="shell">
      <aside className="sidebar">
        <Wordmark />
        <nav className="nav" aria-label="Main">
          {branches && (
            <>
              <div className="nav-label">Attendance</div>
              <Item to="/lessons" icon={CalendarCheck}>
                Lessons
              </Item>
              <Item to="/corrections" icon={PenLine}>
                Teacher corrections
              </Item>
              <div className="nav-label">HolliHop</div>
              <Item to="/writes" icon={Send}>
                HolliHop writes
              </Item>
              <Item to="/issues" icon={CircleAlert}>
                HolliHop issues
              </Item>
              <div className="nav-label">People</div>
              <Item to="/parents" icon={Users}>
                Parents
              </Item>
            </>
          )}
          <div className="nav-label">Practice</div>
          {p.editor && (
            <>
              <Item to="/practice" icon={BookOpenCheck}>
                Practice tests
              </Item>
              <Item to="/practice/queue" icon={ListChecks}>
                Questions to fix
              </Item>
            </>
          )}
          <Item to="/reports" icon={Flag}>
            Issue reports
          </Item>
          {p.super && (
            <>
              <div className="nav-label">Access</div>
              <Item to="/access" icon={ShieldCheck}>
                Admins
              </Item>
            </>
          )}
        </nav>
        <div className="sidebar-foot">
          <div className="who">
            <span className="who-avatar" aria-hidden>
              {initials}
            </span>
            <div className="who-text">
              <div>{p.name}</div>
              <div className="small faint">{scope}</div>
            </div>
          </div>
          <div className="foot-row">
            <button className="icon-btn" onClick={toggle} aria-label={dark ? 'Light mode' : 'Dark mode'} title={dark ? 'Light mode' : 'Dark mode'}>
              {dark ? <Sun size={17} /> : <Moon size={17} />}
            </button>
            <button className="btn secondary small" style={{ flex: 1 }} onClick={() => signOut.mutate()} disabled={signOut.isPending}>
              <LogOut size={15} aria-hidden /> Sign out
            </button>
          </div>
        </div>
      </aside>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}

/** The first page: lessons, or practice tests for an editor with no branches. */
export function Home() {
  const me = useMe().data;
  const editorOnly = me && !me.super && me.offices.length === 0 && me.editor;
  const reportsOnly = me && !me.super && me.offices.length === 0 && !me.editor;
  return <Navigate to={reportsOnly ? '/reports' : editorOnly ? '/practice' : '/lessons'} replace />;
}
