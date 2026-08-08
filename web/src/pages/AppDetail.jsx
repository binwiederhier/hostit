import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, ApiError, isNetworkError } from "../api";
import { useReconnect } from "../hooks";
import { CopyButton, ErrorBanner, Loading, Snippet, StatusDot } from "../components";
import { useSetAppHeader } from "../appHeader";

// xterm is heavy and only needed when a terminal is actually opened, so it is
// split into its own chunk and loaded on demand.
const AppTerminal = lazy(() => import("./AppTerminal"));
// The assistant pulls in a markdown renderer, so it stays a lazy chunk too.
const AppAssistant = lazy(() => import("./AppAssistant"));
// The file-tree editor is its own view, loaded on demand when selected.
const AppEditor = lazy(() => import("./AppEditor"));

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
const SnapshotIcon = () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <circle cx="8" cy="8.4" r="5.4" />
    <path d="M8 5.4v3l2.1 1.3" />
    <path d="M8 1.6v1.4" />
  </svg>
);
const SparkleIcon = () => (
  <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
    <path d="M8 0.8l1.5 4.2 4.2 1.5-4.2 1.5L8 12.2 6.5 8 2.3 6.5 6.5 5z" />
    <path d="M13 9.5l0.6 1.6 1.6 0.6-1.6 0.6L13 14l-0.6-1.7-1.6-0.6 1.6-0.6z" />
  </svg>
);

// Small monochrome glyphs for the dropdown menu items.
const mi = (paths) => () => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {paths}
  </svg>
);
const PlayIcon = mi(<path d="M5 3.5v9l7-4.5z" />);
const StopIcon = mi(<rect x="4" y="4" width="8" height="8" rx="1" />);
const RestartIcon = mi(<><path d="M13 8a5 5 0 1 1-1.5-3.5" /><path d="M13 3v2.2h-2.2" /></>);
const RebootIcon = mi(<><path d="M3.2 8a4.8 4.8 0 1 0 1.4-3.4" /><path d="M4.6 2.3v2.3h2.3" /><path d="M8 5v3" /></>);
const PowerIcon = mi(<><path d="M8 2.2v5" /><path d="M4.6 4.6a4.6 4.6 0 1 0 6.8 0" /></>);
const GearIcon = mi(<><circle cx="8" cy="8" r="2.1" /><path d="M8 1.7v1.6M8 12.7v1.6M14.3 8h-1.6M3.3 8H1.7M12.4 3.6l-1.1 1.1M4.7 11.3l-1.1 1.1M12.4 12.4l-1.1-1.1M4.7 4.7 3.6 3.6" /></>);
const TrashIcon = mi(<><path d="M3 4.5h10" /><path d="M6.5 4.5V3h3v1.5" /><path d="M4.5 4.5l.6 8.5a1 1 0 0 0 1 .9h3.8a1 1 0 0 0 1-.9l.6-8.5" /></>);
const PlusIcon = mi(<path d="M8 3.5v9M3.5 8h9" />);
const ForkIcon = mi(<><circle cx="4.5" cy="4" r="1.6" /><circle cx="11.5" cy="4" r="1.6" /><circle cx="8" cy="12.5" r="1.6" /><path d="M4.5 5.6v1.4a2 2 0 0 0 2 2H8m3.5-3.4v1.4a2 2 0 0 1-2 2H8m0 0v1.9" /></>);
const KeyIcon = mi(<><circle cx="5" cy="11" r="2.3" /><path d="M6.7 9.3l5-5M10.7 5.3l1.4 1.4M12.4 3.6l1.4 1.4" /></>);
const RollbackIcon = mi(<><path d="M4.4 5.2A4.7 4.7 0 1 1 3.4 8.2" /><path d="M1.9 2.9v2.7h2.7" /></>);

// CopyMini is a small two-document copy icon that copies one value and briefly shows
// a check. Used next to each DNS record field so an owner can copy just the name or
// just the value into their DNS provider.
const CopyMini = ({ text, label }) => {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="copy-mini"
      title={label || "Copy"}
      aria-label={label || "Copy"}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        } catch {
          /* clipboard unavailable */
        }
      }}
    >
      {done ? (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M3.5 8.5l3 3 6-7" />
        </svg>
      ) : (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="5.5" y="5.5" width="8" height="8.5" rx="1.3" />
          <path d="M3.5 10.5V3.2a1 1 0 0 1 1-1H10" />
        </svg>
      )}
    </button>
  );
};

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

