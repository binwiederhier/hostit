import { useState, useRef, useEffect } from "react";
import { docsHref } from "./docs";

// Copies text to the clipboard with a "Copied!" confirmation; falls back to
// a hidden textarea + execCommand for non-secure contexts.
// A clipboard glyph for icon-only copy buttons.
export const CopyIcon = () => (
  <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <rect x="9" y="9" width="11" height="11" rx="2" />
    <path d="M5 15V5a2 2 0 0 1 2-2h8" />
  </svg>
);

export const CopyButton = ({ text, small = true, disabled = false, title, onCopied, children }) => {
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
    // With an onCopied handler the caller shows its own feedback (a toast), so
    // the label stays put -- no "Copied!" swap that shifts an icon button.
    if (onCopied) {
      onCopied();
      return;
    }
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  };
  return (
    <button type="button" className={`btn${small ? " btn-small" : ""}`} onClick={copy} disabled={disabled} title={title} aria-label={title}>
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

// A small warning triangle for inline field notes ("<icon> message" next to a
// label), sized to sit on a line of text.
export const WarnIcon = () => (
  <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" style={{ verticalAlign: "-1px" }}>
    <path d="M8 2.2 14.5 13.3H1.5L8 2.2Z" />
    <path d="M8 6.4v3" />
    <path d="M8 11.4h.01" />
  </svg>
);

// The brand mark: monospace wordmark with a blinking block cursor
export const Wordmark = ({ big = false }) => (
  <span className={`wordmark${big ? " wordmark-big" : ""}`}>
    hostit
    <span className="cursor" aria-hidden="true" />
  </span>
);

// A link into the manual, beside the thing it explains. It opens in a new tab
// for the same reason the nav's docs link does -- the manual is a thing you read
// beside the app, not instead of it.
export const DocsLink = ({ guide, section, sub, children }) => (
  <a className="docs-link" href={docsHref(guide, section, sub)} target="_blank" rel="noreferrer">
    {children}
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M6 3.5h6.5V10M12.5 3.5 4 12" />
    </svg>
  </a>
);

// A confirm step that looks like the rest of the app. window.confirm cannot be
// styled, cannot show what is at stake in more than one line, and on some
// browsers is suppressed entirely -- which turns "are you sure" into "done".
//
// Escape and a click outside both cancel: the safe answer is the easy one.
export const ConfirmDialog = ({ title, body, confirmLabel = "Remove", danger = true, busy, onConfirm, onClose }) => {
  useEffect(() => {
    const onKey = (e) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <div className="card modal modal-sheet modal-confirm" onMouseDown={(e) => e.stopPropagation()}>
        <button type="button" className="modal-x" onClick={onClose} title="Close" aria-label="Close">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
            <path d="M6 6l12 12M18 6 6 18" />
          </svg>
        </button>
        <h2>{title}</h2>
        {body && <div className="hint confirm-body">{body}</div>}
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy} autoFocus>
            Cancel
          </button>
          <button
            type="button"
            className={"btn " + (danger ? "btn-danger" : "btn-primary")}
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? "Working..." : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
};

export const Loading = ({ label = "Loading..." }) => (
  <p className="loading" aria-live="polite">
    {label}
  </p>
);

// Placeholder rows shown while a list loads, at the height the real rows will
// be. A one-line "Loading..." is honest but makes every page jump: the card is
// 20px tall, then several hundred the moment data lands. Reserving the space
// costs nothing and removes the jump entirely.
//
// aria-hidden with a live label beside it: a screen reader should hear "loading",
// not a description of grey rectangles.
export const Skeleton = ({ rows = 3, card = false, label = "Loading..." }) => (
  <>
    <span className="sr-only" aria-live="polite">
      {label}
    </span>
    <div className={card ? "skeleton skeleton-cards" : "skeleton"} aria-hidden="true">
      {Array.from({ length: rows }, (_, i) => (
        <div className={card ? "skeleton-card" : "skeleton-row"} key={i}>
          <div className="skeleton-bar skeleton-bar-wide" />
          <div className="skeleton-bar skeleton-bar-narrow" />
        </div>
      ))}
    </div>
  </>
);

// Terminal-style snippet block with a copy button; the signature visual
// element of the app. Lines starting with "#" render as comments.
export const Snippet = ({ text }) => (
  <div className="term">
    <pre>
      {/* No "$" prompt: these blocks are as often a config file or a URL as
          they are a shell command, and a prompt in front of a YAML key is
          simply wrong. Lines starting with "#" still read as comments. */}
      {text.split("\n").map((line, i) => (
        <span key={i} className="term-line">
          {line.startsWith("#") ? <span className="term-comment">{line}</span> : line}
        </span>
      ))}
    </pre>
    <div className="term-copy">
      <CopyButton text={text} />
    </div>
  </div>
);

// The visibility choice, shown wherever an app's audience is decided. Both
// options are always on screen rather than one checkbox: "private" should be a
// thing you picked, not a box you failed to notice.
// Three cards, one per visibility state, so "restricted" is a thing you pick
// rather than an emergent side effect of a private app with a viewer list.
// `value` is the state string ("private" | "restricted" | "public"); the
// caller decides what, if anything, to render beneath a picked "restricted".
// Private FIRST: landing on public by accident publishes something, on private
// it does not. Restricted sits between the two -- private, plus named people.
export const VisibilityChoice = ({ value, onChange, disabled, allowListed = false }) => {
  const keys = allowListed ? ["private", "restricted", "public", "listed"] : ["private", "restricted", "public"];
  return (
  <div className={"visibility-choice " + (allowListed ? "visibility-choice-four" : "visibility-choice-three")} role="radiogroup" aria-label="Visibility">
    {keys.map((key) => (
      <button
        key={key}
        type="button"
        role="radio"
        aria-checked={value === key}
        className={value === key ? "vis-option vis-option-on" : "vis-option"}
        onClick={() => onChange(key)}
        disabled={disabled}
      >
        <VisibilityIcon state={key} />
        <span className="vis-title">{VISIBILITY[key].label}</span>
        <span className="vis-detail">{VISIBILITY[key].detail}</span>
      </button>
    ))}
  </div>
  );
};

// The three self-selected technical levels, as picture cards, shared by the
// welcome modal and the profile page. Least-technical first, as the design asks:
// picking "not technical" by reflex is the safe landing, and the order puts it
// under the reader's eye first. `value` is the level key; onChange(key).
// Colorful people, not pictograms: a friendly non-technical person, a
// headphones-wearing tinkerer, and a bespectacled coder -- so the reader picks
// the one that looks like them at a glance. Each is a self-coloured SVG (its own
// fills, not currentColor), clipped to a round avatar.
const TechAvatarNovice = () => (
  <svg viewBox="0 0 48 48" fill="none" aria-hidden="true" className="tech-avatar">
    <defs><clipPath id="tc-novice"><circle cx="24" cy="24" r="24" /></clipPath></defs>
    <g clipPath="url(#tc-novice)">
      <rect width="48" height="48" fill="#FFE1BE" />
      <path d="M6 48v-4c0-8 8-12 18-12s18 4 18 12v4Z" fill="#F4915C" />
      <g transform="translate(24 19) scale(1.16) translate(-24 -19)">
        <circle cx="24" cy="19" r="9" fill="#F7C9A3" />
        <path d="M15 19A9 9 0 0 1 33 19Z" fill="#8B5A2B" />
        <circle cx="20.5" cy="20" r="1.1" fill="#4A3728" />
        <circle cx="27.5" cy="20" r="1.1" fill="#4A3728" />
        <path d="M20.5 23q3.5 3 7 0" stroke="#4A3728" strokeWidth="1.3" strokeLinecap="round" />
        <circle cx="17.6" cy="23" r="1.3" fill="#F4A9A0" opacity="0.6" />
        <circle cx="30.4" cy="23" r="1.3" fill="#F4A9A0" opacity="0.6" />
      </g>
    </g>
  </svg>
);
const TechAvatarIntermediate = () => (
  <svg viewBox="0 0 48 48" fill="none" aria-hidden="true" className="tech-avatar">
    <defs><clipPath id="tc-inter"><circle cx="24" cy="24" r="24" /></clipPath></defs>
    <g clipPath="url(#tc-inter)">
      <rect width="48" height="48" fill="#BFE7DE" />
      <path d="M6 48v-4c0-8 8-12 18-12s18 4 18 12v4Z" fill="#2FA392" />
      <g transform="translate(24 19) scale(1.16) translate(-24 -19)">
        <circle cx="24" cy="19" r="9" fill="#F1C29B" />
        <path d="M15 19A9 9 0 0 1 33 19Z" fill="#4E342E" />
        <circle cx="20.5" cy="20" r="1.1" fill="#3B2A22" />
        <circle cx="27.5" cy="20" r="1.1" fill="#3B2A22" />
        <path d="M21 23.5q3 2 6 0" stroke="#3B2A22" strokeWidth="1.3" strokeLinecap="round" />
        <path d="M13 20a11 11 0 0 1 22 0" stroke="#37474F" strokeWidth="2.4" strokeLinecap="round" />
        <rect x="11.4" y="19" width="4" height="7.5" rx="2" fill="#37474F" />
        <rect x="32.6" y="19" width="4" height="7.5" rx="2" fill="#37474F" />
      </g>
    </g>
  </svg>
);
const TechAvatarExpert = () => (
  <svg viewBox="0 0 48 48" fill="none" aria-hidden="true" className="tech-avatar">
    <defs><clipPath id="tc-expert"><circle cx="24" cy="24" r="24" /></clipPath></defs>
    <g clipPath="url(#tc-expert)">
      <rect width="48" height="48" fill="#D6CBF2" />
      <path d="M6 48v-4c0-8 8-12 18-12s18 4 18 12v4Z" fill="#7C5CD6" />
      <path d="M18 33c0 3 2.6 5 6 5s6-2 6-5" stroke="#5E44B0" strokeWidth="1.6" />
      <g transform="translate(24 19) scale(1.16) translate(-24 -19)">
        <circle cx="24" cy="19" r="9" fill="#EEBF98" />
        <path d="M14.6 19A9 9 0 0 1 33.4 19q-4-3.4-9.4-3.4T14.6 19Z" fill="#3E2723" />
        <path d="M23.3 20.2h1.4M31.3 19.2 34.4 18M16.7 19.2 13.6 18" stroke="#2B2B3A" strokeWidth="1.4" strokeLinecap="round" />
        <circle cx="20" cy="20.2" r="3.3" fill="#F4ECFF" stroke="#2B2B3A" strokeWidth="1.4" />
        <circle cx="28" cy="20.2" r="3.3" fill="#F4ECFF" stroke="#2B2B3A" strokeWidth="1.4" />
        <circle cx="20" cy="20.2" r="0.9" fill="#2B2B3A" />
        <circle cx="28" cy="20.2" r="0.9" fill="#2B2B3A" />
        <path d="M21 24.6q3 1.6 6 0" stroke="#7A5540" strokeWidth="1.2" strokeLinecap="round" />
      </g>
    </g>
  </svg>
);

export const TechLevelCards = ({ value, onChange, disabled }) => (
  <div className="tech-cards" role="radiogroup" aria-label="How technical are you?">
    {[
      {
        key: "novice",
        title: "Not technical at all",
        detail: "Describe what you want in plain words and let the assistant build it.",
        avatar: <TechAvatarNovice />,
      },
      {
        key: "intermediate",
        title: "Somewhat technical",
        detail: "You can follow along, tweak files, and read the logs when needed.",
        avatar: <TechAvatarIntermediate />,
      },
      {
        key: "expert",
        title: "Very technical",
        detail: "You write code and want the terminal, files, and logs at hand.",
        avatar: <TechAvatarExpert />,
      },
    ].map((option) => (
      <button
        key={option.key}
        type="button"
        role="radio"
        aria-checked={value === option.key}
        className={value === option.key ? "tech-card tech-card-on" : "tech-card"}
        onClick={() => onChange(option.key)}
        disabled={disabled}
      >
        {option.avatar}
        <span className="tech-card-title">{option.title}</span>
        <span className="tech-card-detail">{option.detail}</span>
      </button>
    ))}
  </div>
);

// The three visibility states, in one vocabulary shared by the badge, the
// chooser and the settings row. "Restricted" is not a fourth setting: it is
// what private looks like once somebody else has been let in, so the reader
// can tell "only me" from "me and two others" at a glance.
export const visibilityOf = (isPrivate, viewerCount = 0, listed = false) => {
  if (!isPrivate) return listed ? "listed" : "public";
  return viewerCount > 0 ? "restricted" : "private";
};

const VISIBILITY = {
  public: { label: "Public", detail: "Anyone with the link", hint: "Anyone with the link can open this app" },
  private: { label: "Private", detail: "Only you, collaborators and admins", hint: "Only you, your collaborators and admins can open this app" },
  restricted: { label: "Restricted", detail: "Also specific people you add", hint: "Your collaborators, the people you have given access, and admins" },
  listed: { label: "Listed", detail: "Public, and on the Explore gallery", hint: "Anyone with the link can open it, and it appears on the instance's Explore gallery" },
};

const GlobeIcon = () => <path d="M8 1.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13ZM1.5 8h13M8 1.5c1.7 1.8 2.6 4 2.6 6.5S9.7 12.7 8 14.5c-1.7-1.8-2.6-4-2.6-6.5S6.3 3.3 8 1.5Z" />;
const LockIcon = () => (
  <>
    <rect x="3" y="7" width="10" height="7" rx="1.5" />
    <path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" />
  </>
);
const PeopleIcon = () => (
  <>
    <circle cx="6" cy="5.5" r="2.2" />
    <path d="M2 13.5c0-2.2 1.8-3.5 4-3.5s4 1.3 4 3.5" />
    <path d="M11 4.2a2.2 2.2 0 0 1 0 4.3M12.2 13.5c0-1.6-.6-2.7-1.7-3.3" />
  </>
);

// A globe with a small star: public, and on the gallery.
const ListedIcon = () => (
  <>
    <path d="M7 1.6a6.2 6.2 0 1 0 3.5 11.3M2 5.5h9M6.6 1.7C5.2 3.4 4.4 5.5 4.4 8s.8 4.6 2.2 6.3" />
    <path d="M12.4 8.4l.85 1.72 1.9.28-1.37 1.34.32 1.9-1.7-.9-1.7.9.32-1.9L9.65 10.4l1.9-.28z" />
  </>
);

export const VisibilityIcon = ({ state }) => (
  <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {state === "public" ? <GlobeIcon /> : state === "listed" ? <ListedIcon /> : state === "restricted" ? <PeopleIcon /> : <LockIcon />}
  </svg>
);

// An app's visibility, wherever it is listed. It says "public" too: leaving
// the common case unlabelled makes the label mean "something unusual here"
// rather than "this is the setting", which is the wrong thing to learn about
// a control you are about to change.
export const VisibilityBadge = ({ state }) => (
  <span className={"vis-badge vis-badge-" + state} title={VISIBILITY[state].hint}>
    <VisibilityIcon state={state} />
    {VISIBILITY[state].label}
  </span>
);

// The same three states as VisibilityBadge, reduced to the icon. Beside an
// app's name in a list the word is noise -- the row is already dense -- but the
// state still has to be visible without opening anything, and "public" carries
// a mark too so the absence of one never has to be interpreted.
export const VisibilityMark = ({ state }) => (
  <span className={"vis-mark vis-mark-" + state} title={VISIBILITY[state].label + ": " + VISIBILITY[state].hint}>
    <VisibilityIcon state={state} />
  </span>
);
