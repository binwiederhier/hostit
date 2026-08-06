import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, ApiError } from "../api";
import { CopyButton, ErrorBanner, Loading, Snippet, StatusDot } from "../components";

// xterm is heavy and only needed when a terminal is actually opened, so it is
// split into its own chunk and loaded on demand.
const AppTerminal = lazy(() => import("./AppTerminal"));
// The assistant pulls in a markdown renderer, so it stays a lazy chunk too.
const AppAssistant = lazy(() => import("./AppAssistant"));

// The SPA is served by the hostit daemon itself, so the agent API lives on our
// own origin under /api.
const origin = window.location.origin;
const tokenPlaceholder = "<this app has no agent token>";

// The chat/preview split remembers how the owner sized it, across apps and reloads.
const splitKey = "hostit.ws.chatFrac";
const defaultChatFrac = 0.52;

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
// The detail page shows live gauges, so it refreshes faster than the dashboard.
// The daemon serves state from a warm cache, so this stays cheap.
const detailPollMs = 8000;

// The whole point of the prompt: a short, ready-to-paste block that points an
// external agent at the app's own info endpoint. Everything the agent needs to
// know about the API is returned by that call, so the prompt stays small and does
// not duplicate it -- it only sets the agent's stance: learn the API, then wait
// for the owner rather than interrogating them or building on a guess.
//
// Two shapes, because they are two different jobs. A stub is an invitation to
// build. An app whose agent has written a description into hostit.yml is finished
// work someone is coming back to: say that up front, or the next session reads a
// "build me an app" prompt and starts over on top of it.
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

// A small svg icon set, so the top bar reads as buttons, not a wall of words.
const TerminalIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <rect x="1.5" y="2.5" width="13" height="11" rx="1.5" />
    <path d="M4 6l2.2 2L4 10M8.5 10.5H11.5" />
  </svg>
);
const ControlsIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M2 4.5h7M11.5 4.5h2.5M2 11.5h2.5M7 11.5h7" />
    <circle cx="10" cy="4.5" r="1.6" />
    <circle cx="6" cy="11.5" r="1.6" />
  </svg>
);
const BackIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M9.5 3.5 5 8l4.5 4.5" />
  </svg>
);
const SparkleIcon = () => (
  <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
    <path d="M8 0.8l1.5 4.2 4.2 1.5-4.2 1.5L8 12.2 6.5 8 2.3 6.5 6.5 5z" />
    <path d="M13 9.5l0.6 1.6 1.6 0.6-1.6 0.6L13 14l-0.6-1.7-1.6-0.6 1.6-0.6z" />
  </svg>
);

