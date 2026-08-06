import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { CopyButton, ErrorBanner, formatDate, formatUsage, Loading, Snippet, StatusDot } from "../components";

// xterm is heavy and only needed when a terminal is actually opened, so it is
// split into its own chunk and loaded on demand.
const AppTerminal = lazy(() => import("./AppTerminal"));
// The assistant pulls in a markdown renderer, so it stays a lazy chunk too.
const AppAssistant = lazy(() => import("./AppAssistant"));

// The SPA is served by the hostit daemon itself, so the agent API lives on our
// own origin under /api.
const origin = window.location.origin;
const tokenPlaceholder = "<this app has no agent token>";

// How a lifecycle action is watched to completion. `done(app, base)` decides when
// the transition has actually happened: `base` is the app's start times captured
// at the click. Reboot and restart end in the same running state they began in, so
// they wait for a start time strictly newer than the baseline; the others just
// wait for the container or app process to flip. Power/reboot bring the whole app
// back up (the agent starts the run: command), so their target is app_running.
const TRANSITIONS = {
  poweron: { label: "Powering on", done: (a) => a.running && a.app_running },
  poweroff: { label: "Powering off", done: (a) => !a.running },
  reboot: { label: "Rebooting", done: (a, b) => a.running && a.app_running && a.started_at > b.started_at },
  start: { label: "Starting app", done: (a) => a.running && a.app_running },
  stop: { label: "Stopping app", done: (a) => a.running && !a.app_running },
  restart: { label: "Restarting app", done: (a, b) => a.running && a.app_running && a.app_started_at > b.app_started_at },
};
const transitionPollMs = 2000;
const transitionTimeoutMs = 90000;

// The whole point of this page: a short, ready-to-paste prompt that points an
// agent at the app's own info endpoint. Everything the agent needs to know about
// the API is returned by that call, so the prompt stays small and does not
// duplicate it -- it only sets the agent's stance: learn the API, then wait for
// the owner rather than interrogating them or building on a guess.
//
// Two shapes, because they are two different jobs. A stub is an invitation to
// build. An app whose agent has written a description into hostit.yml is
// finished work someone is coming back to: say that up front, or the next
// session reads a "build me an app" prompt and starts over on top of it.
const promptText = (name, url, token, description) => {
  const apiLine = `Manage it through the hostit REST API: call ${origin}/api/apps/${name}/info with the Bearer token ${token} to learn how, then follow what it returns.`;
  if (!description) {
    return `I've created a hostit app called "${name}" at ${url}.

${apiLine}

Read that, then reply exactly: "I understand the hostit API. I'm ready to build. Tell me what you want to make." Do not ask exploratory questions and do not build anything until I tell you what to make.
`;
  }
  const details = description
    .split("\n")
    .map((line) => `  ${line}`)
    .join("\n");
  return `I'm continuing work on my hostit app "${name}" at ${url}.

App details:
${details}

${apiLine}

Read that and the app's README.md and docs/, then reply exactly: "I understand the hostit API and this app. I'm ready to continue. Tell me what you want to change." Do not rebuild it from scratch, do not ask exploratory questions, and do not change anything until I tell you what to do.
`;
};

