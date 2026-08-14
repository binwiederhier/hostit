import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { api, isNetworkError } from "../api";
import { useReconnect } from "../hooks";
import { ErrorBanner, Loading, Wordmark } from "../components";
import { previewSrc, previewShotSrc, previewScale, DESKTOP_WIDTH, DESKTOP_HEIGHT } from "../preview";

// Same rule the server enforces (app.AppNamePattern)
const nameRe = /^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$/;

const nameHint = "Names are up to 32 characters: lowercase letters, digits and dashes, starting with a letter.";

// The name form, shared by the empty state and the "New app" button. Both need
// a name before anything can happen, so the CTA is the submit button itself.
const CreateForm = ({ name, setName, onSubmit, creating, atLimit, big = false, inputRef }) => {
  const valid = nameRe.test(name);
  return (
    <>
      <form className={big ? "create-form create-form-big" : "create-form"} onSubmit={onSubmit}>
        <input
          ref={inputRef}
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="app name, e.g. blog"
          disabled={atLimit || creating}
          aria-label="New app name"
        />
        <button
          type="submit"
          className={big ? "btn btn-primary btn-big" : "btn btn-primary"}
          disabled={atLimit || creating || !valid}
        >
          {creating && <span className="newapp-spinner" aria-hidden="true" />}
          {creating ? "Creating..." : "Create app"}
        </button>
      </form>
      {atLimit && <p className="hint">You have reached your app limit. Delete an app to create a new one.</p>}
    </>
  );
};

// New app behind a modal, reached from the "New app" button. A dialog asks for
// the one thing needed -- the name -- instead of a field unfolding in place,
// which read as an odd half-state next to the app list.
const NewAppDialog = ({ name, setName, onSubmit, creating, atLimit, onCancel }) => {
  const valid = nameRe.test(name);
  const host = window.location.host;
  const sub = (name || "").replace(/[^a-z0-9-]/g, "") || "app";
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onCancel}>
      <form className="card modal newapp modal-sheet" onSubmit={onSubmit} onMouseDown={(e) => e.stopPropagation()}>
        <button type="button" className="modal-x" onClick={onCancel} title="Close" aria-label="Close">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6 6 18" /></svg>
        </button>
        <div className="newapp-head">
          <div className="newapp-avatar">{sub.slice(0, 2)}</div>
          <div>
            <h2>Create an app</h2>
            <p className="newapp-sub">It gets its own container, subdomain and HTTPS certificate.</p>
          </div>
        </div>
        <label className="newapp-label">App name</label>
        <div className="newapp-input">
          <span className="newapp-dollar mono">$</span>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. blog" aria-label="New app name" autoFocus disabled={creating} spellCheck="false" autoComplete="off" />
        </div>
        <div className="newapp-willbe">
          <div className="row">
            <span className="ico">{"\u{1F310}"}</span>
            <span className="lab">URL</span>
            <span className="val">https://<b>{sub}</b>.{host}</span>
          </div>
          <div className="row">
            <span className="ico">{"⌨️"}</span>
            <span className="lab">SSH</span>
            <span className="val">ssh <b>{sub}</b>@{host}</span>
          </div>
        </div>
        <p className="hint">{nameHint}</p>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onCancel} disabled={creating}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={creating || !valid || atLimit}>
            {creating && <span className="newapp-spinner" aria-hidden="true" />}
            {creating ? "Creating..." : "Create app"}
          </button>
        </div>
      </form>
    </div>
  );
};

// What a brand-new account sees. It has to answer "what is this and what do I
// do now?" on its own, so: the mark, one line of pitch, and the one action.
const EmptyState = (props) => (
  <div className="card empty-state">
    <Wordmark big />
    <h2>Host your own mini-apps</h2>
    <p className="empty-pitch">
      Every app gets its own container, a subdomain with HTTPS, SSH access and an API token you can hand to an AI assistant.
      Name your first app and hostit will have it serving in seconds.
    </p>
    <CreateForm {...props} big />
    <p className="hint">{nameHint}</p>
  </div>
);

const pctOf = (used, limit) => (limit ? Math.min(100, Math.round((used / limit) * 100)) : 0);
const mbLabel = (used, limit) => (limit ? `${used} / ${limit} MB` : `${used} MB`);

