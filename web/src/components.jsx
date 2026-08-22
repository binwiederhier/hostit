import { useState, useRef, useEffect } from "react";

// Copies text to the clipboard with a "Copied!" confirmation; falls back to
// a hidden textarea + execCommand for non-secure contexts.
export const CopyButton = ({ text, small = true, disabled = false, children }) => {
  const [copied, setCopied] = useState(false);
  const timer = useRef(null);
  useEffect(() => () => clearTimeout(timer.current), []);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const el = document.createElement("textarea");
      el.value = text;
      el.style.position = "fixed";
      el.style.opacity = "0";
      document.body.appendChild(el);
      el.select();
      try {
        document.execCommand("copy");
      } finally {
        document.body.removeChild(el);
      }
    }
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  };
  return (
    <button type="button" className={`btn${small ? " btn-small" : ""}`} onClick={copy} disabled={disabled}>
      {copied ? "Copied!" : children || "Copy"}
    </button>
  );
};

// Green when the app's run: process is up, orange when only its container is up
// (booted but not serving), red when the container is down, grey when it is
// archived, and a pulsing blue while a lifecycle action is in flight. The title carries the state for anyone
// who cannot see the color.
export const StatusDot = ({ running, appRunning, appState, pending, archived }) => {
  if (pending) {
    return <span className="status-dot status-pending" title="working" />;
  }
  // Archived reads grey rather than red: the app is not down, it is put away,
  // and red would have someone looking for a fault to fix.
  if (archived) {
    return <span className="status-dot status-archived" title="archived" />;
  }
  // A crash loop that gave up: the container is up but the app failed repeatedly
  // and hostit stopped restarting it. Distinct from a plain "container up, app
  // stopped" -- it is a problem, not an idle state.
  if (running && appState === "failed") {
    return (
      <span
        className="status-dot status-crashed"
        title="App crashed: it failed repeatedly, so hostit stopped restarting it. Check the logs, then redeploy or start it."
      />
    );
  }
  const cls = !running ? "status-down" : appRunning ? "status-up" : "status-degraded";
  const title = !running ? "stopped" : appRunning ? "running" : "container up, app stopped";
  return <span className={`status-dot ${cls}`} title={title} />;
};

// "12 of 512 MB", or just "12 MB" when the app has no limit for that resource.
export const formatUsage = (used, limit) => (limit ? `${used} of ${limit} MB` : `${used} MB`);

// UsagePair is the compact icon + "used/total" reading the dashboard's usage
// strip established ("5.1/20 GB"); one shared unit per pair, GB from 1 GB up.
// usageLevel maps used/total to the shared severity ladder ("" / warn / crit);
// 75 and 90 percent, the same knees the app page's inline stats use.
export const usageLevel = (used, total) => {
  if (!total) return "";
  const pct = (used / total) * 100;
  return pct >= 90 ? "crit" : pct >= 75 ? "warn" : "";
};

// "1 core", "2 cores", "0.5 cores" -- a cap of exactly one is singular, and it
// is the default a new app gets, so it is the one people read most.
export const cores = (n) => `${n} ${n === 1 ? "core" : "cores"}`;

export const pairMB = (u, t) => {
  if ((t || u) >= 1024) {
    const gb = (v) => (v / 1024).toFixed(v % 1024 ? 1 : 0);
    return t ? `${gb(u)}/${gb(t)} GB` : `${gb(u)} GB`;
  }
  return t ? `${u}/${t} MB` : `${u} MB`;
};

export const UsagePair = ({ kind, used, total }) => {
  const pair = pairMB;
  const level = usageLevel(used, total);
  return (
    <span className={"usage-item" + (level ? " usage-" + level : "")} title={kind === "ram" ? "RAM used of the app's limit" : "Disk used of the app's limit"}>
      {kind === "ram" ? (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <rect x="2" y="4" width="12" height="8" rx="1" />
          <path d="M4.5 12v2M8 12v2M11.5 12v2M4.5 2v2M8 2v2M11.5 2v2" />
        </svg>
      ) : (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <ellipse cx="8" cy="4" rx="6" ry="2.2" />
          <path d="M2 4v8c0 1.2 2.7 2.2 6 2.2s6-1 6-2.2V4" />
          <path d="M2 8c0 1.2 2.7 2.2 6 2.2s6-1 6-2.2" />
        </svg>
      )}
      {pair(used || 0, total || 0)}
    </span>
  );
};

// A timestamp as a short local date; `empty` is what a missing value reads as.
export const formatDate = (s, empty = "unknown") => {
  if (!s) {
    return empty;
  }
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString();
};

export const ErrorBanner = ({ message, onDismiss }) => {
  if (!message) {
    return null;
  }
  return (
    <div className="banner banner-error" role="alert">
      <span>{message}</span>
      {onDismiss && (
        <button type="button" className="banner-dismiss" onClick={onDismiss} aria-label="Dismiss">
          &times;
        </button>
      )}
    </div>
  );
};

// The brand mark: monospace wordmark with a blinking block cursor
export const Wordmark = ({ big = false }) => (
  <span className={`wordmark${big ? " wordmark-big" : ""}`}>
    hostit
    <span className="cursor" aria-hidden="true" />
  </span>
);

export const Loading = ({ label = "Loading..." }) => (
  <p className="loading" aria-live="polite">
    {label}
  </p>
);

// Terminal-style snippet block with a copy button; the signature visual
// element of the app. Lines starting with "#" render as comments.
export const Snippet = ({ text }) => (
  <div className="term">
    <pre>
      {text.split("\n").map((line, i) => (
        <span key={i} className="term-line">
          {line.startsWith("#") ? (
            <span className="term-comment">{line}</span>
          ) : (
            <>
              <span className="term-prompt">$ </span>
              {line}
            </>
          )}
        </span>
      ))}
    </pre>
    <div className="term-copy">
      <CopyButton text={text} />
    </div>
  </div>
);
