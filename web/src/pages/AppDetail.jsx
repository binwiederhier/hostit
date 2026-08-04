import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { CopyButton, ErrorBanner, formatUsage, Loading, Snippet, StatusDot } from "../components";

// The SPA is served by the hostit daemon itself, so the agent API lives on our
// own origin under /api.
const origin = window.location.origin;
const tokenPlaceholder = "<this app has no agent token>";

const formatDate = (s) => {
  if (!s) {
    return "unknown";
  }
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString();
};

// The whole point of this page: a ready-to-paste prompt that teaches any agent
// how to drive this one app. It only points at the app's own info endpoint,
// which returns everything else the agent needs.
const promptText = (name, token) => `I want to build a web app called ${name} hosted on hostit.

The app can be managed entirely through the hostit REST API. You can learn more about how to use the API by calling ${origin}/api/${name}/info, using the Bearer token ${token}. Follow the instructions returned by the API.

The app currently serves a placeholder page; replace it.

After exploring the hostit API, ask me questions on what app I want to build.
`;

// Shown once, right after creation, when hostit generated an SSH key pair
// because the account had no keys of its own.
const PrivateKeyBox = ({ privateKey, onDismiss }) => (
  <div className="warn-box">
    <p>
      <strong>This private key is shown only once. Save it now.</strong> hostit generated it for you because no SSH key was
      provided; it grants SSH access to your apps.
    </p>
    <pre className="key-block">{privateKey}</pre>
    <div className="btn-row">
      <CopyButton text={privateKey} small={false}>
        Copy private key
      </CopyButton>
      <button type="button" className="btn" onClick={onDismiss}>
        Dismiss
      </button>
    </div>
  </div>
);

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
const AgentToken = ({ name, token, onRotated }) => {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const rotate = async () => {
    if (busy || !window.confirm("This breaks any assistant session still using the old token. Continue?")) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      onRotated(await api.post(`/v1/apps/${encodeURIComponent(name)}/token`));
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card">
      <h2>Agent token</h2>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {!token && <p className="empty">This app has no owner, so it has no agent token.</p>}
      {token && (
        <>
          <p className="hint">
            This token lets an AI assistant manage this app, and only this app. Anyone with it can change or delete this app's
            contents, so treat it like a password.
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
// the files and the user with it.
const DangerZone = ({ name, onDeleted }) => {
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
      await api.del(`/v1/apps/${encodeURIComponent(name)}`);
      onDeleted();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <div className="card danger-card">
      <h2>Danger zone</h2>
      <p className="hint">
        Deleting <span className="mono">{name}</span> permanently removes its container, files and user. Type the app name to
        confirm.
      </p>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      <form className="inline-form" onSubmit={remove}>
        <input
          type="text"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          placeholder={name}
          aria-label="Type the app name to confirm deletion"
        />
        <button type="submit" className="btn btn-danger" disabled={busy || confirm !== name}>
          {busy ? "Deleting..." : "Delete app"}
        </button>
      </form>
    </div>
  );
};

const AppDetail = ({ account, refreshAccount }) => {
  const { name } = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const [app, setApp] = useState(null);
  const [error, setError] = useState("");
  const [missing, setMissing] = useState(false);
  const [privateKey, setPrivateKey] = useState(location.state ? location.state.private_key || "" : "");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState("");
  const noteTimer = useRef(null);

  useEffect(() => () => clearTimeout(noteTimer.current), []);

  const load = useCallback(async () => {
    setMissing(false);
    try {
      setApp(await api.get(`/v1/apps/${encodeURIComponent(name)}`));
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setMissing(true);
      } else {
        setError(err.message);
      }
    }
  }, [name]);

  useEffect(() => {
    load();
  }, [load]);

  // The one-time private key rides in on router state; scrub it from history so
  // a reload or a back/forward does not resurrect it.
  useEffect(() => {
    if (location.state) {
      navigate(location.pathname, { replace: true, state: null });
    }
  }, [location.pathname, location.state, navigate]);

  // Lifecycle runs through the agent API, which takes our session cookie just
  // like /v1 does; reload afterwards so the status dot follows the container.
  const lifecycle = async (action) => {
    if (busy) {
      return;
    }
    setBusy(true);
    setError("");
    setNote("");
    clearTimeout(noteTimer.current);
    try {
      const res = await api.post(`/api/${encodeURIComponent(name)}/${action}`);
      setNote(res && res.message ? res.message : "Done.");
      noteTimer.current = setTimeout(() => setNote(""), 5000);
      await load();
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
  const prompt = promptText(app.name, token || tokenPlaceholder);

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
        {/* Only the actions that make sense for the current state: a stopped
            app can be started, a running one stopped or restarted. Deleting
            lives in the danger zone at the bottom. */}
        <div className="header-actions">
          {app.running ? (
            <>
              <button type="button" className="btn btn-small btn-primary" onClick={() => lifecycle("restart")} disabled={busy}>
                {busy ? "Working..." : "Restart"}
              </button>
              <button type="button" className="btn btn-small" onClick={() => lifecycle("stop")} disabled={busy}>
                Stop
              </button>
            </>
          ) : (
            <button type="button" className="btn btn-small btn-primary" onClick={() => lifecycle("start")} disabled={busy}>
              {busy ? "Starting..." : "Start"}
            </button>
          )}
        </div>
      </div>
      <p className="usage app-status">
        RAM {formatUsage(app.memory_mb, app.memory_limit_mb)} &middot; disk {formatUsage(app.disk_mb, app.disk_limit_mb)}
        {app.over_quota && <span className="badge badge-danger">over quota</span>} &middot; created {formatDate(app.created_at)}
        {!own && app.owner_email && <> &middot; owned by {app.owner_email}</>}
      </p>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {note && <p className="hint action-note">{note}</p>}
      {privateKey && <PrivateKeyBox privateKey={privateKey} onDismiss={() => setPrivateKey("")} />}

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
        <p>
          Your app is served at{" "}
          <a href={app.url} target="_blank" rel="noreferrer">
            {app.url}
          </a>
          . If you would rather use ssh and scp than an agent, log in with:
        </p>
        {app.ssh && <Snippet text={app.ssh.command} />}
        <p className="hint">
          Your SSH keys from your <Link to="/profile">profile</Link> work here.
        </p>
      </div>

      <AgentToken name={app.name} token={token} onRotated={setApp} />

      <DangerZone name={app.name} onDeleted={deleted} />
    </>
  );
};

export default AppDetail;