// Start/stop/restart behind one button: only one of them is ever the sensible
// next move, and none of them is what the page is for. Delete lives here too,
// set apart at the bottom, so the whole rare-actions surface is one menu.
const ActionsMenu = ({ running, appRunning, busy, onAction, onDelete }) => {
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

  const run = (action) => {
    setOpen(false);
    onAction(action);
  };
  const remove = () => {
    setOpen(false);
    onDelete();
  };
  // App verbs act on the run: command inside a running container; power verbs act
  // on the container itself. When it is off, the only thing to do is power it on.
  // Which app verbs make sense depends on whether the run: process is up: offer
  // Stop/Restart to a running app, and only Start to a stopped one.
  const appVerbs = appRunning
    ? [
        { verb: "restart", label: "Restart app" },
        { verb: "stop", label: "Stop app" },
      ]
    : [{ verb: "start", label: "Start app" }];
  const powerVerbs = [
    { verb: "reboot", label: "Reboot" },
    { verb: "poweroff", label: "Power off" },
  ];

  return (
    <div className="menu" ref={ref}>
      <button
        type="button"
        className="btn"
        onClick={() => setOpen(!open)}
        disabled={busy}
        aria-haspopup="menu"
        aria-expanded={open}
      >
        {busy ? "Working..." : "Actions"} <span aria-hidden="true">&#9662;</span>
      </button>
      {open && (
        <div className="menu-items" role="menu">
          {running ? (
            <>
              {appVerbs.map((a) => (
                <button key={a.verb} type="button" role="menuitem" onClick={() => run(a.verb)}>
                  {a.label}
                </button>
              ))}
              {powerVerbs.map((a) => (
                <button key={a.verb} type="button" role="menuitem" className="menu-item-sep" onClick={() => run(a.verb)}>
                  {a.label}
                </button>
              ))}
            </>
          ) : (
            <button type="button" role="menuitem" onClick={() => run("poweron")}>
              Power on
            </button>
          )}
          <button type="button" role="menuitem" className="menu-item-danger" onClick={remove}>
            Delete app...
          </button>
        </div>
      )}
    </div>
  );
};

const NotFound = ({ name }) => (
  <div className="card">
    <h2>This app does not exist (or is not yours)</h2>
    <p className="empty">
      There is no app called <span className="mono">{name}</span> on your account.
    </p>
    <Link className="btn" to="/">
      Back to apps
    </Link>
  </div>
);

// The app-scoped token is created with the app and returned by the API on
// every fetch, so it is always on display here; rotating it mints a new one.
const ApiAccess = ({ name, token, onRotated }) => {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const rotate = async () => {
    if (busy || !window.confirm("This breaks any assistant session still using the old token. Continue?")) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      onRotated(await api.post(`/api/apps/${encodeURIComponent(name)}/token`));
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card">
      <h2>API access</h2>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {!token && <p className="empty">This app has no owner, so it has no API token.</p>}
      {token && (
        <>
          <p className="hint">
            Everything on this page can also be done through the REST API. This token is scoped to this one app: point an AI
            assistant (or your own scripts) at{" "}
            <span className="mono">
              {origin}/api/apps/{name}/info
            </span>{" "}
            with it and the API describes the rest. Anyone holding it can change or delete this app's contents, so treat it like a
            password. See the{" "}
            <a href="/docs#api" target="_blank" rel="noreferrer">
              API reference
            </a>{" "}
            for the full list of endpoints.
          </p>
          <pre className="key-block">{token}</pre>
          <div className="btn-row">
            <CopyButton text={token} small={false}>
              Copy token
            </CopyButton>
            <button type="button" className="btn" onClick={rotate} disabled={busy}>
              {busy ? "Regenerating..." : "Regenerate"}
            </button>
          </div>
        </>
      )}
    </div>
  );
};

// Delete behind a type-the-name confirmation, since it takes the container,
// the files and the user with it. A modal, not a section at the bottom of the
// page: it is reached deliberately from the Actions menu, never stumbled into.
const DeleteAppDialog = ({ name, onCancel, onDeleted }) => {
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const remove = async (e) => {
    e.preventDefault();
    if (busy || confirm !== name) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.del(`/api/apps/${encodeURIComponent(name)}`);
      onDeleted();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true">
      <form className="card modal" onSubmit={remove}>
        <h2>Delete {name}</h2>
        <p>
          This permanently removes <span className="mono">{name}</span>: its container, all its files and its Unix user.{" "}
          <strong>This cannot be undone.</strong>
        </p>
        <p className="hint">Type the app name to confirm.</p>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <input
          type="text"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          placeholder={name}
          aria-label="Type the app name to confirm deletion"
          autoFocus
        />
        <div className="btn-row">
          <button type="button" className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn btn-danger" disabled={busy || confirm !== name}>
            {busy ? "Deleting..." : "Delete app"}
          </button>
        </div>
      </form>
    </div>
  );
};

