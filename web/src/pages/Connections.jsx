import { useCallback, useEffect, useState } from "react";
import { menuProviders, slugify, splitByKind, suggestSlug } from "../connections";
import { api } from "../api";
import { useDropdown } from "../hooks";
import { ErrorBanner, Loading } from "../components";

// Connections and credentials: the accounts and secrets an owner attaches once
// and can then grant to their apps. TWO cards, not one, because they are two
// different things to a person -- an OAuth account is something you CONNECT, a
// pasted key is something you STORE, and one heading makes one of them read
// wrong.
//
// The credential itself is never shown or returned. This page says WHAT is
// attached; each app's settings say which of them that app may use.
const Connections = () => {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(null);
  const [renaming, setRenaming] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setData(await api.get("/api/connections"));
    } catch (err) {
      setError(err.message);
      setData({ connections: [], providers: [] });
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const providers = data?.providers || [];
  const { connections, credentials } = splitByKind(data?.connections);

  const reconnect = async (c) => {
    setError("");
    try {
      const res = await api.post(`/api/connections/${encodeURIComponent(c.slug)}/reconnect`, {});
      window.location.href = res.redirect_url;
    } catch (err) {
      setError(err.message);
    }
  };

  const rename = async (c, nextSlug, nextLabel) => {
    setError("");
    setBusy(true);
    try {
      await api.put(`/api/connections/${encodeURIComponent(c.slug)}`, { slug: nextSlug, label: nextLabel });
      setRenaming(null);
      await load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (c) => {
    const warn = c.granted_apps > 0 ? ` ${c.granted_apps} app(s) lose access immediately.` : "";
    if (!window.confirm(`Remove ${c.label || c.slug}?${warn}`)) return;
    setError("");
    try {
      await api.del(`/api/connections/${encodeURIComponent(c.slug)}`);
      await load();
    } catch (err) {
      setError(err.message);
    }
  };

  const shared = { onRename: setRenaming, onReconnect: reconnect, onRemove: remove, onAdd: setAdding };

  return (
    <>
      <div className="page-header">
        <h1>Connections</h1>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />

      <ConnectionsCard
        title="Connections"
        hint="Accounts you connect once and then grant to individual apps in their settings. Apps act as you, and only reach what you grant them. Connect the same service twice -- two calendars, say -- and each keeps its own reference."
        emptyText="No accounts connected yet."
        cta="Add connection"
        items={connections}
        providers={providers.filter((p) => p.kind === "oauth")}
        loading={data === null && !error}
        noProvidersText="No accounts can be connected on this server yet: an admin sets each provider's OAuth client in control.yml."
        {...shared}
      />
      <ConnectionsCard
        title="Credentials"
        hint="API keys, tokens, SSH keys, database URLs and mailbox passwords you paste in. Stored encrypted, and handed to an app only when you grant it. Nothing here needs an OAuth client or any review."
        emptyText="No credentials stored yet."
        cta="Add credential"
        items={credentials}
        providers={providers.filter((p) => p.kind === "static")}
        loading={data === null && !error}
        {...shared}
      />

      {adding && (
        <AddConnectionDialog
          provider={adding}
          existing={data?.connections || []}
          onClose={() => setAdding(null)}
          onAdded={async () => {
            setAdding(null);
            await load();
          }}
        />
      )}
      {renaming && (
        <RenameConnectionDialog conn={renaming} busy={busy} onClose={() => setRenaming(null)} onSave={rename} />
      )}
    </>
  );
};

// One of the two cards. Both are the same shape -- what is attached, and one
// button to attach more -- so they share a component rather than being copied
// with the nouns changed.
const ConnectionsCard = ({ title, hint, emptyText, cta, items, providers, loading, onAdd, onRename, onReconnect, onRemove, noProvidersText }) => (
  <div className="card">
    <div className="conn-head">
      <h2>{title}</h2>
      <AddMenu label={cta} providers={providers} onPick={onAdd} disabledText={noProvidersText} />
    </div>
    <p className="hint">{hint}</p>
    {loading && <Loading label={`Loading ${title.toLowerCase()}...`} />}
    {!loading && items.length === 0 && <p className="hint conn-empty">{emptyText}</p>}
    {!loading &&
      items.map((c) => (
        <div key={c.slug} className="conn-row">
          <div className="conn-id">
            <span className="conn-name">
              {c.label || c.slug}
              <span className="conn-provider">{c.provider_label}</span>
            </span>
            <span className="conn-note">
              apps use <span className="mono">{c.slug}</span>
              {" -- "}
              {c.granted_apps > 0
                ? `granted to ${c.granted_apps} app${c.granted_apps === 1 ? "" : "s"}`
                : "not granted to any app yet"}
              {c.meta ? ` -- ${c.meta}` : ""}
            </span>
          </div>
          <span className="conn-actions">
            <button type="button" className="btn btn-small" onClick={() => onRename(c)}>Edit</button>
            {c.kind === "oauth" && (
              <button type="button" className="btn btn-small" onClick={() => onReconnect(c)}>Reconnect</button>
            )}
            <button type="button" className="btn btn-small btn-danger" onClick={() => onRemove(c)}>Remove</button>
          </span>
        </div>
      ))}
    {noProvidersText && providers.length === 0 && <p className="hint">{noProvidersText}</p>}
  </div>
);

// One call to action per card, dropping a menu of what can be attached. The
// catch-all sits below a divider and is styled apart: it is the escape hatch for
// anything hostit does not know, not just another entry in the list.
const AddMenu = ({ label, providers, onPick, disabledText }) => {
  const { open, setOpen, ref } = useDropdown();
  const { named, other } = menuProviders(providers);
  const empty = named.length === 0 && !other;

  return (
    <div className="menu" ref={ref}>
      <button
        type="button"
        className="btn btn-primary btn-small"
        onClick={() => setOpen(!open)}
        disabled={empty}
        aria-haspopup="menu"
        aria-expanded={open}
        title={empty ? disabledText : undefined}
      >
        {label}
        <span className="conn-caret" aria-hidden="true">&#9662;</span>
      </button>
      {open && (
        <div className="menu-items conn-menu" role="menu">
          {named.map((p) => (
            <button
              key={p.name}
              type="button"
              role="menuitem"
              className="menu-item"
              onClick={() => {
                setOpen(false);
                onPick(p);
              }}
            >
              {p.label}
            </button>
          ))}
          {other && (
            <>
              <div className="conn-menu-divider" role="separator" />
              <button
                type="button"
                role="menuitem"
                className="menu-item conn-menu-other"
                onClick={() => {
                  setOpen(false);
                  onPick(other);
                }}
              >
                Add other credential
                <span className="conn-menu-sub">anything with an API key</span>
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
};

// The one place a connection is created, for both kinds. A pasted credential
// saves here; an OAuth account leaves for the provider's consent screen, which
// is why the button says where it is going.
//
// Two fields, deliberately distinct: the NAME is for the person, free text and
// changeable; the REFERENCE is what an app asks for, and is derived from the
// name so nobody has to invent both.
const AddConnectionDialog = ({ provider, existing, onClose, onAdded }) => {
  const [label, setLabel] = useState("");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [values, setValues] = useState({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const oauth = provider.kind === "oauth";

  // Until the reference is edited by hand it follows the name; once touched it
  // stays put, because renaming a connection must not silently move what apps
  // ask for.
  const reference = slugEdited ? slug : slugify(label) || suggestSlug(provider, existing);
  const missing = (provider.fields || []).some((f) => !f.optional && !(values[f.name] || "").trim());
  const valid = label.trim().length > 0 && reference.length >= 3 && !missing;

  const submit = async (e) => {
    e.preventDefault();
    if (busy || !valid) return;
    setBusy(true);
    setError("");
    try {
      const res = await api.post("/api/connections", {
        provider: provider.name,
        slug: reference,
        label: label.trim(),
        values,
      });
      if (res.redirect_url) {
        window.location.href = res.redirect_url;
        return;
      }
      onAdded();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <Modal onClose={onClose} title={`${oauth ? "Connect" : "Add"} ${provider.label}`} help={provider.help}>
      <form onSubmit={submit}>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <label className="conn-field">
          <span>Name</span>
          <input
            type="text"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder={oauth ? "Work calendar" : "OpenAI key"}
            aria-label="Name"
            autoFocus
            disabled={busy}
          />
        </label>
        <label className="conn-field">
          <span>Reference</span>
          <input
            type="text"
            className="mono"
            value={reference}
            onChange={(e) => {
              setSlugEdited(true);
              setSlug(e.target.value);
            }}
            placeholder="work-calendar"
            aria-label="Reference"
            disabled={busy}
          />
        </label>
        <p className="hint">
          What an app asks for:{" "}
          <span className="mono">/v1/connections/{reference || "work-calendar"}/token</span>. Renaming
          it later breaks any app already using the old one.
        </p>
        {(provider.fields || []).map((f) => (
          <label key={f.name} className="conn-field">
            <span>{f.label}{f.optional ? " (optional)" : ""}</span>
            {f.multiline ? (
              <textarea
                rows={7}
                className="mono conn-textarea"
                value={values[f.name] || ""}
                onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
                placeholder={f.placeholder}
                aria-label={f.label}
                disabled={busy}
                spellCheck={false}
              />
            ) : (
              <input
                type={f.secret ? "password" : "text"}
                value={values[f.name] || ""}
                onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
                placeholder={f.placeholder}
                aria-label={f.label}
                disabled={busy}
              />
            )}
          </label>
        ))}
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !valid}>
            {busy && <span className="newapp-spinner" aria-hidden="true" />}
            {oauth ? `Continue to ${provider.label}` : busy ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </Modal>
  );
};

// Editing a connection changes both halves independently: the name is cosmetic,
// the reference is what apps address it by -- so the dialog says which is which.
const RenameConnectionDialog = ({ conn, busy, onClose, onSave }) => {
  const [label, setLabel] = useState(conn.label || "");
  const [slug, setSlug] = useState(conn.slug);
  const changed = slug.trim().toLowerCase() !== conn.slug || label.trim() !== (conn.label || "");

  return (
    <Modal onClose={onClose} title={`Edit ${conn.label || conn.slug}`}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSave(conn, slug.trim().toLowerCase(), label.trim());
        }}
      >
        <label className="conn-field">
          <span>Name</span>
          <input type="text" value={label} onChange={(e) => setLabel(e.target.value)} aria-label="Name" autoFocus disabled={busy} />
        </label>
        <p className="hint">What you see here. Changing it affects nothing else.</p>
        <label className="conn-field">
          <span>Reference</span>
          <input type="text" className="mono" value={slug} onChange={(e) => setSlug(e.target.value)} aria-label="Reference" disabled={busy} />
        </label>
        <p className="hint">
          What apps ask for. Changing it breaks any app already using{" "}
          <span className="mono">{conn.slug}</span> until that app is updated too.
        </p>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !changed || slug.trim().length < 3}>
            {busy ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </Modal>
  );
};

// The shared modal chrome, so the two dialogs differ only in their fields.
const Modal = ({ title, help, onClose, children }) => (
  <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
    <div className="card modal modal-sheet" onMouseDown={(e) => e.stopPropagation()}>
      <button type="button" className="modal-x" onClick={onClose} title="Close" aria-label="Close">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
          <path d="M6 6l12 12M18 6 6 18" />
        </svg>
      </button>
      <h2>{title}</h2>
      {help && <p className="hint" style={{ marginBottom: "5px" }}>{help}</p>}
      {children}
    </div>
  </div>
);

export default Connections;
