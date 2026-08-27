import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { useDropdown } from "../hooks";
import { ConfirmDialog, CopyButton, DocsLink, ErrorBanner, formatDate, Skeleton } from "../components";
import { TOGGLEABLE_TABS, TAB_LABELS, normalizeTabs, tabsFromCsv, tabsToCsv } from "../tabs";

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

// Three dots rather than a delete button: renaming is a thing you want to do,
// and it should not be the one action that is missing because it did not fit.
const KeyMenu = ({ k, onRename, onRemove }) => {
  const { open, setOpen, ref } = useDropdown();
  const pick = (fn) => () => {
    setOpen(false);
    fn();
  };
  return (
    <div className="menu conn-rowmenu" ref={ref}>
      <button
        type="button"
        className="btn btn-icon conn-kebab"
        onClick={() => setOpen(!open)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Actions for ${k.label || "this key"}`}
      >
        <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
          <circle cx="8" cy="3.1" r="1.35" />
          <circle cx="8" cy="8" r="1.35" />
          <circle cx="8" cy="12.9" r="1.35" />
        </svg>
      </button>
      {open && (
        <div className="menu-items" role="menu">
          <button type="button" role="menuitem" onClick={pick(onRename)}>Rename</button>
          <button type="button" role="menuitem" className="menu-item-danger" onClick={pick(onRemove)}>Delete</button>
        </div>
      )}
    </div>
  );
};

// Renaming touches the label only. The key is what everything trusts, so it is
// shown and not editable -- changing it would mean a different key entirely.
const RenameKeyDialog = ({ k, onClose, onRenamed }) => {
  useEscape(onClose);
  const [label, setLabel] = useState(k.label || "");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const save = async (e) => {
    e.preventDefault();
    if (busy || !label.trim()) return;
    setBusy(true);
    setError("");
    try {
      await api.put(`/api/account/keys/${encodeURIComponent(k.id)}`, { label: label.trim() });
      onRenamed();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
      <form className="card modal modal-sheet" onSubmit={save} onMouseDown={(e) => e.stopPropagation()}>
        <button type="button" className="modal-x" onClick={onClose} title="Close" aria-label="Close">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
            <path d="M6 6l12 12M18 6 6 18" />
          </svg>
        </button>
        <h2>Rename SSH key</h2>
        <p className="hint" style={{ marginBottom: "5px" }}>
          The label is only how you tell your keys apart. The key itself is untouched, so nothing
          that trusts it has to change.
        </p>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <label className="conn-field">
          <span>Label</span>
          <input type="text" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="work laptop" aria-label="Label" autoFocus disabled={busy} />
        </label>
        <label className="conn-field">
          <span>Key</span>
          <input type="text" className="mono" value={keyPreview(k.key)} readOnly disabled />
        </label>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !label.trim()}>
            {busy ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </div>
  );
};

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
  const [renaming, setRenaming] = useState(null);
  const [removing, setRemoving] = useState(null);
  const [busy, setBusy] = useState(false);

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
    setError("");
    setBusy(true);
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
                    <KeyMenu k={k} onRename={() => setRenaming(k)} onRemove={() => setRemoving(k)} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {adding && <AddKeyDialog onClose={() => setAdding(false)} onAdded={load} />}
      {removing && (
        <ConfirmDialog
          title={`Delete SSH key "${removing.label || keyPreview(removing.key)}"?`}
          confirmLabel="Delete key"
          busy={busy}
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            await remove(removing);
            setRemoving(null);
          }}
          body="You will no longer be able to log in to any of your apps with it. Adding it back later works, but the key has to be pasted again."
        />
      )}
      {renaming && (
        <RenameKeyDialog
          k={renaming}
          onClose={() => setRenaming(null)}
          onRenamed={async () => {
            setRenaming(null);
            await load();
          }}
        />
      )}
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
  const [revoking, setRevoking] = useState(null);

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
    setError("");
    setRevoking(null);
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
                    <button type="button" className="btn btn-small btn-danger" onClick={() => setRevoking(t)}>
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
      {revoking && (
        <ConfirmDialog
          title={`Revoke token "${revoking.label || revoking.prefix}"?`}
          confirmLabel="Revoke token"
          onClose={() => setRevoking(null)}
          onConfirm={() => revoke(revoking)}
          body="Anything still using it stops working immediately -- a script, a CLI, an agent you pasted it into. It cannot be recovered; create a new one instead."
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

// ProfilePrefs is the self-service part of the profile: technical level (which
// the welcome modal first set), the default app-detail tabs, and -- when the
// instance has an assistant -- a personal instruction appended to every app's
// assistant. Each control saves on change; the server normalizes the tab set.
const ProfilePrefs = ({ account, refreshAccount }) => {
  const [prompt, setPrompt] = useState("");
  const [tabs, setTabs] = useState(new Set());
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const assistantEnabled = !!(account && account.assistant_enabled);

  useEffect(() => {
    if (!account) return;
    setPrompt(account.assistant_prompt || "");
    setTabs(new Set(tabsFromCsv(account.default_tabs)));
  }, [account]);

  const flashSaved = () => {
    setSaved(true);
    setTimeout(() => setSaved(false), 1500);
  };
  const save = async (patch) => {
    setSaving(true);
    setError("");
    try {
      await api.patch("/api/account", patch);
      if (refreshAccount) await refreshAccount();
      flashSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setSaving(false);
    }
  };
  const toggleTab = (key) => {
    const next = new Set(tabs);
    if (next.has(key)) {
      next.delete(key);
    } else {
      next.add(key);
    }
    const csv = tabsToCsv(normalizeTabs([...next], assistantEnabled));
    setTabs(new Set(tabsFromCsv(csv)));
    save({ default_tabs: csv });
  };

  if (!account) return null;
  const tabKeys = TOGGLEABLE_TABS.filter((k) => k !== "assistant" || assistantEnabled);
  return (
    <div className="card profile-prefs">
      <div className="conn-head">
        <h2>Preferences</h2>
        {saved && <span className="profile-saved">Saved</span>}
      </div>

      <label className="newapp-label">Default app tabs</label>
      <p className="hint">Which tabs an app opens with. Each app can override this from its own View menu.</p>
      <div className="tab-toggle-row">
        {tabKeys.map((key) => {
          const on = tabs.size === 0 ? true : tabs.has(key);
          return (
            <button
              key={key}
              type="button"
              role="checkbox"
              aria-checked={on}
              className={on ? "tab-toggle on" : "tab-toggle"}
              onClick={() => toggleTab(key)}
              disabled={saving}
            >
              <span className="tab-toggle-check" aria-hidden="true">{on ? "✓" : ""}</span>
              {TAB_LABELS[key]}
            </button>
          );
        })}
      </div>
      {tabs.size === 0 && <p className="hint profile-prefs-sub">All tabs (the default). Turn some off to hide them by default.</p>}

      {assistantEnabled && (
        <>
          <label className="newapp-label profile-prefs-gap">Assistant instructions</label>
          <p className="hint">Added to the assistant&rsquo;s prompt in every app you own, and to each app&rsquo;s <span className="mono">/info</span>. Say how you want it to work.</p>
          <textarea
            className="profile-prompt"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            onBlur={() => { if (prompt !== (account.assistant_prompt || "")) save({ assistant_prompt: prompt }); }}
            placeholder="e.g. Explain changes in plain language and always write tests."
            rows={4}
          />
        </>
      )}
      <ErrorBanner message={error} onDismiss={() => setError("")} />
    </div>
  );
};

const Profile = ({ account, refreshAccount }) => (
  <>
    <div className="page-header">
      <h1>Profile</h1>
    </div>
    <ProfilePrefs account={account} refreshAccount={refreshAccount} />
    <SshKeys />
    <Tokens />
  </>
);

export default Profile;
