import { useState, type FormEvent } from 'react';

import { ApiError, describe } from '../api/client';
import { useSignIn } from '../api/hooks';

function message(error: unknown): string {
  if (!(error instanceof ApiError)) return describe(error);
  switch (error.code) {
    case 'invalid_credentials':
      return 'Wrong HolliHop login or password.';
    case 'not_admin':
      return 'This HolliHop account can’t use the admin panel. Ask a super admin to give you access.';
    case 'rate_limited':
      return 'Too many attempts. Wait 15 minutes and try again.';
    case 'unavailable':
      return 'HolliHop isn’t answering, so your password can’t be checked. Try again in a minute.';
    default:
      return describe(error);
  }
}

export function SignIn() {
  const [login, setLogin] = useState('');
  const [password, setPassword] = useState('');
  const signIn = useSignIn();

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!login.trim() || !password) return;
    signIn.mutate({ login: login.trim(), password });
  }

  return (
    <div className="signin">
      <form onSubmit={submit} noValidate>
        <div className="signin-head">
          <img src="/logo.png" alt="" />
          <h1>MakonBook Admin</h1>
          <p className="muted" style={{ margin: 0 }}>
            Sign in with your HolliHop login and password.
          </p>
        </div>
        <div className="card">
          <div className="field">
            <label htmlFor="login">HolliHop login</label>
            <input
              id="login"
              className="input"
              autoComplete="username"
              autoFocus
              value={login}
              onChange={(e) => setLogin(e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="password">Password</label>
            <input
              id="password"
              className="input"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          {signIn.isError && (
            <div className="field-error" role="alert">
              {message(signIn.error)}
            </div>
          )}
          <button className="btn" type="submit" disabled={signIn.isPending || !login.trim() || !password}>
            {signIn.isPending ? 'Checking with HolliHop…' : 'Sign in'}
          </button>
        </div>
        <p className="small faint" style={{ textAlign: 'center', margin: 0 }}>
          MakonBook doesn’t store your HolliHop password.
        </p>
      </form>
    </div>
  );
}