// resLevel maps a usage percent to a severity, which colours the dot and bar.
const resLevel = (pct) => (pct >= 90 ? "hot" : pct >= 75 ? "warn" : "good");

// One compact inline stat: a level-coloured dot, label and value, with a thin bar
// under it. `detail` is the hover tooltip (the exact used/total for RAM/disk).
// The unmetered ones (uptime) skip the dot/bar but keep the height, so the row
// stays aligned.
const Stat = ({ label, pct = 0, value, detail, metered = true }) => {
  const lvl = resLevel(pct);
  return (
    <div className={"res" + (metered ? " res-" + lvl : "")} title={detail}>
      <span className="res-top">
        {metered && <span className="res-dot" aria-hidden="true" />}
        <span className="res-k">{label}</span>
        <span className={"res-v" + (metered && lvl === "hot" ? " res-v-hot" : "")}>{value}</span>
      </span>
      <span className={"res-bar" + (metered ? "" : " res-bar-empty")}>
        {metered && <span style={{ width: `${Math.min(100, pct)}%` }} />}
      </span>
    </div>
  );
};

// CPU / RAM / Disk / Uptime as a compact inline row beside the controls. RAM and
// disk read as percent; the exact used/total is in the hover tooltip. Only
// meaningful while the container is up, so the caller shows it only then.
const UsageGrid = ({ app }) => {
  const cpuPct = app.cpu_percent || 0;
  const memPct = app.memory_limit_mb ? Math.round((app.memory_mb / app.memory_limit_mb) * 100) : 0;
  const diskPct = app.disk_limit_mb ? Math.round((app.disk_mb / app.disk_limit_mb) * 100) : 0;
  const mb = (used, limit) => (limit ? `${used} / ${limit} MB` : `${used} MB`);
  return (
    <div className="ws-resources">
      <Stat label="CPU" pct={cpuPct} value={`${cpuPct}%`} detail={`CPU ${cpuPct}%`} />
      <Stat label="RAM" pct={memPct} value={`${memPct}%`} detail={`RAM ${mb(app.memory_mb, app.memory_limit_mb)}`} />
      <Stat label="Disk" pct={diskPct} value={`${diskPct}%`} detail={`Disk ${mb(app.disk_mb, app.disk_limit_mb)}`} />
      <Stat label="Uptime" value={formatUptime(app.started_at)} detail={`Up ${formatUptime(app.started_at)}`} metered={false} />
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
          <MenuItem icon={<TerminalIcon />} label="Web shell" onClick={() => pick(onWebShell)} />
          <MenuItem icon={<KeyIcon />} label="Connect via SSH" onClick={() => pick(onSsh)} />
        </div>
      )}
    </div>
  );
};