const AppDetail = ({ account, refreshAccount }) => {
  const { name } = useParams();
  const navigate = useNavigate();
  const [app, setApp] = useState(null);
  const [error, setError] = useState("");
  const [missing, setMissing] = useState(false);
  const [pending, setPending] = useState(null); // an in-flight lifecycle transition, or null
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [showTerminal, setShowTerminal] = useState(false);
  const [showAssistant, setShowAssistant] = useState(false);
  const [hasKeys, setHasKeys] = useState(null); // null until we know, so nothing flickers
  const catchUpTimers = useRef([]);

  // Whether SSH is usable at all depends on the profile, not on the app
  useEffect(() => {
    api
      .get("/api/account/keys")
      .then((keys) => setHasKeys(keys.length > 0))
      .catch(() => setHasKeys(null));
  }, []);

  const load = useCallback(async () => {
    setMissing(false);
    try {
      setApp(await api.get(`/api/apps/${encodeURIComponent(name)}`));
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setMissing(true);
      } else {
        setError(err.message);
      }
    }
  }, [name]);

  // A container takes a few seconds to come up, so a single reload right after
  // landing (or after start/restart) catches it mid-boot and shows a red dot for
  // an app that is fine. Look again a few times while it settles.
  const scheduleCatchUp = useCallback(() => {
    catchUpTimers.current.forEach(clearTimeout);
    catchUpTimers.current = [5, 10, 15].map((seconds) => setTimeout(load, seconds * 1000));
  }, [load]);

  useEffect(() => {
    load();
    scheduleCatchUp();
    const ticker = setInterval(load, 60000);
    return () => {
      catchUpTimers.current.forEach(clearTimeout);
      clearInterval(ticker);
    };
  }, [load, scheduleCatchUp]);

  // Lifecycle runs through the agent API, which takes our session cookie just like
  // the REST API does. We do not guess the result: we record the app's start times
  // at the moment of the click and then poll until the app has actually reached the
  // target state. That is the only way to show a reboot or an app restart honestly,
  // since both end in the same running state they began in -- "done" means a start
  // time newer than the one we saw, not merely "running" again.
  const lifecycle = async (action) => {
    if (pending) {
      return;
    }
    const transition = TRANSITIONS[action];
    const base = { started_at: app?.started_at || 0, app_started_at: app?.app_started_at || 0 };
    setError("");
    setPending({ verb: action, label: transition.label, base, since: Date.now() });
    try {
      await api.post(`/api/apps/${encodeURIComponent(name)}/${action}`);
    } catch (err) {
      // The action itself was refused (e.g. an app verb while the container is
      // down): there is nothing to wait for, so stop and surface it.
      setError(err.message);
      setPending(null);
    }
  };

  // While a transition is pending, poll every couple of seconds until the app
  // reaches the target state (or we give up). The daemon keeps its state cache
  // warm for the whole settle window, so each poll sees real progress.
  useEffect(() => {
    if (!pending) {
      return undefined;
    }
    const done = TRANSITIONS[pending.verb].done;
    let cancelled = false;
    let timer;
    const poll = async () => {
      try {
        const fresh = await api.get(`/api/apps/${encodeURIComponent(name)}`);
        if (cancelled) {
          return;
        }
        setApp(fresh);
        if (done(fresh, pending.base)) {
          setPending(null);
          return;
        }
      } catch {
        // Transient error mid-transition: keep polling.
      }
      if (cancelled) {
        return;
      }
      if (Date.now() - pending.since > transitionTimeoutMs) {
        setPending(null);
        setError(`${pending.label} did not finish in time; showing the last known state`);
        return;
      }
      timer = setTimeout(poll, transitionPollMs);
    };
    timer = setTimeout(poll, transitionPollMs);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [pending, name]);

  const deleted = () => {
    refreshAccount();
    navigate("/", { replace: true });
  };

  if (missing) {
    return <NotFound name={name} />;
  }
  if (error && app === null) {
    return (
      <>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <Link className="btn" to="/">
          Back to apps
        </Link>
      </>
    );
  }
  if (app === null) {
    return <Loading label="Loading app..." />;
  }

  const own = !account.limits || app.owner_email === undefined || app.owner_email === account.email;
  const token = app.agent_token || "";
  const prompt = promptText(app.name, app.url, token || tokenPlaceholder, (app.description || "").trim());

  return (
    <>
      <p className="crumb">
        <Link to="/">&larr; Apps</Link>
      </p>
      <div className="page-header app-header">
        <div className="app-heading">
          <div className="app-title-row">
            <h1 className="app-title">
              <StatusDot running={app.running} appRunning={app.app_running} pending={!!pending} />
              {app.name}
            </h1>
            {pending ? (
              <span className="status-label status-label-pending">
                {pending.label}
                <span className="ellipsis" aria-hidden="true">
                  <span>.</span>
                  <span>.</span>
                  <span>.</span>
                </span>
              </span>
            ) : (
              <span className="status-label">
                {!app.running ? "Powered off" : app.app_running ? "Running" : "App stopped"}
              </span>
            )}
          </div>
          <a className="app-url" href={app.url} target="_blank" rel="noreferrer">
            {app.url}
          </a>
        </div>
        {/* Seeing the app is what people come here to do, so that is the one
            accented button; lifecycle and delete hide in the menu. */}
        <div className="header-actions">
          <ActionsMenu
            running={app.running}
            appRunning={app.app_running}
            busy={!!pending}
            onAction={lifecycle}
            onDelete={() => setConfirmDelete(true)}
          />
          {/* Build/change the app from the browser, no local machine needed */}
          <button type="button" className="btn" onClick={() => setShowAssistant(true)}>
            Build with AI
          </button>
          {/* A shell in the container, in the browser -- only useful while it runs */}
          {app.running && (
            <button type="button" className="btn" onClick={() => setShowTerminal(true)}>
              Terminal
            </button>
          )}
          <a className="btn btn-primary" href={app.url} target="_blank" rel="noreferrer">
            Open app
          </a>
        </div>
      </div>
      <p className="usage app-status">
        RAM {formatUsage(app.memory_mb, app.memory_limit_mb)} &middot; disk {formatUsage(app.disk_mb, app.disk_limit_mb)}
        {app.over_quota && <span className="badge badge-danger">over quota</span>} &middot; created {formatDate(app.created_at)}
        {!own && app.owner_email && <> &middot; owned by {app.owner_email}</>}
      </p>
      <ErrorBanner message={error} onDismiss={() => setError("")} />

      <div className="card prompt-card">
        <h2>Prompt for your AI assistant</h2>
        <div className="term prompt-block">
          <pre>{prompt}</pre>
          <div className="term-copy">
            <CopyButton text={prompt} small={false} disabled={token === ""}>
              Copy prompt
            </CopyButton>
          </div>
        </div>
        <p className="hint">Paste this into Claude Code (or any AI agent).</p>
      </div>

      <div className="card">
        <h2>SSH access</h2>
        {/* Without a key in the profile the ssh command cannot work, so showing
            it would only produce a "Permission denied" later. */}
        {hasKeys === false ? (
          <p>
            You must add at least one SSH key to your <Link to="/profile">profile</Link> before you can reach this app over SSH.
            Keys you add there are installed on every app you own.
          </p>
        ) : (
          <>
            <p>You can also access your app container via SSH and/or copy files via scp/rsync:</p>
            {app.ssh && <Snippet text={app.ssh.command} />}
            <p className="hint">
              Your SSH keys from your <Link to="/profile">profile</Link> work here.
            </p>
          </>
        )}
      </div>

      <ApiAccess name={app.name} token={token} onRotated={setApp} />

      {showTerminal && (
        <Suspense fallback={null}>
          <AppTerminal name={app.name} onClose={() => setShowTerminal(false)} />
        </Suspense>
      )}
      {showAssistant && (
        <Suspense fallback={null}>
          <AppAssistant name={app.name} onClose={() => setShowAssistant(false)} />
        </Suspense>
      )}
      {confirmDelete && (
        <DeleteAppDialog name={app.name} onCancel={() => setConfirmDelete(false)} onDeleted={deleted} />
      )}
    </>
  );
};

export default AppDetail;