// formatUptime turns a container start time (Unix seconds) into a short duration
// like "3d 4h", "2h 15m", "8m" or "42s"; a stopped container has no uptime.
const formatUptime = (startedAt) => {
  if (!startedAt) {
    return "-";
  }
  let s = Math.max(0, Math.floor(Date.now() / 1000 - startedAt));
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m`;
  return `${s}s`;
};

// One resource readout: a label and its value, the value going red near a limit.
const Res = ({ label, value, hot }) => (
  <div className="res">
    <span className="res-k">{label}</span>
    <span className={"res-v" + (hot ? " res-v-hot" : "")}>{value}</span>
  </div>
);

// CPU / RAM / Disk / Uptime in a compact 2x2 grid, sitting beside the controls.
// Only meaningful while the container is up, so the caller shows it only then.
const UsageGrid = ({ app }) => {
  const cpuHot = (app.cpu_percent || 0) >= 90;
  const memHot = app.memory_limit_mb && app.memory_mb / app.memory_limit_mb >= 0.9;
  const diskHot = (app.disk_limit_mb && app.disk_mb / app.disk_limit_mb >= 0.9) || app.over_quota;
  const mb = (used, limit) => (limit ? `${used}/${limit} MB` : `${used} MB`);
  return (
    <div className="ws-resources">
      <Res label="CPU" value={`${app.cpu_percent || 0}%`} hot={cpuHot} />
      <Res label="Disk" value={mb(app.disk_mb, app.disk_limit_mb)} hot={diskHot} />
      <Res label="RAM" value={mb(app.memory_mb, app.memory_limit_mb)} hot={memHot} />
      <Res label="Uptime" value={formatUptime(app.started_at)} />
    </div>
  );
};

// A small dropdown wrapper shared by the Actions and Terminal menus: closes on an
// outside click or Escape.
const useDropdown = () => {
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

// A split button for the terminal: the left half (the icon) opens the web shell
// directly; the right half (the caret) drops a menu (web shell / SSH). When a
// shell session is running -- even minimized out of sight -- the button goes dark
// so the owner can see it is still there, and the icon click brings it back.
const TerminalSplitButton = ({ active, connecting, onWebShell, onSsh }) => {
  const { open, setOpen, ref } = useDropdown();
  const pick = (fn) => {
    setOpen(false);
    fn();
  };
  const cls = ["menu split-btn", active ? "split-btn-active" : "", connecting ? "split-btn-loading" : ""]
    .join(" ")
    .trim();
  return (
    <div className={cls} ref={ref}>
      <button
        type="button"
        className="btn split-btn-main"
        onClick={onWebShell}
        disabled={connecting}
        title={connecting ? "Connecting..." : active ? "Show web shell" : "Open web shell"}
        aria-label={connecting ? "Connecting" : active ? "Show web shell" : "Open web shell"}
      >
        <TerminalIcon />
      </button>
      <button
        type="button"
        className="btn split-btn-caret"
        onClick={() => setOpen(!open)}
        disabled={connecting}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Terminal options"
      >
        <span aria-hidden="true">&#9662;</span>
      </button>
      {open && (
        <div className="menu-items" role="menu">
          <button type="button" role="menuitem" onClick={() => pick(onWebShell)}>
            Web shell
          </button>
          <button type="button" role="menuitem" onClick={() => pick(onSsh)}>
            Connect via SSH
          </button>
        </div>
      )}
    </div>
  );
};

// Start/stop/restart behind one button: only one of them is ever the sensible
// next move. Delete lives here too, set apart at the bottom, so the whole
// rare-actions surface is one menu.
const ActionsMenu = ({ running, appRunning, busy, onAction, onDelete }) => {
  const { open, setOpen, ref } = useDropdown();

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
        className="btn btn-icon"
        onClick={() => setOpen(!open)}
        disabled={busy}
        aria-haspopup="menu"
        aria-expanded={open}
        title={busy ? "Working..." : "Actions"}
        aria-label="Actions"
      >
        <ControlsIcon />
        <span aria-hidden="true">&#9662;</span>
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

// The app-scoped token is created with the app and returned by the API on every
// fetch, so it is always on display; rotating it mints a new one. Lives in the
// "bring your own AI" dialog next to the copy-paste prompt.
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
    <>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {!token && <p className="empty">This app has no owner, so it has no API token.</p>}
      {token && (
        <>
          <p className="hint">
            Scoped to this one app. Anyone holding it can change or delete the app, so treat it like a password.
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
    </>
  );
};

// "Bring your own Claude": the ready-to-paste prompt, the app address and the API
// token, all in one modal reached from the sparkle button. This is where the
// address/token/regenerate controls live now that the page itself is a workspace.
const PromptDialog = ({ app, token, prompt, onRotated, onClose }) => {
  useEscape(onClose);
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <div className="card modal modal-wide" onMouseDown={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h2>Use your own AI agent</h2>
          <button type="button" className="term-btn" onClick={onClose} title="Close" aria-label="Close">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        </div>
        <p className="hint">
          Prefer Claude Code or another agent over the built-in chat? Paste this prompt and it will learn this app's
          API and wait for your instructions.
        </p>
        <div className="term prompt-block">
          <pre>{prompt}</pre>
          <div className="term-copy">
            <CopyButton text={prompt} small={false}>
              Copy prompt
            </CopyButton>
          </div>
        </div>

        <div className="field-row">
          <span className="field-k">Address</span>
          <a className="field-v mono" href={app.url} target="_blank" rel="noreferrer">
            {hostOf(app.url)}
          </a>
          <CopyButton text={app.url} small>
            Copy
          </CopyButton>
        </div>

        <h3 className="modal-sub">API token</h3>
        <ApiAccess name={app.name} token={token} onRotated={onRotated} />
      </div>
    </div>
  );
};

// How to reach the container over SSH: the ready command, an scp hint and a link
// to where the keys that make it work are managed.
const SshDialog = ({ app, hasKeys, onClose }) => {
  useEscape(onClose);
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <div className="card modal" onMouseDown={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h2>Connect via SSH</h2>
          <button type="button" className="term-btn" onClick={onClose} title="Close" aria-label="Close">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        </div>
        {hasKeys === false ? (
          <p className="hint">
            Add an SSH key to your <Link to="/profile">profile</Link> first. Keys there work on every app you own; then
            this command connects you.
          </p>
        ) : (
          <p className="hint">Shell in, or copy files with scp/rsync. The SSH keys on your profile authorize this.</p>
        )}
        {app.ssh && <Snippet text={app.ssh.command} />}
        <p className="hint" style={{ marginTop: "14px" }}>
          Manage the keys that grant access on your{" "}
          <Link to="/profile">SSH keys page</Link>.
        </p>
      </div>
    </div>
  );
};

// Delete behind a type-the-name confirmation, since it takes the container, the
// files and the user with it. A modal, not a section on the page: it is reached
// deliberately from the Actions menu, never stumbled into.
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

// useEscape calls back when Escape is pressed -- shared by the page's modals.
const useEscape = (onClose) => {
  useEffect(() => {
    if (!onClose) return undefined;
    const onKey = (e) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);
};

const AppDetail = ({ account, refreshAccount }) => {
  const { name } = useParams();
  const navigate = useNavigate();
  const [app, setApp] = useState(null);
  const [error, setError] = useState("");
  const [missing, setMissing] = useState(false);
  const [pending, setPending] = useState(null); // an in-flight lifecycle transition, or null
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [termOpen, setTermOpen] = useState(false); // a shell session exists (mounted)
  const [termMin, setTermMin] = useState(false); // ...but hidden out of the way
  const [termConnecting, setTermConnecting] = useState(false); // opening, not yet connected
  const termOpenRef = useRef(false);
  termOpenRef.current = termOpen;
  const [showSsh, setShowSsh] = useState(false);
  const [showPrompt, setShowPrompt] = useState(false);
  const [hasKeys, setHasKeys] = useState(null); // null until we know, so nothing flickers
  const catchUpTimers = useRef([]);

  // The live preview reloads whenever the app is (re)deployed by anything -- the
  // chat's deploy tool, an external `hostit deploy`, a restart. We remount the
  // iframe by bumping its key, and flag "refreshing" until it loads again.
  const [previewKey, setPreviewKey] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const lastAppStart = useRef(null);
  const refreshTimer = useRef(null);

  const reloadPreview = useCallback(() => {
    // Always freshen the URL so the preview is current whenever it is next shown.
    setPreviewKey((k) => k + 1);
    // On small screens the preview is hidden: there is no pane to spin over, and a
    // hidden lazy iframe never fires onLoad, so skip the "refreshing" status there.
    if (window.matchMedia("(max-width: 820px)").matches) {
      return;
    }
    setRefreshing(true);
    clearTimeout(refreshTimer.current);
    // Fallback: some apps never fire load on the iframe; do not spin forever.
    refreshTimer.current = setTimeout(() => setRefreshing(false), 6000);
  }, []);

  // Open the web shell, or bring back a minimized session. The terminal stays
  // mounted while minimized so the shell keeps running, so only a fresh open (not
  // a restore) shows the "connecting" pulse.
  const openWebShell = useCallback(() => {
    if (!termOpenRef.current) {
      setTermConnecting(true);
    }
    setTermOpen(true);
    setTermMin(false);
  }, []);

  // Tear the terminal down: on the user's close, and when the server ends the
  // session (a reboot/poweroff), so the button stops showing a live session.
  const closeTerminal = useCallback(() => {
    setTermOpen(false);
    setTermMin(false);
    setTermConnecting(false);
  }, []);

  // The chat/preview divider position, remembered across reloads.
  const [chatFrac, setChatFrac] = useState(() => {
    const saved = parseFloat(localStorage.getItem(splitKey) || "");
    return saved >= 0.2 && saved <= 0.8 ? saved : defaultChatFrac;
  });
  const chatFracRef = useRef(chatFrac);
  chatFracRef.current = chatFrac;
  const splitRef = useRef(null);

  const startResize = (e) => {
    e.preventDefault();
    let latest = chatFracRef.current; // track here; React state lags a frame behind
    const move = (ev) => {
      const rect = splitRef.current?.getBoundingClientRect();
      if (!rect || rect.width === 0) return;
      const clientX = ev.touches ? ev.touches[0].clientX : ev.clientX;
      latest = Math.min(0.8, Math.max(0.2, (clientX - rect.left) / rect.width));
      setChatFrac(latest);
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.classList.remove("ws-resizing");
      localStorage.setItem(splitKey, String(latest));
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    document.body.classList.add("ws-resizing");
  };

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
    const ticker = setInterval(load, detailPollMs);
    return () => {
      catchUpTimers.current.forEach(clearTimeout);
      clearInterval(ticker);
      clearTimeout(refreshTimer.current);
    };
  }, [load, scheduleCatchUp]);

  // The app process restarting (a deploy, restart or reboot) is our signal to
  // reload the preview: whatever changed is now live. Skip the very first read,
  // which only establishes the baseline.
  useEffect(() => {
    if (!app) return;
    const startedAt = app.app_started_at || 0;
    if (lastAppStart.current === null) {
      lastAppStart.current = startedAt;
      return;
    }
    if (startedAt !== lastAppStart.current) {
      lastAppStart.current = startedAt;
      if (app.running) {
        reloadPreview();
      }
    }
  }, [app, reloadPreview]);

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

  // A cache-busting query on the preview URL, bumped on every reload, so a refresh
  // always fetches the live app rather than the browser's cached copy.
  const previewSrc = app.url + (app.url.includes("?") ? "&" : "?") + "hostit_preview=" + previewKey;

  // "Running" is the unremarkable state, so it is left unsaid; only the states
  // worth noticing get a label.
  const statusText = pending
    ? null
    : refreshing
      ? "Refreshing preview"
      : !app.running
        ? "Powered off"
        : app.app_running
          ? ""
          : "App stopped";
  const statusPending = !!pending || refreshing;

  return (
    <>
      <div className="ws-page">
        {/* Top bar. Left: identity (the "Running" state is left unsaid -- only the
            notable states are named). Right: the live resources beside the
            controls, all vertically centred. */}
        <div className="ws-topbar">
          <Link to="/" className="ws-back" aria-label="Back to apps" title="Back to apps">
            <BackIcon />
          </Link>
          <div className="ws-idrow">
            <StatusDot running={app.running} appRunning={app.app_running} pending={statusPending} />
            <span className="ws-name">{app.name}</span>
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
              statusText && (
                <span className={"status-label" + (refreshing ? " status-label-pending" : "")}>{statusText}</span>
              )
            )}
          </div>

          <div className="ws-topright">
            {app.running && <UsageGrid app={app} />}
            <div className="ws-topacts">
              {app.running && (
                <TerminalSplitButton
                  active={termOpen && !termConnecting}
                  connecting={termConnecting}
                  onWebShell={openWebShell}
                  onSsh={() => setShowSsh(true)}
                />
              )}
              <button
                type="button"
                className="btn btn-icon btn-sparkle"
                onClick={() => setShowPrompt(true)}
                title="Use your own AI agent"
                aria-label="Use your own AI agent"
              >
                <SparkleIcon />
              </button>
              <ActionsMenu
                running={app.running}
                appRunning={app.app_running}
                busy={!!pending}
                onAction={lifecycle}
                onDelete={() => setConfirmDelete(true)}
              />
              <a className="btn btn-primary" href={app.url} target="_blank" rel="noreferrer">
                Open app &#8599;
              </a>
            </div>
          </div>
        </div>
        <ErrorBanner message={error} onDismiss={() => setError("")} />

        {/* The workspace: build on the left, watch it change on the right, with a
            draggable divider between them. */}
        <div
          className="ws-split"
          ref={splitRef}
          style={{ gridTemplateColumns: `minmax(0, ${chatFrac}fr) 10px minmax(0, ${1 - chatFrac}fr)` }}
        >
          <div className="ws-chat">
            <Suspense fallback={<div className="ws-chat-loading">Loading assistant...</div>}>
              <AppAssistant name={app.name} embedded onPreviewRefresh={reloadPreview} />
            </Suspense>
          </div>

          <div
            className="ws-resizer"
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize chat and preview"
            onPointerDown={startResize}
            onTouchStart={startResize}
          >
            <span className="ws-resizer-grip" aria-hidden="true" />
          </div>

          <div className="ws-right">
            {app.running ? (
              <div className="ws-preview">
                {refreshing && (
                  <div className="ws-preview-badge">
                    <span className="ws-preview-spinner" aria-hidden="true" /> Refreshing
                  </div>
                )}
                {/* The app runs in its own origin; sandbox keeps it from navigating
                    the dashboard away or opening windows. A per-reload cache-buster
                    on the URL forces a real fetch: Go's file server sets no
                    Cache-Control, so a plain reload would serve the browser's stale
                    copy and a "refresh" would appear to do nothing (or revert). */}
                <iframe
                  key={previewKey}
                  title={`Live preview of ${app.name}`}
                  src={previewSrc}
                  loading="lazy"
                  sandbox="allow-scripts allow-same-origin allow-forms"
                  onLoad={() => setRefreshing(false)}
                />
              </div>
            ) : (
              <div className="ws-preview ws-preview-off">
                <p>
                  The app is powered off. Use <strong>Actions</strong> to power it on, then it shows here.
                </p>
              </div>
            )}
          </div>
        </div>
      </div>

      {termOpen && (
        <Suspense fallback={null}>
          <AppTerminal
            name={app.name}
            minimized={termMin}
            onReady={() => setTermConnecting(false)}
            onMinimize={() => setTermMin(true)}
            onClose={closeTerminal}
            onSessionEnd={closeTerminal}
          />
        </Suspense>
      )}
      {showSsh && <SshDialog app={app} hasKeys={hasKeys} onClose={() => setShowSsh(false)} />}
      {showPrompt && (
        <PromptDialog app={app} token={token} prompt={prompt} onRotated={setApp} onClose={() => setShowPrompt(false)} />
      )}
      {confirmDelete && (
        <DeleteAppDialog name={app.name} onCancel={() => setConfirmDelete(false)} onDeleted={deleted} />
      )}
    </>
  );
};

// hostOf returns just the hostname of a URL, for a compact address display
const hostOf = (url) => {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
};

export default AppDetail;
