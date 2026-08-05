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
// (booted but not serving), red when the container is down. The title carries
// the state for anyone who cannot see the color.
export const StatusDot = ({ running, appRunning }) => {
  const cls = !running ? "status-down" : appRunning ? "status-up" : "status-degraded";
  const title = !running ? "stopped" : appRunning ? "running" : "container up, app stopped";
  return <span className={`status-dot ${cls}`} title={title} />;
};

// "12 of 512 MB", or just "12 MB" when the app has no limit for that resource.
export const formatUsage = (used, limit) => (limit ? `${used} of ${limit} MB` : `${used} MB`);

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
