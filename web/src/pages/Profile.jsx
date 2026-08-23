import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { docsHref } from "../docs";
import { CopyButton, ErrorBanner, formatDate, Skeleton } from "../components";

// Shortens an authorized_keys line to "ssh-ed25519 ...<tail> comment".
const keyPreview = (key) => {
  const parts = key.trim().split(/\s+/);
  const type = parts[0] || "";
  const b64 = parts[1] || "";
  const comment = parts.slice(2).join(" ");
  return `${type} ...${b64.slice(-16)}${comment ? ` ${comment}` : ""}`;
};

// Escape closes a dialog, like every other modal in the app.
const useEscape = (onClose) => {
  useEffect(() => {
    const onKey = (e) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);
};

// DocsLink points at the section of the public manual that explains a feature,
// so the page can stay short and still be self-explanatory. It is a plain
// anchor, not a router Link: the docs render outside the router, so a
// client-side navigation would land on the catch-all and bounce to the
// dashboard. It opens in a new tab for the same reason the nav's docs link
// does -- the manual is a thing you read beside the app, not instead of it.
const DocsLink = ({ guide, section, children }) => (
  <a className="docs-link" href={docsHref(guide, section)} target="_blank" rel="noreferrer">
    {children}
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M6 3.5h6.5V10M12.5 3.5 4 12" />
    </svg>
  </a>
);

const AddKeyDialog = ({ onClose, onAdded }) => {
  useEscape(onClose);
  const [label, setLabel] = useState("");
  const [key, setKey] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const add = async (e) => {
    e.preventDefault();
    if (busy || key.trim() === "") return;
    setBusy(true);
    setError("");
    try {
      await api.post("/api/account/keys", { label: label.trim(), key: key.trim() });
      await onAdded();
      onClose();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <form className="card modal modal-sheet" onMouseDown={(e) => e.stopPropagation()} onSubmit={add}>
        <button type="button" className="modal-x" onClick={onClose} title="Close" aria-label="Close">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6 6 18" /></svg>
        </button>
        <h2>Add an SSH key</h2>
        <p className="hint">
          Paste a public key -- the contents of <span className="mono">~/.ssh/id_ed25519.pub</span>, not the
          private half. It grants SSH and scp access to every app you own.
        </p>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <label className="settings-field">
          <span>Label</span>
          <input
            type="text"
            className="settings-input"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="laptop"
            autoFocus
            disabled={busy}
          />
        </label>
        <label className="settings-field settings-field-stacked">
          <span>Public key</span>
          <textarea
            className="settings-desc mono"
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder="ssh-ed25519 AAAA... you@laptop"
            rows={4}
            disabled={busy}
          />
        </label>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={busy || key.trim() === ""}>
            {busy ? "Adding..." : "Add key"}
          </button>
        </div>
      </form>
    </div>
  );
};

const SshKeys = () => {
  const [keys, setKeys] = useState(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(async () => {
    try {
      setKeys(await api.get("/api/account/keys"));
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const remove = async (k) => {
    if (!window.confirm(`Delete SSH key "${k.label || keyPreview(k.key)}"? You will no longer be able to log in with it.`)) {
      return;
    }
    setError("");
    try {
      await api.del(`/api/account/keys/${k.id}`);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <div className="card">
      <div className="card-header">
        <h2>SSH keys</h2>
        <button type="button" className="btn btn-small btn-primary" onClick={() => setAdding(true)}>
          Add SSH key
        </button>
      </div>
      <p className="hint">
        These keys grant SSH, scp and rsync access to all of your apps, and the terminal in the web
        app uses the same login. <DocsLink guide="user" section="ssh">How SSH access works</DocsLink>
      </p>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {keys === null && !error && <Skeleton rows={2} label="Loading keys..." />}
      {keys !== null && keys.length === 0 && (
        <p className="empty">No SSH keys yet. Add one to reach your apps over SSH.</p>
      )}
      {keys !== null && keys.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Label</th>
                <th>Key</th>
                <th>Added</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id}>
                  <td>{k.label || <span className="cell-muted">(no label)</span>}</td>
                  <td className="mono cell-muted">{keyPreview(k.key)}</td>
                  <td className="cell-muted">{formatDate(k.created_at, "unknown")}</td>
                  <td className="cell-actions">
                    <button type="button" className="btn btn-small btn-danger" onClick={() => remove(k)}>
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {adding && <AddKeyDialog onClose={() => setAdding(false)} onAdded={load} />}
    </div>
  );
};

const CreateTokenDialog = ({ onClose, onCreated }) => {
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
      const created = await api.post("/api/account/tokens", { label: label.trim() });
      await onCreated(created);
      onClose();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <form className="card modal modal-sheet" onMouseDown={(e) => e.stopPropagation()} onSubmit={create}>
        <button type="button" className="modal-x" onClick={onClose} title="Close" aria-label="Close">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6 6 18" /></svg>
        </button>
        <h2>Create an API token</h2>
        <p className="hint">
          The token is shown once, right after it is created. It can manage every app you own, so
          give it a label you will recognise when it is time to revoke it.
        </p>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <label className="settings-field">
          <span>Label</span>
          <input
            type="text"
            className="settings-input"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="claude on my laptop"
            autoFocus
            disabled={busy}
          />
        </label>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={busy}>
            {busy ? "Creating..." : "Create token"}
          </button>
        </div>
      </form>
    </div>
  );
};

const Tokens = () => {
  const [tokens, setTokens] = useState(null);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [newToken, setNewToken] = useState(null);

  const load = useCallback(async () => {
    try {
      setTokens(await api.get("/api/account/tokens"));
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const revoke = async (t) => {
    if (!window.confirm(`Revoke token "${t.label || t.prefix}"? Anything still using it will stop working.`)) {
      return;
    }
    setError("");
    try {
      await api.del(`/api/account/tokens/${t.id}`);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <div className="card">
      <div className="card-header">
        <h2>API tokens</h2>
        <button type="button" className="btn btn-small btn-primary" onClick={() => setCreating(true)}>
          Create token
        </button>
      </div>
      <p className="hint">
        Tokens authenticate the <span className="mono">hostit</span> CLI and direct API calls -- including
        your own AI agent. They can manage all your apps; each app also has its own scoped token on
        its page. <DocsLink guide="user" section="api">The API, and how agents use it</DocsLink>
      </p>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {newToken && (
        <div className="warn-box">
          <p>
            <strong>This token is shown only once. Save it now.</strong>
          </p>
          <pre className="key-block">{newToken.token}</pre>
          <div className="btn-row">
            <CopyButton text={newToken.token} small={false}>
              Copy token
            </CopyButton>
            <button type="button" className="btn" onClick={() => setNewToken(null)}>
              Dismiss
            </button>
          </div>
        </div>
      )}
      {tokens === null && !error && <Skeleton rows={2} label="Loading tokens..." />}
      {tokens !== null && tokens.length === 0 && (
        <p className="empty">No API tokens yet. Create one to use the CLI or the API.</p>
      )}
      {tokens !== null && tokens.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Token</th>
                <th>Label</th>
                <th>Created</th>
                <th>Last used</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => (
                <tr key={t.id}>
                  <td className="mono">{t.prefix}...</td>
                  <td>{t.label || <span className="cell-muted">(no label)</span>}</td>
                  <td className="cell-muted">{formatDate(t.created_at, "never")}</td>
                  <td className="cell-muted">{formatDate(t.last_used, "never")}</td>
                  <td className="cell-actions">
                    <button type="button" className="btn btn-small btn-danger" onClick={() => revoke(t)}>
                      Revoke
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {creating && (
        <CreateTokenDialog
          onClose={() => setCreating(false)}
          onCreated={async (created) => {
            setNewToken(created);
            await load();
          }}
        />
      )}
    </div>
  );
};



// RenameConnectionDialog changes the name apps address a connection by, which
// breaks any app configured for the old one -- so it says so rather than
// looking cosmetic.
const RenameConnectionDialog = ({ conn, busy, onClose, onSave }) => {
  useEscape(onClose);
  const [slug, setSlug] = useState(conn.slug);
  const [label, setLabel] = useState(conn.label || "");
  const changed = slug.trim().toLowerCase() !== conn.slug || label.trim() !== (conn.label || "");

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <form
        className="card modal modal-sheet"
        onSubmit={(e) => {
          e.preventDefault();
          onSave(conn, slug.trim().toLowerCase(), label.trim());
        }}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <button type="button" className="modal-x" onClick={onClose} title="Close" aria-label="Close">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6 6 18" /></svg>
        </button>
        <h2>Rename {conn.slug}</h2>
        <p className="hint" style={{ marginBottom: "5px" }}>
          The name is what an app asks for. Changing it breaks any app already configured for
          <span className="mono"> {conn.slug}</span> until that app is updated too.
        </p>
        <label className="conn-field">
          <span>Name</span>
          <input type="text" value={slug} onChange={(e) => setSlug(e.target.value)} aria-label="Name" autoFocus disabled={busy} />
        </label>
        <label className="conn-field">
          <span>Description</span>
          <input type="text" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="optional" aria-label="Description" disabled={busy} />
        </label>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !changed || slug.trim().length < 3}>
            {busy ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </div>
  );
};

const Profile = () => (
  <>
    <div className="page-header">
      <h1>Profile</h1>
    </div>
    <SshKeys />
    <Tokens />
  </>
);

export default Profile;
