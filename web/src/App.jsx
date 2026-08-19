import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { BrowserRouter, Link, Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import { api, ApiError, isNetworkError } from "./api";
import { useReconnect, useAppViewportHeight } from "./hooks";
import { Loading, StatusDot, Wordmark } from "./components";
import { getTheme, setTheme, THEMES } from "./theme";
import { AppHeaderContext } from "./appHeader";
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

// A small dropdown hook: closes on an outside click or Escape.
const useNavDropdown = () => {
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
  return { open, setOpen, ref };
};

// The "Apps" nav item: clicking goes to the app list, hovering opens a switcher
// that lists the owner's apps so you can jump straight to any one (handy from an
// app detail page). The list is fetched the first time it opens.
const AppsMenu = () => {
  const { open, setOpen, ref } = useNavDropdown();
  const [apps, setApps] = useState(null); // null = not loaded yet
  const [failed, setFailed] = useState(false);
  const closeTimer = useRef(null);
  useEffect(() => {
    // Prefetch on mount so the switcher is populated the moment it opens, rather
    // than showing "Loading..." on the first hover.
    api
      .get("/api/apps")
      .then(setApps)
      .catch(() => setFailed(true));
  }, []);
  // Hover opens it (a small close delay bridges the gap to the popup); clicking
  // "Apps" still navigates to the list.
  const openNow = () => {
    clearTimeout(closeTimer.current);
    setOpen(true);
  };
  const closeSoon = () => {
    clearTimeout(closeTimer.current);
    closeTimer.current = setTimeout(() => setOpen(false), 140);
  };
  return (
    <div className="nav-apps" ref={ref} onPointerEnter={(e) => e.pointerType === "mouse" && openNow()} onPointerLeave={(e) => e.pointerType === "mouse" && closeSoon()}>
      <NavLink to="/" end className="nav-apps-link" onClick={() => setOpen(false)}>
        Apps
      </NavLink>
      {open && (
        <div className="nav-apps-pop" role="menu">
          {apps === null && !failed && <div className="nav-apps-note">Loading...</div>}
          {failed && <div className="nav-apps-note">Couldn't load apps</div>}
          {apps && apps.length === 0 && <div className="nav-apps-note">No apps yet</div>}
          {apps &&
            apps.map((a) => (
              <Link
                key={a.name}
                to={`/app/${a.name}`}
                role="menuitem"
                className="nav-apps-item"
                onClick={() => setOpen(false)}
              >
                <StatusDot running={a.running} appRunning={a.app_running} />
                <span>{a.name}</span>
              </Link>
            ))}
          {apps && apps.length > 0 && <div className="nav-apps-div" />}
          <Link to="/?new=1" role="menuitem" className="nav-apps-all nav-apps-new" onClick={() => setOpen(false)}>
            + New app
          </Link>
        </div>
      )}
    </div>
  );
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
        Log out
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
        Log out
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

// The dashboard, profile and admin views are tables and grids, and 920px
// squeezes them for no reason on a normal screen. The docs match this width
// through their own .docs-container (they render their own <main>); the app
// page is the one exception, staying full-bleed.
const roomyPath = (pathname) => pathname === "/" || pathname === "/profile" || pathname === "/admin";

// RoutedMain is <main> with the width the current route wants. It exists as its
// own component because App renders the Router, so only a child of it can read
// the location.
const RoutedMain = ({ children }) => {
  const { pathname } = useLocation();
  return <main className={"container" + (roomyPath(pathname) ? " container-roomy" : "")}>{children}</main>;
};

const Nav = ({ account, appHeader }) => {
  // The app detail page runs full width; the nav widens to match, so the logo and
  // links slide out to the left edge and the avatar to the right.
  const { pathname } = useLocation();
  const wide = /^\/app\/[^/]+(\/[^/]+)?$/.test(pathname);
  const roomy = roomyPath(pathname);
  // On phones the app's back+name replaces the logo, so there is a single top bar.
  const onApp = wide && appHeader;
  return (
    <header className={"nav" + (wide ? " nav-wide" : "") + (roomy ? " nav-roomy" : "") + (onApp ? " nav-hasappid" : "")}>
      <div className="nav-inner">
      <Link to="/" className="nav-brand">
        <Wordmark />
      </Link>
      {onApp && (
        <div className="nav-appid">
          <Link to="/" className="nav-appid-back" aria-label="Back to apps" title="Back to apps">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <path d="M9.5 3.5 5 8l4.5 4.5" />
            </svg>
          </Link>
          <span className="nav-appid-name">{appHeader.name}</span>
          <StatusDot running={appHeader.running} appRunning={appHeader.appRunning} appState={appHeader.appState} pending={appHeader.pending} />
        </div>
      )}
      {/* Inline on wide screens; on narrow ones these move into the profile menu,
          so there is a single menu (the avatar), not also a burger. */}
      <div className="nav-menu">
        <nav className="nav-links">
          <AppsMenu />
          <NavLink to="/profile">Profile</NavLink>
          {account.role === "admin" && <NavLink to="/admin">Admin</NavLink>}
          {/* A reference you read alongside the app, so: its own tab */}
          <a href="/docs/user" target="_blank" rel="noreferrer">
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
  const [theme, setThemeState] = useState(getTheme());
  const ref = useRef(null);
  const closeTimer = useRef(null);
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
  // Hover opens it (a small close delay bridges the gap to the popup), matching the
  // Apps menu; clicking the avatar still toggles it.
  const openNow = () => {
    clearTimeout(closeTimer.current);
    setOpen(true);
  };
  const closeSoon = () => {
    clearTimeout(closeTimer.current);
    closeTimer.current = setTimeout(() => setOpen(false), 140);
  };
  const name = (account.name || "").trim();
  const initial = (name || account.email || "?").charAt(0).toUpperCase();
  return (
    <div className="nav-profile" ref={ref} onPointerEnter={(e) => e.pointerType === "mouse" && openNow()} onPointerLeave={(e) => e.pointerType === "mouse" && closeSoon()}>
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
              Apps
            </NavLink>
            <NavLink to="/profile" role="menuitem" onClick={close}>
              Profile
            </NavLink>
            {account.role === "admin" && (
              <NavLink to="/admin" role="menuitem" onClick={close}>
                Admin
              </NavLink>
            )}
            <a href="/docs/user" target="_blank" rel="noreferrer" role="menuitem" onClick={close}>
              Docs
            </a>
          </div>
          <div className="nav-profile-div nav-profile-navdiv" />
          <div className="nav-theme" role="group" aria-label="Theme">
            {THEMES.map((t) => (
              <button
                key={t}
                type="button"
                className={"nav-theme-opt" + (theme === t ? " nav-theme-opt-on" : "")}
                aria-pressed={theme === t}
                onClick={() => {
                  setTheme(t);
                  setThemeState(t);
                }}
              >
                {t === "system" ? "System" : t === "light" ? "Light" : "Dark"}
              </button>
            ))}
          </div>
          <div className="nav-profile-div" />
          <button type="button" role="menuitem" className="nav-logout" onClick={logout}>
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
  const [appHeader, setAppHeader] = useState(null); // the app page's identity, for the mobile nav
  // The docs describe the instance, not the account, so they open without one:
  // they are a link people share, and a tab they leave open next to the app
  const docsOnly = window.location.pathname === "/docs" || window.location.pathname.startsWith("/docs/");
  // The popped-out terminal is its own bare, dark window: no nav, no account gate
  // (the WebSocket carries the same cookie). Rendering it before the gate keeps the
  // app chrome from flashing on a white page while the account and xterm chunk load.
  const termPopout = window.location.pathname.match(/^\/app\/([^/]+)\/terminal\/popout\/?$/);

  const loadedOnce = useRef(false); // have we ever loaded the account?

  const refreshAccount = useCallback(async () => {
    try {
      setAccount(await api.get("/api/account"));
      loadedOnce.current = true;
      setError("");
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setAccount(null);
      } else if (isNetworkError(err) && loadedOnce.current) {
        // A transient blip (e.g. the laptop just woke) after we've loaded once:
        // keep the current view rather than dropping to the failure screen. It
        // heals on the next poll or on reconnect.
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

  // Recover when connectivity returns or the tab is shown again.
  useReconnect(refreshAccount);
  // Keep full-height views above the mobile keyboard (see --app-vh).
  useAppViewportHeight();

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
      <main className="container docs-container">
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
      <AppHeaderContext.Provider value={setAppHeader}>
        <Nav account={account} appHeader={appHeader} />
        <RoutedMain>
          <Routes>
            <Route path="/" element={<Dashboard account={account} refreshAccount={refreshAccount} />} />
            <Route path="/app/:name" element={<AppDetail account={account} refreshAccount={refreshAccount} />} />
            <Route path="/app/:name/:viewSlug" element={<AppDetail account={account} refreshAccount={refreshAccount} />} />
            <Route path="/profile" element={<Profile />} />
            <Route path="/admin" element={<Admin account={account} />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </RoutedMain>
      </AppHeaderContext.Provider>
    </BrowserRouter>
  );
};

export default App;
