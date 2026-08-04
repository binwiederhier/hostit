import { useState, useRef, useEffect } from "react";

// Copies text to the clipboard with a "Copied!" confirmation; falls back to
// a hidden textarea + execCommand for non-secure contexts.
export const CopyButton = ({ text, small = true, children }) => {
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
    <button type="button" className={`btn${small ? " btn-small" : ""}`} onClick={copy}>
      {copied ? "Copied!" : children || "Copy"}
    </button>
  );
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
