import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { BrowserRouter, Link, Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
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

const Nav = ({ account }) => {
  // The app detail page runs full width; the nav widens to match, so the logo and
  // links slide out to the left edge and the avatar to the right.
  const { pathname } = useLocation();
  const wide = /^\/app\/[^/]+$/.test(pathname);
  return (
    <header className={"nav" + (wide ? " nav-wide" : "")}>
      <div className="nav-inner">
      <Link to="/" className="nav-brand">
        <Wordmark />
      </Link>
      {/* Inline on wide screens; on narrow ones these move into the profile menu,
          so there is a single menu (the avatar), not also a burger. */}
      <div className="nav-menu">
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
      </div>
      <div className="nav-right">
        <ProfileMenu account={account} />
      </div>
      </div>
    </header>
  );
};

// The account, behind a circular initial avatar in the top-right corner: it shows
// the name Google gave us (falling back to the email), and is the single menu on
// narrow screens -- the nav links live inside it there, so there is no burger.
const ProfileMenu = ({ account }) => {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    if (!open) {
      return undefined;
    }
    const close = (e) => {
      if (ref.current && !ref.current.contains(e.target)) {
        setOpen(false);
      }
    };
    const onKey = (e) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  const close = () => setOpen(false);
  const name = (account.name || "").trim();
  const initial = (name || account.email || "?").charAt(0).toUpperCase();
  return (
    <div className="nav-profile" ref={ref}>
      <button
        type="button"
        className="avatar"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Account"
        title={name || account.email}
      >
        {initial}
      </button>
      {open && (
        <div className="nav-profile-pop" role="menu">
          <div className="nav-profile-head">
            <span className="avatar avatar-lg" aria-hidden="true">
              {initial}
            </span>
            <span className="nav-profile-who">
              {name && <span className="nav-profile-name">{name}</span>}
              <span className="nav-profile-email" title={account.email}>
                {account.email}
              </span>
            </span>
          </div>
          <div className="nav-profile-div" />
          {/* Shown only on narrow screens, where the bar's inline nav is hidden */}
          <div className="nav-profile-nav">
            <NavLink to="/" end role="menuitem" onClick={close}>
              Dashboard
            </NavLink>
            <NavLink to="/profile" role="menuitem" onClick={close}>
              Profile
            </NavLink>
            {account.role === "admin" && (
              <NavLink to="/admin" role="menuitem" onClick={close}>
                Admin
              </NavLink>
            )}
            <a href="/docs" target="_blank" rel="noreferrer" role="menuitem" onClick={close}>
              Docs
            </a>
          </div>
          <div className="nav-profile-div nav-profile-navdiv" />
          <button type="button" role="menuitem" onClick={logout}>
            Log out
          </button>
        </div>
      )}
    </div>
  );
};

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
