import { useDeferredValue, useState } from 'react';

import { ApiError, describe } from '../api/client';
import { useAccess, useGrant, useMe, useRevoke, useStaffSearch, type Access } from '../api/hooks';
import { Empty, ErrorNotice, Loading, Notice } from '../components/ui';
import { dateTime } from '../lib/dates';

type Member = { type: 'employee' | 'teacher'; id: number; name: string };

const memberOf = (a: Access): Member => ({ type: a.member_type === 'Teacher' ? 'teacher' : 'employee', id: a.member_id, name: a.name });

function grantError(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'super_admin_protected':
        return 'Super admins can’t be changed or removed in the panel; that’s done on the server.';
      case 'fired':
        return 'HolliHop lists this person as fired.';
      case 'not_found':
        return 'HolliHop (or, for a teacher, MakonBook) has no one with this id.';
    }
  }
  return describe(error);
}

// Who can use the admin panel, and for which branches. Super admins only.
export function AccessPage() {
  const me = useMe().data;
  const access = useAccess(!!me?.super);
  const revoke = useRevoke();
  const [editing, setEditing] = useState<Member | 'new' | null>(null);

  if (!me?.super) {
    return <Notice tone="warning" title="Super admins only">Ask a super admin to change who can use the panel.</Notice>;
  }

  function remove(a: Access) {
    if (window.confirm(`Remove ${a.name || 'this admin'}’s access? They’re signed out at once.`)) {
      revoke.mutate(memberOf(a));
    }
  }

  const current = editing && editing !== 'new' ? access.data?.access.find((a) => memberOf(a).type === editing.type && a.member_id === editing.id) : undefined;

  return (
    <div className="stack">
      <div className="page-head">
        <div>
          <h1>Admins</h1>
          <p className="muted">
            They sign in with their own HolliHop login. Branch admins see only their branches. Super admins are added and
            removed on the server, not here.
          </p>
        </div>
        {editing === null && (
          <button className="btn" onClick={() => setEditing('new')}>
            Add branch admin
          </button>
        )}
      </div>

      {editing !== null && (
        <AccessForm
          key={editing === 'new' ? 'new' : `${editing.type}-${editing.id}`}
          member={editing === 'new' ? null : editing}
          initial={current}
          offices={me.offices}
          onDone={() => setEditing(null)}
        />
      )}

      {revoke.isError && (
        <Notice tone="error" title="Couldn’t remove access">
          {grantError(revoke.error)}
        </Notice>
      )}

      {access.isPending ? (
        <Loading />
      ) : access.isError ? (
        <ErrorNotice error={access.error} />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Access</th>
                <th>Last sign-in</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {access.data.access.map((a) => {
                const self = a.member_type === me.member_type && a.member_id === me.member_id;
                return (
                  <tr key={`${a.member_type}-${a.member_id}`}>
                    <td>
                      {a.name || <span className="faint">Not signed in yet</span>}
                      {self && <span className="small faint"> (you)</span>}
                      <div className="small faint">
                        HolliHop {a.member_type.toLowerCase()} {a.member_id}
                      </div>
                    </td>
                    <td>
                      {a.super ? (
                        <span className="chip accent">Super admin</span>
                      ) : (
                        <span className="toolbar">
                          {a.offices.map((o) => (
                            <span key={o.id} className="chip plain">
                              {o.name}
                            </span>
                          ))}
                        </span>
                      )}
                    </td>
                    <td className="small muted">{a.last_login_at ? dateTime(a.last_login_at) : 'Never'}</td>
                    <td className="nowrap" style={{ textAlign: 'right' }}>
                      {a.super ? (
                        <span className="small faint">Managed on the server</span>
                      ) : (
                        <span className="toolbar" style={{ justifyContent: 'flex-end' }}>
                          <button className="btn secondary small" onClick={() => setEditing(memberOf(a))}>
                            Change
                          </button>
                          <button className="btn danger small" disabled={revoke.isPending} onClick={() => remove(a)}>
                            Remove
                          </button>
                        </span>
                      )}
                    </td>
                  </tr>
                );
              })}
              {access.data.access.length === 0 && (
                <tr>
                  <td colSpan={4}>
                    <Empty>No admins.</Empty>
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function AccessForm({
  member,
  initial,
  offices,
  onDone,
}: {
  member: Member | null; // null: adding someone
  initial?: Access;
  offices: { id: number; name: string }[];
  onDone: () => void;
}) {
  const [picked, setPicked] = useState<Member | null>(member);
  const [officeIds, setOfficeIds] = useState<number[]>(initial?.offices.map((o) => o.id) ?? []);
  const grant = useGrant();

  const valid = picked !== null && officeIds.length > 0;
  const toggle = (id: number) => setOfficeIds((ids) => (ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id]));

  return (
    <form
      className="panel"
      onSubmit={(e) => {
        e.preventDefault();
        if (picked && valid) grant.mutate({ member: picked, officeIds }, { onSuccess: onDone });
      }}>
      <div className="card-head" style={{ margin: 0 }}>
        <h2>{member ? `Change ${member.name || 'access'}` : 'Add a branch admin'}</h2>
        <button type="button" className="btn link" onClick={onDone}>
          Cancel
        </button>
      </div>

      {!member && <PersonPicker picked={picked} onPick={setPicked} />}

      <div className="field">
        <label>Branches</label>
        <div className="checks">
          {offices.map((o) => (
            <label key={o.id} className="check">
              <input type="checkbox" checked={officeIds.includes(o.id)} onChange={() => toggle(o.id)} /> {o.name}
            </label>
          ))}
        </div>
        {officeIds.length === 0 && <span className="small faint">Pick at least one branch.</span>}
      </div>

      {grant.isError && <div className="field-error" role="alert">{grantError(grant.error)}</div>}

      <div className="toolbar">
        <button className="btn" type="submit" disabled={!valid || grant.isPending}>
          {grant.isPending ? 'Saving…' : 'Save'}
        </button>
      </div>
    </form>
  );
}

// Find the person in HolliHop's staff by name, or enter a teacher's id.
function PersonPicker({ picked, onPick }: { picked: Member | null; onPick: (m: Member | null) => void }) {
  const [mode, setMode] = useState<'employee' | 'teacher'>('employee');
  const [q, setQ] = useState('');
  const term = useDeferredValue(q.trim());
  const staff = useStaffSearch(term);
  const [teacherId, setTeacherId] = useState('');

  return (
    <div className="field">
      <label>Person</label>
      <div className="checks">
        <label className="check">
          <input
            type="radio"
            name="mode"
            checked={mode === 'employee'}
            onChange={() => {
              setMode('employee');
              onPick(null);
            }}
          />{' '}
          HolliHop staff
        </label>
        <label className="check">
          <input
            type="radio"
            name="mode"
            checked={mode === 'teacher'}
            onChange={() => {
              setMode('teacher');
              onPick(null);
            }}
          />{' '}
          A teacher (by HolliHop teacher id)
        </label>
      </div>

      {mode === 'employee' ? (
        <>
          <input className="input" placeholder="Search by name, phone or email" value={q} onChange={(e) => setQ(e.target.value)} />
          {term.length >= 2 &&
            (staff.isPending ? (
              <span className="small muted">Searching HolliHop…</span>
            ) : staff.isError ? (
              <span className="field-error">{describe(staff.error)}</span>
            ) : staff.data.staff.length === 0 ? (
              <span className="small muted">No one found.</span>
            ) : (
              <div className="results" role="listbox" aria-label="Staff">
                {staff.data.staff.map((s) => (
                  <button
                    key={s.id}
                    type="button"
                    role="option"
                    aria-selected={picked?.id === s.id}
                    className={`result ${picked?.type === 'employee' && picked.id === s.id ? 'selected' : ''}`}
                    disabled={s.fired}
                    onClick={() => onPick({ type: 'employee', id: s.id, name: s.name })}>
                    <span>
                      {s.name}
                      <div className="small faint">
                        {[s.position, s.offices.join(', ')].filter(Boolean).join(' · ')}
                      </div>
                    </span>
                    {s.fired ? <span className="chip plain">Fired</span> : picked?.id === s.id && <span className="chip accent">Selected</span>}
                  </button>
                ))}
              </div>
            ))}
        </>
      ) : (
        <input
          className="input"
          inputMode="numeric"
          placeholder="Teacher id, e.g. 1043"
          value={teacherId}
          onChange={(e) => {
            const v = e.target.value.replace(/\D/g, '');
            setTeacherId(v);
            onPick(v ? { type: 'teacher', id: Number(v), name: '' } : null);
          }}
        />
      )}
    </div>
  );
}
