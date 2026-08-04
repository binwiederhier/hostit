import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { CopyButton, ErrorBanner, Loading } from "../components";

// Shortens an authorized_keys line to "ssh-ed25519 ...<tail> comment".
const keyPreview = (key) => {
  const parts = key.trim().split(/\s+/);
  const type = parts[0] || "";
  const b64 = parts[1] || "";
  const comment = parts.slice(2).join(" ");
  return `${type} ...${b64.slice(-16)}${comment ? ` ${comment}` : ""}`;
};

const formatDate = (s) => {
  if (!s) {
    return "never";
  }
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString();
};

const SshKeys = () => {
  const [keys, setKeys] = useState(null);
  const [error, setError] = useState("");
  const [label, setLabel] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setKeys(await api.get("/v1/account/keys"));
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const add = async (e) => {
    e.preventDefault();
    if (busy || key.trim() === "") {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.post("/v1/account/keys", { label: label.trim(), key: key.trim() });
      setLabel("");
      setKey("");
      await load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (k) => {
    if (!window.confirm(`Delete SSH key "${k.label || keyPreview(k.key)}"? You will no longer be able to log in with it.`)) {
      return;
    }
    setError("");
    try {
      await api.del(`/v1/account/keys/${k.id}`);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <div className="card">
      <h2>SSH keys</h2>
      <p className="hint">These keys grant SSH access to all of your apps.</p>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {keys === null && !error && <Loading label="Loading keys..." />}
      {keys !== null && keys.length === 0 && <p className="empty">No SSH keys yet. Add one below.</p>}
      {keys !== null && keys.length > 0 && (
        <ul className="item-list">
          {keys.map((k) => (
            <li key={k.id}>
              <div className="item-main">
                <span className="item-label">{k.label || "(no label)"}</span>
                <span className="mono item-detail">{keyPreview(k.key)}</span>
              </div>
              <button type="button" className="btn btn-small btn-danger" onClick={() => remove(k)}>
                Delete
              </button>
            </li>
          ))}
        </ul>
      )}
      <form className="stack-form" onSubmit={add}>
        <input
          type="text"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="label, e.g. laptop"
          aria-label="SSH key label"
        />
        <textarea
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder="ssh-ed25519 AAAA... you@laptop"
          rows={3}
          aria-label="SSH public key"
        />
        <div>
          <button type="submit" className="btn btn-primary" disabled={busy || key.trim() === ""}>
            {busy ? "Adding..." : "Add key"}
          </button>
        </div>
      </form>
    </div>
  );
};

const Tokens = () => {
  const [tokens, setTokens] = useState(null);
  const [error, setError] = useState("");
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [newToken, setNewToken] = useState(null);

  const load = useCallback(async () => {
    try {
      setTokens(await api.get("/v1/account/tokens"));
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const create = async (e) => {
    e.preventDefault();
    if (busy) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      setNewToken(await api.post("/v1/account/tokens", { label: label.trim() }));
      setLabel("");
      await load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (t) => {
    if (!window.confirm(`Revoke token "${t.label || t.prefix}"? Anything still using it will stop working.`)) {
      return;
    }
    setError("");
    try {
      await api.del(`/v1/account/tokens/${t.id}`);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <div className="card">
      <h2>API tokens</h2>
      <p className="hint">
        Tokens authenticate the <span className="mono">hostit</span> CLI and API calls, e.g. from Claude or Codex.
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
      {tokens === null && !error && <Loading label="Loading tokens..." />}
      {tokens !== null && tokens.length === 0 && <p className="empty">No API tokens yet. Create one below.</p>}
      {tokens !== null && tokens.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Token</th>
                <th>Label</th>
                <th>Scope</th>
                <th>Created</th>
                <th>Last used</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {tokens.map((t) => (
                <tr key={t.id}>
                  <td className="mono">{t.prefix}...</td>
                  <td>{t.label || "-"}</td>
                  <td className="cell-muted">{t.app_name ? `App: ${t.app_name}` : "All apps"}</td>
                  <td className="cell-muted">{formatDate(t.created_at)}</td>
                  <td className="cell-muted">{formatDate(t.last_used)}</td>
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
      {tokens !== null && tokens.length > 0 && (
        <p className="hint">
          Account tokens can manage all your apps. App tokens work only for one app and are created automatically with it.
        </p>
      )}
      <form className="inline-form" onSubmit={create}>
        <input
          type="text"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="label, e.g. claude"
          aria-label="Token label"
        />
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? "Creating..." : "Create token"}
        </button>
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
