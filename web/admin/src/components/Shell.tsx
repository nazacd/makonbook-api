import { Navigate, NavLink, Outlet } from "react-router";

import { useMe, useSignOut } from "../api/hooks";
import { SignIn } from "../pages/SignIn";
import { ErrorNotice, Loading } from "./ui";

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
  const branches = p.super || p.offices.length > 0;
  const scope = p.super
    ? "Super admin · all branches"
    : [
        p.offices.map((o) => o.name).join(", "),
        p.editor ? "Practice editor" : "",
      ]
        .filter(Boolean)
        .join(" · ");
  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="logo">
          <img src="/logo.png" alt="" />
          MakonBook
        </div>
        <nav className="nav" aria-label="Main">
          {branches && (
            <>
              <NavLink to="/lessons">Lessons</NavLink>
              <NavLink to="/corrections">Teacher corrections</NavLink>
              <NavLink to="/writes">HolliHop writes</NavLink>
              <NavLink to="/issues">HolliHop issues</NavLink>
              <NavLink to="/parents">Parents</NavLink>
            </>
          )}
          {p.editor && <NavLink to="/practice">Practice tests</NavLink>}
          {p.super && <NavLink to="/access">Admins</NavLink>}
        </nav>
        <div className="sidebar-foot">
          <div>
            <div>{p.name}</div>
            <div className="small faint">{scope}</div>
          </div>
          <button
            className="btn secondary small"
            onClick={() => signOut.mutate()}
            disabled={signOut.isPending}
          >
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

/** The first page: lessons, or practice tests for an editor with no branches. */
export function Home() {
  const me = useMe().data;
  const editorOnly = me && !me.super && me.offices.length === 0 && me.editor;
  return <Navigate to={editorOnly ? "/practice" : "/lessons"} replace />;
}
