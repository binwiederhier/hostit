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
const AppCard = ({ app, onToast }) => {
  const running = app.running;
  // A crash loop that gave up shows red "crashed", not the green "running" its
  // still-up container would otherwise imply.
  const crashed = running && app.app_state === "failed";
  const status = app.archived ? "archived" : crashed ? "crashed" : running ? "running" : "powered off";
  // Prefer a verified custom domain over the default subdomain for the link.
  const publicUrl = app.custom_domain ? `https://${app.custom_domain}` : app.url;
  const publicHost = app.custom_domain || app.url.replace(/^https?:\/\//, "");
  // Manual screenshot refresh: fire-and-forget, just queues a shot and flashes a
  // toast. The card does not update live (the new shot appears on the next load).
  const canRefresh = (app.preview_mode || "live") === "screenshot" && running;
  const refreshShot = async (e) => {
    e.preventDefault();
    e.stopPropagation();
    try {
      await api.post(`/api/apps/${encodeURIComponent(app.name)}/preview`);
      onToast("Screenshot queued");
    } catch {
      onToast("Couldn't queue screenshot");
    }
  };
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
      <div className="appcard-statusrow">
        <span className={"appcard-pill" + (crashed ? " crashed" : running ? "" : " off")}>
          <span className="appcard-dot" />
          {status}
        </span>
        {canRefresh && (
          <button type="button" className="appcard-refresh" onClick={refreshShot} title="Queue a new screenshot" aria-label="Queue a new screenshot">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M20 11a8 8 0 1 0-.6 4" />
              <path d="M20 5v6h-6" />
            </svg>
          </button>
        )}
      </div>
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

// The dashboard remembers how you like to look at your apps. Cards are good for
// a handful; a dense list is what makes thirty of them readable. Kept in
// localStorage rather than the account: it is a per-device viewing preference,
// like a window size, not something worth a registry column and a round trip.
const VIEW_KEY = "hostit.appview";
const readView = () => {
  try {
    return localStorage.getItem(VIEW_KEY) === "list" ? "list" : "cards";
  } catch {
    return "cards"; // private mode, or storage disabled
  }
};

// Archived apps are hidden by default: they are the ones deliberately put away,
// so they should not crowd the ones being worked on. Remembered the same way as
// the view, and for the same reason.
const ARCHIVED_KEY = "hostit.showarchived";
const readShowArchived = () => {
  try {
    return localStorage.getItem(ARCHIVED_KEY) === "1";
  } catch {
    return false;
  }
};

// The archived filter, shown only when the account HAS archived apps -- a switch
// for something that does not exist is just a question the reader has to answer.
// fmtPair renders "allocated/pool" in one shared unit ("1.2/3.8 GB").
const fmtPair = (used, total) => {
  if (total >= 1024) {
    const gb = (v) => (v / 1024).toFixed(v % 1024 ? 1 : 0);
    return `${gb(used)}/${gb(total)} GB`;
  }
  return `${used}/${total} MB`;
};

const ArchivedToggle = ({ on, count, onChange }) => (
  <button
    type="button"
    className={"dash-viewbtn dash-archivedbtn" + (on ? " on" : "")}
    onClick={() => onChange(!on)}
    title={on ? `Hide ${count} archived` : `Show ${count} archived`}
    aria-pressed={on}
  >
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M2.5 4.5h11" />
      <path d="M3.5 4.5v8a1 1 0 0 0 1 1h7a1 1 0 0 0 1-1v-8" />
      <path d="M6.5 7.5h3" />
    </svg>
    <span>{count}</span>
  </button>
);

// How long the container has been up, from the Unix SECONDS the API reports --
// not a Date string, which is what makes new Date(started_at) land in 1970.
const formatUptime = (startedAt) => {
  if (!startedAt) {
    return "--";
  }
  const secs = Math.max(0, Math.floor(Date.now() / 1000) - startedAt);
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`;
  return `${Math.floor(secs / 86400)}d`;
};

// One row of the list view: the same facts a card carries, at a glance.
const AppRow = ({ app }) => {
  const navigate = useNavigate();
  const running = app.running;
  const crashed = running && app.app_state === "failed";
  const status = app.archived ? "archived" : crashed ? "crashed" : running ? "running" : "powered off";
  const publicUrl = app.custom_domain ? `https://${app.custom_domain}` : app.url;
  const publicHost = app.custom_domain || app.url.replace(/^https?:\/\//, "");
  // The whole row opens the app, the way the whole card does. Clicks that land
  // on a link or a button are left alone -- the public-URL link in this row
  // goes somewhere else entirely, and a modified click on the name should still
  // open a new tab rather than navigate this one.
  const openApp = (e) => {
    if (e.target.closest("a, button")) {
      return;
    }
    navigate(`/app/${app.name}`);
  };
  return (
    <tr className="applist-row" onClick={openApp}>
      <td className="applist-name">
        <span className="appcard-avatar applist-avatar" style={avatarStyle(app.id)}>{app.name.slice(0, 2)}</span>
        <span className="applist-id">
          <Link className="applist-nm" to={`/app/${app.name}`}>{app.name}</Link>
          <a className="applist-url" href={publicUrl} target="_blank" rel="noreferrer">{publicHost}</a>
        </span>
      </td>
      <td>
        <span className={"appcard-pill" + (crashed ? " crashed" : running ? "" : " off")}>
          <span className="appcard-dot" />
          {status}
        </span>
      </td>
      <td className="applist-desc">{app.description || <span className="appcard-nodesc">--</span>}</td>
      <td className="applist-num">{running ? `${app.cpu_percent || 0}%` : "--"}</td>
      <td className="applist-num">{running ? mbLabel(app.memory_mb, app.memory_limit_mb) : "--"}</td>
      <td className="applist-num">{mbLabel(app.disk_mb, app.disk_limit_mb)}</td>
      {/* Uptime, not "last deploy": a deploy restarts the app so the two track
          each other closely, but the API has no deploy timestamp and a column
          claiming one would be wrong after a plain reboot. */}
      <td className="applist-num" title={app.started_at ? new Date(app.started_at * 1000).toLocaleString() : ""}>
        {running ? formatUptime(app.started_at) : "--"}
      </td>
    </tr>
  );
};

// The list itself.
const AppList = ({ apps }) => {
  return (
    <div className="applist-wrap">
      <table className="applist">
        <thead>
          <tr>
            <th>App</th>
            <th>Status</th>
            <th>Description</th>
            <th className="applist-num">CPU</th>
            <th className="applist-num">RAM</th>
            <th className="applist-num">Disk</th>
            <th className="applist-num">Uptime</th>
          </tr>
        </thead>
        <tbody>
          {apps.map((app) => (
            <AppRow key={app.name} app={app} />
          ))}
        </tbody>
      </table>
    </div>
  );
};

// The cards/list switch.
const ViewToggle = ({ view, onChange }) => (
  <div className="dash-viewtoggle" role="group" aria-label="How to show apps">
    <button
      type="button"
      className={"dash-viewbtn" + (view === "cards" ? " on" : "")}
      onClick={() => onChange("cards")}
      title="Card view"
      aria-label="Card view"
      aria-pressed={view === "cards"}
    >
      <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
        <rect x="1" y="1" width="6" height="6" rx="1.4" />
        <rect x="9" y="1" width="6" height="6" rx="1.4" />
        <rect x="1" y="9" width="6" height="6" rx="1.4" />
        <rect x="9" y="9" width="6" height="6" rx="1.4" />
      </svg>
    </button>
    <button
      type="button"
      className={"dash-viewbtn" + (view === "list" ? " on" : "")}
      onClick={() => onChange("list")}
      title="List view"
      aria-label="List view"
      aria-pressed={view === "list"}
    >
      <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
        <rect x="1" y="2" width="14" height="2" rx="1" />
        <rect x="1" y="7" width="14" height="2" rx="1" />
        <rect x="1" y="12" width="14" height="2" rx="1" />
      </svg>
    </button>
  </div>
);

const Dashboard = ({ account, refreshAccount }) => {
  const [apps, setApps] = useState(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const [adding, setAdding] = useState(false);
  const [toast, setToast] = useState("");
  const [view, setView] = useState(readView);
  const [showArchived, setShowArchived] = useState(readShowArchived);
  const toastTimer = useRef(null);
  const inputRef = useRef(null);
  // A single page-level snackbar (not per card): the cards lift with a transform
  // on hover, which would otherwise trap a fixed-position toast inside the card.
  const showToast = useCallback((message) => {
    setToast(message);
    clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(""), 2500);
  }, []);
  const changeView = useCallback((next) => {
    setView(next);
    try {
      localStorage.setItem(VIEW_KEY, next);
    } catch {
      // Storage can be unavailable; the choice then lasts for this page only.
    }
  }, []);
  const changeShowArchived = useCallback((next) => {
    setShowArchived(next);
    try {
      localStorage.setItem(ARCHIVED_KEY, next ? "1" : "0");
    } catch {
      // As above: the choice then lasts for this page only.
    }
  }, []);
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
  const archivedCount = (apps || []).filter((a) => a.archived).length;
  const shown = showArchived ? apps || [] : (apps || []).filter((a) => !a.archived);

  return (
    <>
      <div className="page-header">
        <div className="page-title">
          <h1>Apps</h1>
          <span className="usage">
            <span className="usage-item" title="Apps created of your app limit">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <rect x="2" y="2" width="5" height="5" rx="1" /><rect x="9" y="2" width="5" height="5" rx="1" />
                <rect x="2" y="9" width="5" height="5" rx="1" /><rect x="9" y="9" width="5" height="5" rx="1" />
              </svg>
              {account.usage.apps}/{account.limits.app_limit}
            </span>
            {account.limits.memory_pool_mb > 0 && (
              <>
                <span className="usage-item" title="RAM allocated of your pool">
                  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <rect x="2" y="4" width="12" height="8" rx="1" />
                    <path d="M4.5 12v2M8 12v2M11.5 12v2M4.5 2v2M8 2v2M11.5 2v2" />
                  </svg>
                  {fmtPair(account.usage.pool_memory_mb || 0, account.limits.memory_pool_mb)}
                </span>
                <span className="usage-item" title="Disk allocated of your pool">
                  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <ellipse cx="8" cy="4" rx="6" ry="2.2" />
                    <path d="M2 4v8c0 1.2 2.7 2.2 6 2.2s6-1 6-2.2V4" />
                    <path d="M2 8c0 1.2 2.7 2.2 6 2.2s6-1 6-2.2" />
                  </svg>
                  {fmtPair(account.usage.pool_disk_mb || 0, account.limits.disk_pool_mb)}
                </span>
              </>
            )}
          </span>
        </div>
        <div className="header-actions">
          {!empty && apps !== null && apps.length > 0 && (
            <>
              {archivedCount > 0 && <ArchivedToggle on={showArchived} count={archivedCount} onChange={changeShowArchived} />}
              <ViewToggle view={view} onChange={changeView} />
            </>
          )}
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
          {shown.length === 0 ? (
            // Every app is archived and hidden: say so, rather than showing the
            // same blank page an account with no apps at all would get.
            <div className="card dash-allarchived">
              All {archivedCount} of your apps are archived.{" "}
              <button type="button" className="btn btn-small" onClick={() => changeShowArchived(true)}>
                Show archived
              </button>
            </div>
          ) : view === "list" ? (
            <AppList apps={shown} />
          ) : (
            <div className="dash-grid">
              {shown.map((app) => (
                <AppCard key={app.name} app={app} onToast={showToast} />
              ))}
            </div>
          )}
        </>
      )}
      {adding && (
        <NewAppDialog name={name} setName={setName} onSubmit={create} creating={creating} atLimit={atLimit} onCancel={cancelAdding} />
      )}
      {toast && (
        <div className="snackbar" role="status" aria-live="polite">
          {toast}
        </div>
      )}
    </>
  );
};

export default Dashboard;
