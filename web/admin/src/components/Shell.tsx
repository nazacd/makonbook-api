import { NavLink, Outlet } from 'react-router';

import { useMe, useSignOut } from '../api/hooks';
import { SignIn } from '../pages/SignIn';
import { ErrorNotice, Loading } from './ui';

// Signed out, every address shows the sign-in form; signed in, the panel.
export function Shell() {
  const me = useMe();
  const signOut = useSignOut();

  if (me.isPending) return <Loading />;
  if (me.isError) {
    return (
      <div className="signin">
        <ErrorNotice error={me.error} title="Couldn’t reach MakonBook" />
      </div>
    );
  }
  if (!me.data) return <SignIn />;

  const p = me.data;
  const scope = p.super ? 'Super admin · all branches' : p.offices.map((o) => o.name).join(', ');
  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="logo">
          <img src="/favicon.svg" alt="" />
          MakonBook
        </div>
        <nav className="nav" aria-label="Main">
          <NavLink to="/lessons">Lessons</NavLink>
          <NavLink to="/corrections">Teacher corrections</NavLink>
          <NavLink to="/writes">HolliHop writes</NavLink>
          <NavLink to="/data-quality">Data quality</NavLink>
          {p.super && <NavLink to="/access">Admins</NavLink>}
        </nav>
        <div className="sidebar-foot">
          <div>
            <div>{p.name}</div>
            <div className="small faint">{scope}</div>
          </div>
          <button className="btn secondary small" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
            Sign out
          </button>
        </div>
      </aside>
      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}
