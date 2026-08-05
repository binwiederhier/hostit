import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { CopyButton, ErrorBanner, formatDate, formatUsage, Loading, Snippet, StatusDot } from "../components";

// The SPA is served by the hostit daemon itself, so the agent API lives on our
// own origin under /api.
const origin = window.location.origin;
const tokenPlaceholder = "<this app has no agent token>";

// The whole point of this page: a ready-to-paste prompt that teaches any agent
// how to drive this one app. It only points at the app's own info endpoint,
// which returns everything else the agent needs.
//
// Two shapes, because they are two different jobs. A stub is an invitation to
// build. An app whose agent has written a description into hostit.yml is
// finished work someone is coming back to: say that up front, or the next
// session reads a "build me an app" prompt and starts over on top of it.
const promptText = (name, url, token, description) => {
  const api = `The app can be managed entirely through the hostit REST API. You can learn more about how to use the API by calling ${origin}/api/apps/${name}/info, using the Bearer token ${token}. Follow the instructions returned by the API.`;
  if (!description) {
    return `I want to build a web app called "${name}" hosted at ${url}.

The app currently serves a placeholder page. Replace it.

${api}

Keep the app's own documentation in docs/, and update it after every change that matters.

Don't build anything just yet. Check with me first: explore the API, then tell me what you found and ask me what I want the app to do.
`;
  }
  const details = description
    .split("\n")
    .map((line) => `  ${line}`)
    .join("\n");
  return `I want to continue working on my existing web app "${name}", hosted at ${url}.

App details:
${details}

The app is already built and live. Do not rebuild it from scratch.

${api}

The app's own documentation is in docs/. Read it before changing anything, and update it after every change that matters.

Don't change anything just yet. Check with me first: explore the API, read the app's README.md and docs/, then tell me what it does today and wait for my instructions.
`;
};

// Start/stop/restart behind one button: only one of them is ever the sensible
// next move, and none of them is what the page is for. Delete lives here too,
// set apart at the bottom, so the whole rare-actions surface is one menu.
const ActionsMenu = ({ running, busy, onAction, onDelete }) => {
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
  const actions = running ? ["restart", "stop"] : ["start"];

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
          {actions.map((action) => (
            <button key={action} type="button" role="menuitem" onClick={() => run(action)}>
              {action.charAt(0).toUpperCase() + action.slice(1)}
            </button>
          ))}
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
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [hasKeys, setHasKeys] = useState(null); // null until we know, so nothing flickers
  const noteTimer = useRef(null);
  const catchUpTimers = useRef([]);

  useEffect(() => () => clearTimeout(noteTimer.current), []);

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

  // Lifecycle runs through the agent API, which takes our session cookie just
  // like the REST API does; reload afterwards so the status dot follows the
  // container, then keep looking while it settles (start/restart take a moment).
  const lifecycle = async (action) => {
    if (busy) {
      return;
    }
    setBusy(true);
    setError("");
    setNote("");
    clearTimeout(noteTimer.current);
    try {
      const res = await api.post(`/api/apps/${encodeURIComponent(name)}/${action}`);
      setNote(res && res.message ? res.message : "Done.");
      noteTimer.current = setTimeout(() => setNote(""), 5000);
      await load();
      scheduleCatchUp();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

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
              <StatusDot running={app.running} />
              {app.name}
            </h1>
            <span className="status-label">{app.running ? "running" : "stopped"}</span>
          </div>
          <a className="app-url" href={app.url} target="_blank" rel="noreferrer">
            {app.url}
          </a>
        </div>
        {/* Seeing the app is what people come here to do, so that is the one
            accented button; lifecycle and delete hide in the menu. */}
        <div className="header-actions">
          <ActionsMenu running={app.running} busy={busy} onAction={lifecycle} onDelete={() => setConfirmDelete(true)} />
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
      {note && <p className="hint action-note">{note}</p>}

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

      {confirmDelete && (
        <DeleteAppDialog name={app.name} onCancel={() => setConfirmDelete(false)} onDeleted={deleted} />
      )}
    </>
  );
};

export default AppDetail;