// A split button for snapshots, mirroring the terminal one: the left half (the
// icon) opens the snapshots list; the caret drops a menu whose second item, "New
// snapshot...", opens a dialog to name and take one.
const SnapshotSplitButton = ({ onList, onNew, onFork }) => {
  const { open, setOpen, ref } = useDropdown();
  const pick = (fn) => {
    setOpen(false);
    fn();
  };
  return (
    <div className="menu split-btn" ref={ref}>
      <button
        type="button"
        className="btn split-btn-main"
        onClick={onList}
        title="Snapshots"
        aria-label="Snapshots"
      >
        <SnapshotIcon />
      </button>
      <button
        type="button"
        className="btn split-btn-caret"
        onClick={() => setOpen(!open)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Snapshot options"
      >
        <span aria-hidden="true">&#9662;</span>
      </button>
      {open && (
        <div className="menu-items" role="menu">
          <MenuItem icon={<SnapshotIcon />} label="Snapshots" onClick={() => pick(onList)} />
          <MenuItem icon={<PlusIcon />} label="New snapshot" onClick={() => pick(onNew)} />
          <div className="menu-sep" />
          <MenuItem icon={<ForkIcon />} label="Fork app" onClick={() => pick(onFork)} />
        </div>
      )}
    </div>
  );
};

// MenuItem is one dropdown row: a small leading icon and a label.
const MenuItem = ({ icon, label, onClick, disabled, danger }) => (
  <button
    type="button"
    role="menuitem"
    className={danger ? "menu-item-danger" : undefined}
    onClick={onClick}
    disabled={disabled}
  >
    <span className="menu-ico" aria-hidden="true">
      {icon}
    </span>
    {label}
  </button>
);

// Everything rare about an app behind one button, grouped: app actions (the run:
// command), container actions (power), settings (token + custom domains), and
// delete -- dividers between the groups. Only one app verb is ever the sensible
// next move, and when the container is off there is no app to act on, so that
// group is dropped.
const ActionsMenu = ({ running, appRunning, busy, onAction, onSettings, onDelete }) => {
  const { open, setOpen, ref } = useDropdown();

  const run = (action) => {
    setOpen(false);
    onAction(action);
  };
  const pick = (fn) => () => {
    setOpen(false);
    fn();
  };
  const appVerbs = appRunning
    ? [
        { verb: "restart", label: "Restart app", icon: <RestartIcon /> },
        { verb: "stop", label: "Stop app", icon: <StopIcon /> },
      ]
    : [{ verb: "start", label: "Start app", icon: <PlayIcon /> }];

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
          {running && (
            <>
              {appVerbs.map((a) => (
                <MenuItem key={a.verb} icon={a.icon} label={a.label} onClick={() => run(a.verb)} />
              ))}
              <div className="menu-sep" />
            </>
          )}

          {running ? (
            <>
              <MenuItem icon={<RebootIcon />} label="Reboot" onClick={() => run("reboot")} />
              <MenuItem icon={<PowerIcon />} label="Power off" onClick={() => run("poweroff")} />
            </>
          ) : (
            <MenuItem icon={<PowerIcon />} label="Power on" onClick={() => run("poweron")} />
          )}
          <div className="menu-sep" />

          <MenuItem icon={<GearIcon />} label="Settings" onClick={pick(onSettings)} />
          <div className="menu-sep" />

          <MenuItem icon={<TrashIcon />} label="Delete app" onClick={pick(onDelete)} danger />
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

// "Bring your own Claude": just the ready-to-paste prompt (which already carries
// the app's URL and token). The token's own copy/regenerate controls live in the
// Actions menu now.
const PromptDialog = ({ prompt, token, onClose }) => {
  // Show the token masked on screen (shoulder-surfing), but copy the real prompt.
  const shown = token ? prompt.split(token).join("*".repeat(8)) : prompt;
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
          <pre>{shown}</pre>
          <div className="term-copy">
            <CopyButton text={prompt} small={false}>
              Copy prompt
            </CopyButton>
          </div>
        </div>
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
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <input
          type="text"
          className="modal-input-full"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          placeholder={`Type '${name}' to confirm`}
          aria-label={`Type '${name}' to confirm deletion`}
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

// ConfirmDialog is a small yes/no modal that explains what an action will do.
const ConfirmDialog = ({ title, children, confirmLabel, danger, busy, onConfirm, onCancel }) => {
  useEscape(onCancel);
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onCancel}>
      <div className="card modal" onMouseDown={(e) => e.stopPropagation()}>
        <h2>{title}</h2>
        <div className="confirm-body">{children}</div>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button
            type="button"
            className={"btn " + (danger ? "btn-danger" : "btn-primary")}
            onClick={onConfirm}
            disabled={busy}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
};

// NewSnapshotDialog names and takes a manual snapshot. Reached from the snapshot
// split button's caret; the name is optional but helps the owner find it later.
const NewSnapshotDialog = ({ name, onClose, onCreated, showToast }) => {
  useEscape(onClose);
  const [label, setLabel] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const create = async (e) => {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await api.post(`/api/apps/${encodeURIComponent(name)}/snapshots`, { label: label.trim() });
      showToast("Snapshot saved");
      onClose();
      if (onCreated) onCreated();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <form className="card modal" onSubmit={create} onMouseDown={(e) => e.stopPropagation()}>
        <h2>New snapshot</h2>
        <p className="hint">
          Save a point-in-time copy of <span className="mono">{name}</span>'s files you can roll back to. A name is
          optional.
        </p>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <input
          type="text"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="e.g. before the big refactor"
          aria-label="Snapshot name"
          maxLength={200}
          autoFocus
        />
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={busy}>
            {busy ? "Saving..." : "Take snapshot"}
          </button>
        </div>
      </form>
    </div>
  );
};

// ForkDialog duplicates the app into a new one, seeding its home from the source's
// current files -- or, when snapshotId is set, from that snapshot. Reached from the
// snapshot split button's caret, or a snapshot row's Fork action.
const ForkDialog = ({ name, snapshotId, onClose, onForked }) => {
  useEscape(onClose);
  const [newName, setNewName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const fork = async (e) => {
    e.preventDefault();
    const target = newName.trim();
    if (busy || !target) return;
    setBusy(true);
    setError("");
    try {
      const created = await api.post(`/api/apps/${encodeURIComponent(name)}/fork`, {
        new_name: target,
        snapshot_id: snapshotId || undefined,
      });
      onForked(created);
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <form className="card modal" onSubmit={fork} onMouseDown={(e) => e.stopPropagation()}>
        <h2>Fork {name}</h2>
        <p className="hint">
          {snapshotId ? (
            <>
              Create a new app seeded from snapshot <span className="mono">{snapshotId}</span> of{" "}
              <span className="mono">{name}</span>.
            </>
          ) : (
            <>
              Create a new app seeded from a copy of <span className="mono">{name}</span>'s current files.
            </>
          )}{" "}
          It gets its own subdomain, user and container; the two run independently from here on.
        </p>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <input
          type="text"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          placeholder="new app name"
          aria-label="New app name"
          autoFocus
        />
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={busy || !newName.trim()}>
            {busy ? "Forking..." : "Fork app"}
          </button>
        </div>
      </form>
    </div>
  );
};

// SnapshotsDialog lists an app's point-in-time snapshots and rolls back to (or
// deletes) any of them. A rollback is reversible (a safety snapshot of the live
// state is taken first), so it runs on one click, no type-to-confirm gate. New
// snapshots are taken from the split button's caret, not here.
const SnapshotsDialog = ({ name, onClose, showToast, onRolledBack, onFork }) => {
  useEscape(onClose);
  const [snaps, setSnaps] = useState(null); // null until loaded
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const list = await api.get(`/api/apps/${encodeURIComponent(name)}/snapshots`);
      setSnaps(Array.isArray(list) ? list : []);
    } catch (err) {
      setError(err.message);
      setSnaps([]);
    }
  }, [name]);
  useEffect(() => {
    load();
  }, [load]);

  const [confirm, setConfirm] = useState(null); // { type: "rollback" | "delete", snap }

  const doRollback = async () => {
    const id = confirm?.snap?.id;
    if (!id || busy) return;
    setBusy(true);
    setError("");
    showToast("Rolling back..."); // before the (synchronous, possibly slow) request
    try {
      await api.post(`/api/apps/${encodeURIComponent(name)}/snapshots/${encodeURIComponent(id)}/restore`, {});
      onClose();
      if (onRolledBack) onRolledBack();
    } catch (err) {
      setError(err.message);
      setBusy(false);
      setConfirm(null);
    }
  };

  const doRemove = async () => {
    const id = confirm?.snap?.id;
    if (!id || busy) return;
    setBusy(true);
    setError("");
    try {
      await api.del(`/api/apps/${encodeURIComponent(name)}/snapshots/${encodeURIComponent(id)}`);
      showToast("Snapshot deleted");
      setConfirm(null);
      await load();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  const when = (snap) => new Date(snap.created_at).toLocaleString();

  return (
    <>
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <div className="card modal modal-xwide" onMouseDown={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h2>
            Snapshots{snaps && snaps.length > 0 ? <span className="snap-count"> {snaps.length}</span> : ""}
          </h2>
          <button type="button" className="term-btn" onClick={onClose} title="Close" aria-label="Close">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        </div>
        <p className="hint">
          Point-in-time copies of <span className="mono">{name}</span>'s files. Rolling back restores it to that point --
          reversible, since a snapshot of the current state is taken first.
        </p>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        {snaps === null ? (
          <p className="hint">Loading...</p>
        ) : snaps.length === 0 ? (
          <p className="hint">No snapshots yet.</p>
        ) : (
          <div className="snap-list">
            {snaps.map((s) => (
              <div className="snap-row" key={s.id}>
                <div className="snap-meta">
                  <span className="snap-when">{new Date(s.created_at).toLocaleString()}</span>
                  <span className={"snap-kind" + (s.auto ? "" : " snap-kind-manual")}>{s.auto ? "auto" : "manual"}</span>
                  {s.label && <span className="snap-label">{s.label}</span>}
                </div>
                <div className="snap-actions">
                  <button
                    type="button"
                    className="btn btn-small btn-icon"
                    onClick={() => setConfirm({ type: "rollback", snap: s })}
                    disabled={busy}
                    title="Roll back to this snapshot"
                    aria-label="Roll back to this snapshot"
                  >
                    <RollbackIcon />
                  </button>
                  <button
                    type="button"
                    className="btn btn-small btn-icon"
                    onClick={() => onFork(s.id)}
                    disabled={busy}
                    title="Fork a new app from this snapshot"
                    aria-label="Fork a new app from this snapshot"
                  >
                    <ForkIcon />
                  </button>
                  <button
                    type="button"
                    className="btn btn-small btn-icon menu-item-danger"
                    onClick={() => setConfirm({ type: "delete", snap: s })}
                    disabled={busy}
                    title="Delete this snapshot"
                    aria-label="Delete this snapshot"
                  >
                    <TrashIcon />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>

    {confirm?.type === "rollback" && (
      <ConfirmDialog
        title="Roll back to this snapshot?"
        confirmLabel={busy ? "Rolling back..." : "Roll back"}
        busy={busy}
        onCancel={() => setConfirm(null)}
        onConfirm={doRollback}
      >
        <p>
          Restore <span className="mono">{name}</span>'s files to <strong>{when(confirm.snap)}</strong>
          {confirm.snap.label ? <> (&ldquo;{confirm.snap.label}&rdquo;)</> : null}.
        </p>
        <p>
          A snapshot of the <strong>current</strong> state is taken first, so this is reversible -- you can roll back to
          that if you change your mind.
        </p>
      </ConfirmDialog>
    )}
    {confirm?.type === "delete" && (
      <ConfirmDialog
        title="Delete this snapshot?"
        confirmLabel="Delete"
        danger
        busy={busy}
        onCancel={() => setConfirm(null)}
        onConfirm={doRemove}
      >
        <p>
          Permanently delete the snapshot from <strong>{when(confirm.snap)}</strong>
          {confirm.snap.label ? <> (&ldquo;{confirm.snap.label}&rdquo;)</> : null}.
        </p>
        <p>
          <strong>This cannot be undone.</strong>
        </p>
      </ConfirmDialog>
    )}
    </>
  );
};

// SettingsDialog holds an app's settings: its API token and its custom domains.
// Each domain shows the two DNS records the owner must create (one to route
// traffic, one to delegate the ACME challenge so a certificate issues even when the
// box is not publicly reachable).
const SettingsDialog = ({ name, description, hasToken, onCopyToken, onRegenerateToken, onSaved, onClose, showToast }) => {
  useEscape(onClose);
  const [domains, setDomains] = useState(null); // null until loaded
  const [input, setInput] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmId, setConfirmId] = useState(null);
  const [desc, setDesc] = useState(description || "");
  const [savingDesc, setSavingDesc] = useState(false);

  const saveDescription = async () => {
    if (savingDesc) return;
    setSavingDesc(true);
    setError("");
    try {
      await api.put(`/api/apps/${encodeURIComponent(name)}/description`, { description: desc.trim() });
      showToast("Description saved");
      if (onSaved) onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setSavingDesc(false);
    }
  };

  const load = useCallback(async () => {
    try {
      const list = await api.get(`/api/apps/${encodeURIComponent(name)}/domains`);
      setDomains(Array.isArray(list) ? list : []);
    } catch (err) {
      setError(err.message);
      setDomains([]);
    }
  }, [name]);
  useEffect(() => {
    load();
  }, [load]);

  // While any domain is not yet active, refresh the list periodically so its status
  // updates on its own -- the server retries issuance every minute once DNS is set up.
  const anyPending = domains && domains.some((d) => d.status !== "active");
  useEffect(() => {
    if (!anyPending) return undefined;
    const id = setInterval(load, 20000);
    return () => clearInterval(id);
  }, [anyPending, load]);

  const add = async (e) => {
    e.preventDefault();
    const domain = input.trim().toLowerCase();
    if (busy || !domain) return;
    setBusy(true);
    setError("");
    try {
      await api.post(`/api/apps/${encodeURIComponent(name)}/domains`, { domain });
      setInput("");
      showToast("Domain added; create the DNS records");
      await load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const verify = async (domain) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await api.post(`/api/apps/${encodeURIComponent(name)}/domains/${encodeURIComponent(domain)}/verify`, {});
      showToast("Checking DNS...");
      setTimeout(load, 1500); // give issuance a moment, then refresh
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (domain) => {
    if (busy) return;
    setConfirmId(null);
    setBusy(true);
    setError("");
    try {
      await api.del(`/api/apps/${encodeURIComponent(name)}/domains/${encodeURIComponent(domain)}`);
      showToast("Domain removed");
      await load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <div className="card modal modal-wide" onMouseDown={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h2>Settings</h2>
          <button type="button" className="term-btn" onClick={onClose} title="Close" aria-label="Close">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        </div>
        <ErrorBanner message={error} onDismiss={() => setError("")} />

        <section className="settings-section">
          <h3>Description</h3>
          <p className="hint">
            A one-line summary of the app -- shown on the dashboard and where the assistant starts from. Saved to{" "}
            <span className="mono">hostit.yml</span>.
          </p>
          <textarea
            className="settings-desc"
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
            rows={2}
            maxLength={280}
            placeholder="What this app is..."
          />
          <div className="btn-row" style={{ justifyContent: "flex-start" }}>
            <button
              type="button"
              className="btn btn-small btn-primary"
              onClick={saveDescription}
              disabled={savingDesc || desc.trim() === (description || "").trim()}
            >
              {savingDesc ? "Saving..." : "Save description"}
            </button>
          </div>
        </section>

        <section className="settings-section">
          <h3>API token</h3>
          <p className="hint">The app-scoped token an external agent (Claude Code, etc.) uses to work on this app.</p>
          <div className="btn-row" style={{ justifyContent: "flex-start" }}>
            <button type="button" className="btn btn-small" onClick={onCopyToken} disabled={!hasToken}>
              Copy token
            </button>
            <button type="button" className="btn btn-small" onClick={onRegenerateToken}>
              Regenerate token
            </button>
          </div>
        </section>

        <section className="settings-section">
          <h3>Custom domains</h3>
          <p className="hint">
            Serve <span className="mono">{name}</span> on your own hostname. Add it, then create the two DNS records
            shown; the certificate issues automatically (works even if this server is not publicly reachable).
          </p>
          <form className="domain-add" onSubmit={add}>
          <input
            type="text"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            placeholder="blog.example.com"
            aria-label="Custom domain"
            autoFocus
          />
          <button type="submit" className="btn btn-primary btn-small" disabled={busy || !input.trim()}>
            Add domain
          </button>
        </form>
        {domains === null ? (
          <p className="hint">Loading...</p>
        ) : domains.length === 0 ? (
          <p className="hint">No custom domains yet.</p>
        ) : (
          <div className="domain-list">
            {domains.map((d) => (
              <div className="domain-row" key={d.domain}>
                <div className="domain-head">
                  <span className="domain-name">{d.domain}</span>
                  <span className={"domain-status domain-" + d.status}>{d.status}</span>
                  <span className="domain-actions">
                    <button type="button" className="btn btn-small" onClick={() => verify(d.domain)} disabled={busy}>
                      {d.status === "active" ? "Re-check" : "Verify"}
                    </button>
                    {confirmId === d.domain ? (
                      <button type="button" className="btn btn-small btn-danger" onClick={() => remove(d.domain)} disabled={busy}>
                        Confirm remove
                      </button>
                    ) : (
                      <button type="button" className="btn btn-small" onClick={() => setConfirmId(d.domain)} disabled={busy}>
                        Remove
                      </button>
                    )}
                  </span>
                </div>
                {d.last_error && <div className="domain-error">{d.last_error}</div>}
                {d.status !== "active" && d.dns && (
                  <div className="domain-dns">
                    {d.dns.map((r) => (
                      <div className="dns-rec" key={r.name}>
                        <span className="dns-type">{r.type}</span>
                        <code className="dns-name">{r.name}</code>
                        <CopyMini text={r.name} label="Copy record name" />
                        <span className="dns-arrow" aria-hidden="true">-&gt;</span>
                        <code className="dns-value">{r.value}</code>
                        <CopyMini text={r.value} label="Copy record value" />
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
        </section>
      </div>
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
  const [termOpen, setTermOpen] = useState(false); // a shell session exists (mounted)
  const [termMin, setTermMin] = useState(false); // ...but hidden out of the way
  const [termConnecting, setTermConnecting] = useState(false); // opening, not yet connected
  const termOpenRef = useRef(false);
  termOpenRef.current = termOpen;
  const [showSsh, setShowSsh] = useState(false);
  const [showPrompt, setShowPrompt] = useState(false);
  const [showSnapshots, setShowSnapshots] = useState(false);
  const [showNewSnapshot, setShowNewSnapshot] = useState(false);
  const [showFork, setShowFork] = useState(false);
  const [forkSnapshotId, setForkSnapshotId] = useState(null); // null = fork from current state
  const [showSettings, setShowSettings] = useState(false);
  const [hasKeys, setHasKeys] = useState(null); // null until we know, so nothing flickers
  const [toast, setToast] = useState(""); // a 3s "Copied"/"Regenerated" snackbar
  const [view, setView] = useState("assistant"); // "assistant" (chat + preview) | "editor" (file tree)
  const toastTimer = useRef(null);
  const catchUpTimers = useRef([]);

  // showToast flashes a message at the bottom for 3 seconds, then clears it.
  const showToast = useCallback((message) => {
    setToast(message);
    clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(""), 3000);
  }, []);

  // Copy the current app token to the clipboard; regenerate mints a new one, points
  // the page at it, and copies that. Both flash a snackbar.
  const copyToken = useCallback(async () => {
    const t = app?.agent_token;
    if (!t) return;
    try {
      await navigator.clipboard.writeText(t);
      showToast("Copied to clipboard");
    } catch {
      showToast("Copy failed");
    }
  }, [app, showToast]);

  const regenerateToken = useCallback(async () => {
    try {
      const fresh = await api.post(`/api/apps/${encodeURIComponent(name)}/token`);
      setApp(fresh);
      try {
        await navigator.clipboard.writeText(fresh.agent_token || "");
      } catch {
        // clipboard may be unavailable; the token still rotated
      }
      showToast("Regenerated and copied");
    } catch (err) {
      setError(err.message);
    }
  }, [name, showToast]);

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

  // Publish this app's identity to the nav, so on phones it can show the back
  // button and name in place of the logo (a single top bar). Clear it on the way
  // out.
  const setAppHeader = useSetAppHeader();
  useEffect(() => {
    if (app) {
      setAppHeader({ name: app.name, running: app.running, appRunning: app.app_running, pending: !!pending || refreshing });
    }
  }, [app, pending, refreshing, setAppHeader]);
  useEffect(() => () => setAppHeader(null), [setAppHeader]);

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
    if (!navigator.onLine) return; // offline: don't hammer, wait for reconnect
    try {
      const fresh = await api.get(`/api/apps/${encodeURIComponent(name)}`);
      setApp(fresh);
      setMissing(false);
      setError(""); // a good read heals any transient error banner
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setMissing(true);
      } else if (isNetworkError(err)) {
        // A transient network blip -- the app or daemon restarting, a wifi hiccup.
        // Keep showing the last known state and let the next poll recover, rather
        // than flashing a scary "Network error" banner on every restart.
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
      clearTimeout(toastTimer.current);
    };
  }, [load, scheduleCatchUp]);
  useReconnect(load); // refresh when connectivity or visibility returns

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
            <span className="ws-name">{app.name}</span>
            <StatusDot running={app.running} appRunning={app.app_running} pending={statusPending} />
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
              {app.snapshots_enabled && (
                <SnapshotSplitButton
                  onList={() => setShowSnapshots(true)}
                  onNew={() => setShowNewSnapshot(true)}
                  onFork={() => {
                    setForkSnapshotId(null);
                    setShowFork(true);
                  }}
                />
              )}
              <ActionsMenu
                running={app.running}
                appRunning={app.app_running}
                busy={!!pending}
                onAction={lifecycle}
                onSettings={() => setShowSettings(true)}
                onDelete={() => setConfirmDelete(true)}
              />
              <a className="btn btn-primary" href={app.url} target="_blank" rel="noreferrer" title="Open app">
                <span className="ws-open-label">Open app</span> <span aria-hidden="true">&#8599;</span>
              </a>
            </div>
          </div>
        </div>
        <ErrorBanner message={error} onDismiss={() => setError("")} />

        {/* View switcher: the chat + preview split is one view; the file editor is
            another. More can join as tabs (terminal, details) later. */}
        <div className="ws-viewtabs" role="tablist" aria-label="Workspace view">
          <button
            type="button"
            role="tab"
            aria-selected={view === "assistant"}
            className={"ws-viewtab" + (view === "assistant" ? " on" : "")}
            onClick={() => setView("assistant")}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
              <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
            </svg>
            Assistant
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={view === "editor"}
            className={"ws-viewtab" + (view === "editor" ? " on" : "")}
            onClick={() => setView("editor")}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
              <path d="m9 8-5 4 5 4M15 8l5 4-5 4" />
            </svg>
            Editor
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={view === "terminal"}
            className={"ws-viewtab" + (view === "terminal" ? " on" : "")}
            onClick={() => setView("terminal")}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
              <path d="M4 5h16v14H4z" />
              <path d="m8 10 3 2-3 2M13 16h4" />
            </svg>
            Terminal
          </button>
        </div>

        {/* All three views stay mounted -- only the active one is shown -- so
            switching is instant and the terminal keeps its live session (and the
            assistant its scroll) instead of reconnecting on every tab click. */}
        <div className={"ws-editorwrap" + (view === "editor" ? "" : " ws-inactive")}>
          <Suspense fallback={<div className="ws-chat-loading">Loading editor...</div>}>
            <AppEditor name={app.name} url={app.url} running={app.running} onDeploy={reloadPreview} />
          </Suspense>
        </div>
        <div className={"ws-termwrap" + (view === "terminal" ? "" : " ws-inactive")}>
          <Suspense fallback={<div className="ws-chat-loading">Loading terminal...</div>}>
            <AppTerminal name={app.name} embedded />
          </Suspense>
        </div>
        {/* The workspace: build on the left, watch it change on the right, with a
            draggable divider between them. */}
        <div
          className={"ws-split" + (view === "assistant" ? "" : " ws-inactive")}
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
      {showPrompt && <PromptDialog prompt={prompt} token={token} onClose={() => setShowPrompt(false)} />}
      {showSnapshots && (
        <SnapshotsDialog
          name={app.name}
          onClose={() => setShowSnapshots(false)}
          showToast={showToast}
          onRolledBack={load}
          onFork={(snapshotId) => {
            setForkSnapshotId(snapshotId);
            setShowFork(true); // opens the fork dialog on top; the snapshots list stays open
          }}
        />
      )}
      {showNewSnapshot && (
        <NewSnapshotDialog name={app.name} onClose={() => setShowNewSnapshot(false)} showToast={showToast} />
      )}
      {showSettings && (
        <SettingsDialog
          name={app.name}
          description={app.description}
          hasToken={!!token}
          onCopyToken={copyToken}
          onRegenerateToken={regenerateToken}
          onSaved={load}
          showToast={showToast}
          onClose={() => setShowSettings(false)}
        />
      )}
      {showFork && (
        <ForkDialog
          name={app.name}
          snapshotId={forkSnapshotId}
          onClose={() => setShowFork(false)}
          onForked={(created) => {
            setShowFork(false);
            navigate(`/app/${encodeURIComponent(created.name)}`);
          }}
        />
      )}
      {confirmDelete && (
        <DeleteAppDialog name={app.name} onCancel={() => setConfirmDelete(false)} onDeleted={deleted} />
      )}
      {toast && (
        <div className="snackbar" role="status" aria-live="polite">
          {toast}
        </div>
      )}
    </>
  );
};

export default AppDetail;
