import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { BrowserRouter, Link, Navigate, NavLink, Route, Routes } from "react-router-dom";
import { api, ApiError } from "./api";
import { Loading, Wordmark } from "./components";
import Dashboard from "./pages/Dashboard";
import AppDetail from "./pages/AppDetail";
import Profile from "./pages/Profile";
import Admin from "./pages/Admin";
import Docs from "./pages/Docs";

// The popped-out, full-window terminal (also reachable directly). xterm is heavy,
// so it stays a lazy chunk, loaded only when a terminal is actually opened.
const AppTerminal = lazy(() => import("./pages/AppTerminal"));

const logout = async () => {
  try {
    await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
  } finally {
    window.location.reload();
  }
};

const Login = () => (
  <div className="center-page">
    <div className="card gate-card">
      <Wordmark big />
      <p className="gate-pitch">
        Self-hosted mini-apps: each one gets its own container, SSH access, a subdomain and automatic TLS.
      </p>
      <a className="btn btn-primary btn-wide" href="/auth/google">
        Sign in with Google
      </a>
    </div>
  </div>
);

const Pending = ({ account }) => (
  <div className="center-page">
    <div className="card gate-card">
      <Wordmark big />
      <h1>Waiting for approval</h1>
      <p className="gate-pitch">
        Your account <strong>{account.email}</strong> has been created, but an admin needs to approve it before you can create
        apps. Check back later.
      </p>
      <button type="button" className="btn" onClick={logout}>
        Logout
      </button>
    </div>
  </div>
);

const Denied = ({ account }) => (
  <div className="center-page">
    <div className="card gate-card">
      <Wordmark big />
      <h1>Access denied</h1>
      <p className="gate-pitch">
        Access for <strong>{account.email}</strong> has been denied by an admin. If you think this is a mistake, contact the
        person running this hostit instance.
      </p>
      <button type="button" className="btn" onClick={logout}>
        Logout
      </button>
    </div>
  </div>
);

const LoadFailed = ({ message, onRetry }) => (
  <div className="center-page">
    <div className="card gate-card">
      <Wordmark big />
      <h1>Something went wrong</h1>
      <p className="gate-pitch">{message}</p>
      <button type="button" className="btn" onClick={onRetry}>
        Try again
      </button>
    </div>
  </div>
);

const Nav = ({ account }) => (
  <header className="nav">
    <div className="nav-inner">
      <Link to="/" className="nav-brand">
        <Wordmark />
      </Link>
      <nav className="nav-links">
        <NavLink to="/" end>
          Dashboard
        </NavLink>
        <NavLink to="/profile">Profile</NavLink>
        {account.role === "admin" && <NavLink to="/admin">Admin</NavLink>}
        {/* A reference you read alongside the app, so: its own tab */}
        <a href="/docs" target="_blank" rel="noreferrer">
          Docs
        </a>
      </nav>
      <div className="nav-session">
        <span className="nav-email" title={account.email}>
          {account.email}
        </span>
        <button type="button" className="btn btn-small" onClick={logout}>
          Logout
        </button>
      </div>
    </div>
  </header>
);

const App = () => {
  const [account, setAccount] = useState(undefined); // undefined = loading, null = not logged in
  const [error, setError] = useState("");
  // The docs describe the instance, not the account, so they open without one:
  // they are a link people share, and a tab they leave open next to the app
  const docsOnly = window.location.pathname === "/docs";
  // The popped-out terminal is its own bare, dark window: no nav, no account gate
  // (the WebSocket carries the same cookie). Rendering it before the gate keeps the
  // app chrome from flashing on a white page while the account and xterm chunk load.
  const termPopout = window.location.pathname.match(/^\/app\/([^/]+)\/terminal\/?$/);

  const refreshAccount = useCallback(async () => {
    try {
      setAccount(await api.get("/api/account"));
      setError("");
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setAccount(null);
      } else {
        setError(err.message);
        setAccount(undefined);
      }
    }
  }, []);

  useEffect(() => {
    if (!docsOnly && !termPopout) {
      refreshAccount();
    }
  }, [refreshAccount, docsOnly, termPopout]);

  if (termPopout) {
    // A dark, full-window fallback (same class the terminal itself uses) so the
    // first paint is already the terminal's background, not a white page.
    return (
      <Suspense fallback={<div className="term-page" />}>
        <AppTerminal name={decodeURIComponent(termPopout[1])} fullPage />
      </Suspense>
    );
  }
  if (docsOnly) {
    return (
      <main className="container">
        <Docs />
      </main>
    );
  }
  if (error) {
    return <LoadFailed message={error} onRetry={refreshAccount} />;
  }
  if (account === undefined) {
    return (
      <div className="center-page">
        <Loading />
      </div>
    );
  }
  if (account === null) {
    return <Login />;
  }
  if (account.status === "pending") {
    return <Pending account={account} />;
  }
  if (account.status === "denied") {
    return <Denied account={account} />;
  }
  return (
    <BrowserRouter>
      <Nav account={account} />
      <main className="container">
        <Routes>
          <Route path="/" element={<Dashboard account={account} refreshAccount={refreshAccount} />} />
          <Route path="/app/:name" element={<AppDetail account={account} refreshAccount={refreshAccount} />} />
          <Route path="/profile" element={<Profile />} />
          <Route path="/admin" element={<Admin account={account} />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </BrowserRouter>
  );
};

export default App;