// A stable, distinct avatar colour per app, derived from its id (not its name) so
// it never changes on a rename. Hash the id to a hue; a fixed saturation and
// lightness keep white text legible on it in both themes.
const avatarStyle = (id) => {
  let h = 0;
  const s = id || "";
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return { background: `hsl(${h % 360} 52% 45%)`, color: "#fff" };
};

// A non-interactive live thumbnail of the app: its own URL in an iframe rendered
// at a desktop viewport, then CSS-scaled down to the card width. pointer-events
// are off so a click falls through to the card's stretched link (opens the app),
// and the frame is sandboxed and hidden from assistive tech. Powered-off and
// crashed apps have nothing live to show, so we render a muted placeholder to
// keep the grid's card heights even.
const AppPreview = ({ app }) => {
  // app-preview: screenshot swaps the live iframe for the sweep's periodic shot
  // (one image instead of a whole embedded page per card); off drops the pane.
  const mode = app.preview_mode || "live";
  const shot = previewShotSrc(app);
  const [shotFailed, setShotFailed] = useState(false); // no shot taken yet (404)
  const src = mode === "live" ? previewSrc(app) : null;
  const ref = useRef(null);
  const [scale, setScale] = useState(0);
  useEffect(() => {
    if (!src || !ref.current) return undefined;
    const el = ref.current;
    // The card width is set by the grid and changes with the viewport, so measure
    // it rather than guessing a scale.
    const measure = () => setScale(previewScale(el.clientWidth));
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [src]);
  if (mode === "off") {
    return null;
  }
  const showShot = shot && !shotFailed;
  return (
    <div className={"appcard-preview" + (src || showShot ? "" : " is-empty")} ref={ref} aria-hidden="true">
      {src ? (
        <iframe
          className="appcard-preview-frame"
          src={src}
          title=""
          tabIndex={-1}
          scrolling="no"
          loading="lazy"
          sandbox="allow-scripts allow-same-origin"
          style={{ width: DESKTOP_WIDTH, height: DESKTOP_HEIGHT, transform: `scale(${scale})` }}
        />
      ) : showShot ? (
        <img className="appcard-preview-shot" src={shot} alt="" loading="lazy" onError={() => setShotFailed(true)} />
      ) : (
        <span className="appcard-preview-empty">{mode === "screenshot" ? "No screenshot yet" : "No live preview"}</span>
      )}
    </div>
  );
};

// One app as a card: identity, live status, preview, description, bars, actions.
const AppCard = ({ app }) => {
  const running = app.running;
  // A crash loop that gave up shows red "crashed", not the green "running" its
  // still-up container would otherwise imply.
  const crashed = running && app.app_state === "failed";
  const status = crashed ? "crashed" : running ? "running" : "powered off";
  // Prefer a verified custom domain over the default subdomain for the link.
  const publicUrl = app.custom_domain ? `https://${app.custom_domain}` : app.url;
  const publicHost = app.custom_domain || app.url.replace(/^https?:\/\//, "");
  return (
    <div className="appcard">
      <div className="appcard-top">
        <span className="appcard-avatar" style={avatarStyle(app.id)}>{app.name.slice(0, 2)}</span>
        <div className="appcard-id">
          {/* Stretched link: covers the whole card so the entire card opens the app. */}
          <Link className="appcard-nm appcard-link" to={`/app/${app.name}`}>{app.name}</Link>
          <a className="appcard-url" href={publicUrl} target="_blank" rel="noreferrer">{publicHost}</a>
        </div>
      </div>
      <span className={"appcard-pill" + (crashed ? " crashed" : running ? "" : " off")}>
        <span className="appcard-dot" />
        {status}
      </span>
      <AppPreview app={app} />
      <div className="appcard-desc">{app.description || <span className="appcard-nodesc">No description yet</span>}</div>
      <div className="appcard-bars">
        <div className="appcard-bar"><span className="k">CPU</span><span className="bar"><i style={{ width: `${running ? app.cpu_percent || 0 : 0}%` }} /></span><span className="v">{running ? `${app.cpu_percent || 0}%` : "--"}</span></div>
        <div className="appcard-bar"><span className="k">RAM</span><span className="bar"><i style={{ width: `${running ? pctOf(app.memory_mb, app.memory_limit_mb) : 0}%` }} /></span><span className="v">{running ? mbLabel(app.memory_mb, app.memory_limit_mb) : "--"}</span></div>
        <div className="appcard-bar"><span className="k">Disk</span><span className="bar"><i style={{ width: `${pctOf(app.disk_mb, app.disk_limit_mb)}%` }} /></span><span className="v">{mbLabel(app.disk_mb, app.disk_limit_mb)}</span></div>
      </div>
      <div className="appcard-foot">
        <a className="btn btn-small btn-primary" href={publicUrl} target="_blank" rel="noreferrer">Open app</a>
      </div>
    </div>
  );
};

// The stats strip above the grid: turns the list into an at-a-glance overview.
const AppsSummary = ({ apps }) => {
  const running = apps.filter((a) => a.running).length;
  const diskMB = apps.reduce((sum, a) => sum + (a.disk_mb || 0), 0);
  const disk = diskMB >= 1024 ? `${(diskMB / 1024).toFixed(1)} GB` : `${diskMB} MB`;
  const ramMB = apps.reduce((sum, a) => sum + (a.running ? a.memory_mb || 0 : 0), 0);
  const ram = ramMB >= 1024 ? `${(ramMB / 1024).toFixed(1)} GB` : `${ramMB} MB`;
  return (
    <div className="dash-summary">
      <div className="dash-stat"><div className="k">Running</div><div className="v">{running}<small> / {apps.length}</small></div></div>
      <div className="dash-stat"><div className="k">Memory used</div><div className="v">{ram}</div></div>
      <div className="dash-stat"><div className="k">Disk used</div><div className="v">{disk}</div></div>
    </div>
  );
};

const Dashboard = ({ account, refreshAccount }) => {
  const [apps, setApps] = useState(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const [adding, setAdding] = useState(false);
  const inputRef = useRef(null);
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();

  // Opened here with ?new (e.g. from the nav's "+ New app"): show the dialog.
  useEffect(() => {
    if (searchParams.get("new") !== null) {
      setAdding(true);
      setSearchParams({}, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  const atLimit = account.usage.apps >= account.limits.app_limit;
  const nameValid = nameRe.test(name);

  const load = useCallback(async () => {
    if (!navigator.onLine) return; // offline: don't hammer, wait for reconnect
    try {
      setApps(await api.get("/api/apps"));
      setError("");
    } catch (err) {
      // A background poll: a transient network blip keeps the last list rather
      // than flashing a sticky banner; only real errors surface.
      if (isNetworkError(err)) return;
      setError(err.message);
    }
  }, []);

  // The server answers from a cache so the list renders at once; its numbers may
  // be a few seconds old, so ask again shortly after and then keep it fresh
  // while the page is open.
  useEffect(() => {
    load();
    const soon = setTimeout(load, 2000);
    const ticker = setInterval(load, 15000);
    return () => {
      clearTimeout(soon);
      clearInterval(ticker);
    };
  }, [load]);
  useReconnect(load); // refresh the list when connectivity or visibility returns

  const create = async (e) => {
    e.preventDefault();
    if (!nameValid || creating) {
      return;
    }
    setCreating(true);
    setError("");
    try {
      const res = await api.post("/api/apps", { name });
      setName("");
      setAdding(false);
      refreshAccount();
      navigate(`/app/${res.name}`);
    } catch (err) {
      setError(err.message);
    } finally {
      setCreating(false);
    }
  };

  const cancelAdding = () => {
    setAdding(false);
    setName("");
  };

  const formProps = { name, setName, onSubmit: create, creating, atLimit, inputRef };
  const empty = apps !== null && apps.length === 0;

  return (
    <>
      <div className="page-header">
        <h1>Apps</h1>
        <div className="header-actions">
          <span className="usage">
            {account.usage.apps} of {account.limits.app_limit} apps
          </span>
          {!empty && (
            <button type="button" className="btn btn-primary btn-withicon" onClick={() => setAdding(true)} disabled={atLimit}>
              New app
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true"><path d="M8 3.5v9M3.5 8h9" /></svg>
            </button>
          )}
        </div>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {empty && <EmptyState {...formProps} />}
      {!empty && apps === null && !error && (
        <div className="card">
          <Loading label="Loading apps..." />
        </div>
      )}
      {!empty && apps !== null && apps.length > 0 && (
        <>
          <AppsSummary apps={apps} />
          <div className="dash-grid">
            {apps.map((app) => (
              <AppCard key={app.name} app={app} />
            ))}
          </div>
        </>
      )}
      {adding && (
        <NewAppDialog name={name} setName={setName} onSubmit={create} creating={creating} atLimit={atLimit} onCancel={cancelAdding} />
      )}
    </>
  );
};

export default Dashboard;
