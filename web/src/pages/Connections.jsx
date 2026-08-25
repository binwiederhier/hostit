import { useCallback, useEffect, useState } from "react";
import { filterProviders, filterTools, slugify, splitByKind, suggestSlug, defaultScopeKeys } from "../connections";
import { api } from "../api";
import { useDropdown } from "../hooks";
import { ConfirmDialog, DocsLink, ErrorBanner, Skeleton, Snippet } from "../components";

// Connections: the accounts, secrets and tool servers an owner attaches once and
// can then grant to their apps. THREE cards, not one, because they are three
// different things to a person -- an account is something you SIGN IN to, a
// pasted key is something you STORE, an MCP server is a set of TOOLS -- and one
// heading makes all but one of them read wrong.
//
// "Connections" is the umbrella here and nothing else. The oauth card is called
// Accounts precisely so the umbrella word is not also the name of one of the
// things under it, which is what it used to be.
//
// The credential itself is never shown or returned. This page says WHAT is
// attached; each app's settings say which of them that app may use.
const Connections = () => {
  const [data, setData] = useState(null);
  const [defs, setDefs] = useState(null);
  const [editingProvider, setEditingProvider] = useState(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(null);
  const [renaming, setRenaming] = useState(null);
  const [removing, setRemoving] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [conns, providers] = await Promise.all([
        api.get("/api/connections"),
        api.get("/api/providers"),
      ]);
      setData(conns);
      setDefs(providers);
    } catch (err) {
      setError(err.message);
      setData({ connections: [], providers: [] });
      setDefs({ providers: [], redirect_uri: "" });
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const providers = data?.providers || [];
  const { accounts, credentials, servers } = splitByKind(data?.connections);
  const mcpProvider = providers.find((p) => p.kind === "mcp") || null;

  const reconnect = async (c) => {
    setError("");
    try {
      const res = await api.post(`/api/connections/${encodeURIComponent(c.slug)}/reconnect`, {});
      window.location.href = res.redirect_url;
    } catch (err) {
      setError(err.message);
    }
  };

  const edit = async (c, nextSlug, nextLabel, values) => {
    setError("");
    setBusy(true);
    try {
      const body = { slug: nextSlug, label: nextLabel };
      // Only sent when something was actually typed: an empty form means
      // "leave the credential alone", not "replace it with nothing".
      if (values && Object.values(values).some((v) => (v || "").trim() !== "")) {
        body.values = values;
      }
      await api.put(`/api/connections/${encodeURIComponent(c.slug)}`, body);
      setRenaming(null);
      await load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (c) => {
    setError("");
    setBusy(true);
    try {
      await api.del(`/api/connections/${encodeURIComponent(c.slug)}`);
      setRemoving(null);
      await load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const shared = { onRename: setRenaming, onReconnect: reconnect, onRemove: setRemoving, onAdd: setAdding };

  return (
    <>
      <div className="page-header">
        <h1>Connections</h1>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />

      <ConnectionsCard
        title="Accounts"
        hint={
          <>
            Accounts you sign in to once and then grant to individual apps, on the app&rsquo;s
            Connections tab. Apps act as you, and only reach what you grant them. Connect the same
            service twice -- two calendars, say -- and each keeps its own reference.{" "}
            <DocsLink guide="user" section="connections" sub="accounts">How accounts work</DocsLink>
          </>
        }
        emptyText="No accounts connected yet."
        cta="Add account"
        items={accounts}
        providers={providers.filter((p) => p.kind === "oauth")}
        loading={data === null && !error}
        noProvidersText="No accounts can be connected on this server yet: an admin sets each provider's OAuth client in control.yml."
        onAddOwn={() => setEditingProvider({})}
        {...shared}
      />
      <ConnectionsCard
        title="Credentials"
        hint={
          <>
            API keys, tokens, SSH keys, database URLs and mailbox passwords you paste in. Stored
            encrypted, and handed to an app only when you grant it. Nothing here needs an OAuth
            client or any review.{" "}
            <DocsLink guide="user" section="connections" sub="credentials">How credentials work</DocsLink>
          </>
        }
        emptyText="No credentials stored yet."
        cta="Add credential"
        items={credentials}
        providers={providers.filter((p) => p.kind === "static")}
        loading={data === null && !error}
        {...shared}
      />
      <ConnectionsCard
        title="MCP servers"
        hint={
          <>
            Tool servers you point hostit at by URL. hostit signs in if the server asks it to,
            holds the token, and makes the calls -- so a granted app calls tools by name and
            never holds a credential that would open the whole server. The tools also show up
            in the assistant.{" "}
            <DocsLink guide="user" section="connections" sub="mcp">How MCP servers work</DocsLink>
          </>
        }
        emptyText="No MCP servers connected yet."
        cta="Add MCP server"
        items={servers}
        singleProvider={mcpProvider}
        presets={data?.mcp_servers || []}
        loading={data === null && !error}
        noProvidersText="MCP is not available on this server."
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
      {defs && <MyProvidersCard defs={defs} onEdit={setEditingProvider} onChanged={load} onError={setError} />}

      {editingProvider && (
        <ProviderDialog
          existing={editingProvider.name ? editingProvider : null}
          redirectURI={defs?.redirect_uri || ""}
          onClose={() => setEditingProvider(null)}
          onSaved={async () => {
            setEditingProvider(null);
            await load();
          }}
        />
      )}
      {renaming && (
        <EditConnectionDialog
          conn={renaming}
          provider={providers.find((p) => p.name === renaming.provider)}
          busy={busy}
          onClose={() => setRenaming(null)}
          onSave={edit}
        />
      )}
      {removing && (
        <ConfirmDialog
          title={`Remove ${removing.label || removing.slug}?`}
          confirmLabel="Remove"
          busy={busy}
          onClose={() => setRemoving(null)}
          onConfirm={() => remove(removing)}
          body={
            <>
              The stored credential is deleted and cannot be recovered.{" "}
              {removing.granted_apps > 0 ? (
                <>
                  <b>
                    {removing.granted_apps} app{removing.granted_apps === 1 ? "" : "s"}
                  </b>{" "}
                  {removing.granted_apps === 1 ? "loses" : "lose"} access immediately.
                </>
              ) : (
                <>No app is using it.</>
              )}
              {removing.kind === "oauth" && (
                <> Reconnecting later means approving it at the provider again.</>
              )}
            </>
          }
        />
      )}
    </>
  );
};

// The user's OWN provider definitions -- services hostit does not ship and the
// operator has not set up, which they registered an OAuth app with themselves.
//
// Shown only once they have one. An empty card explaining a concept nobody has
// used yet is noise on a page that already has three; the way in is the line
// under the Accounts card.
const MyProvidersCard = ({ defs, onEdit, onChanged, onError }) => {
  const [removing, setRemoving] = useState(null);
  const [busy, setBusy] = useState(false);
  const mine = (defs.providers || []).filter((p) => p.scope === "personal" && p.editable && p.kind !== "mcp");
  if (mine.length === 0) return null;

  const remove = async (p) => {
    setBusy(true);
    onError("");
    try {
      await api.del(`/api/providers/${encodeURIComponent(p.name)}`);
      setRemoving(null);
      await onChanged();
    } catch (err) {
      onError(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card">
      <div className="conn-head">
        <h2>Your own services</h2>
        <button type="button" className="btn btn-primary btn-small" onClick={() => onEdit({})}>
          Add service
        </button>
      </div>
      <p className="hint">
        Services you registered an OAuth app with yourself. Only you can see or use these; they
        appear in your <b>Add account</b> menu alongside the ones hostit ships.{" "}
        <DocsLink guide="user" section="connections" sub="own">How this works</DocsLink>
      </p>
      {mine.map((p) => (
        <div key={p.name} className="conn-row">
          <div className="conn-id">
            <span className="conn-name">
              {p.label}
              <span className="conn-provider">OAuth</span>
            </span>
            <span className="conn-note">
              connect as <span className="mono">{p.name}</span>
              {" -- "}client <span className="mono">{p.client_id}</span>
            </span>
          </div>
          <div className="menu conn-rowmenu">
            <button type="button" className="btn btn-small" onClick={() => onEdit(p)}>Edit</button>
            <button type="button" className="btn btn-small" onClick={() => setRemoving(p)}>Remove</button>
          </div>
        </div>
      ))}
      {removing && (
        <ConfirmDialog
          title={`Remove ${removing.label}?`}
          confirmLabel="Remove"
          busy={busy}
          onClose={() => setRemoving(null)}
          onConfirm={() => remove(removing)}
          body={
            <>
              The definition goes; accounts already connected through it keep working until their
              token expires and then cannot be refreshed. Your OAuth app at the service itself is
              untouched.
            </>
          }
        />
      )}
    </div>
  );
};

// Defining a service. The redirect URI is shown first and copyable, because it
// is the one value the person cannot work out and the one that fails the whole
// flow at the vendor if it is wrong.
const ProviderDialog = ({ existing, redirectURI, onClose, onSaved }) => {
  const [label, setLabel] = useState(existing?.label || "");
  const [name, setName] = useState(existing?.name || "");
  const [nameEdited, setNameEdited] = useState(Boolean(existing));
  const [form, setForm] = useState({
    client_id: existing?.client_id || "",
    client_secret: "",
    auth_url: existing?.auth_url || "",
    token_url: existing?.token_url || "",
    issuer: existing?.issuer || "",
    scopes: (existing?.scopes || []).join(" "),
  });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (k) => (e) => setForm({ ...form, [k]: e.target.value });

  const reference = nameEdited ? name : slugify(label);
  const endpointsOK = form.issuer.trim() || (form.auth_url.trim() && form.token_url.trim());
  const valid =
    label.trim() && reference.length >= 3 && endpointsOK &&
    form.client_id.trim() && (existing || form.client_secret.trim());

  const submit = async (e) => {
    e.preventDefault();
    if (busy || !valid) return;
    setBusy(true);
    setError("");
    const body = {
      name: reference, label: label.trim(), kind: "oauth",
      client_id: form.client_id.trim(),
      client_secret: form.client_secret.trim(),
      auth_url: form.auth_url.trim(),
      token_url: form.token_url.trim(),
      issuer: form.issuer.trim(),
      scopes: form.scopes.split(/\s+/).filter(Boolean),
    };
    try {
      if (existing) {
        await api.put(`/api/providers/${encodeURIComponent(existing.name)}`, body);
      } else {
        await api.post("/api/providers", body);
      }
      onSaved();
    } catch (err) {
      setError(err.message);
      setBusy(false);
    }
  };

  return (
    <Modal wide onClose={onClose} title={existing ? `Edit ${existing.label}` : "Add your own service"}>
      <form onSubmit={submit}>
        <ErrorBanner message={error} onDismiss={() => setError("")} />
        <p className="hint">
          For a service you sign in to. Register an OAuth app with them first, giving it this
          callback URL:
        </p>
        <Snippet text={redirectURI} />
        <p className="hint">
          An <b>MCP server</b> is not this: you add one straight from the MCP servers card by
          pasting its URL, with nothing to register.
        </p>
        {/* Paired across two columns: these are six short fields, and one
            column of them made a dialog taller than most windows. Each pair is
            two halves of one idea -- what it is called, what the client is,
            where it lives -- so they read across rather than down. */}
        <div className="conn-grid">
          <label className="conn-field">
            <span>Name</span>
            <input type="text" value={label} onChange={(e) => setLabel(e.target.value)}
              placeholder="Acme" aria-label="Service name" autoFocus disabled={busy} />
          </label>
          <label className="conn-field">
            <span>Reference</span>
            <input type="text" className="mono" value={reference}
              onChange={(e) => { setNameEdited(true); setName(e.target.value); }}
              aria-label="Reference" disabled={busy || Boolean(existing)} />
          </label>
          <label className="conn-field">
            <span>Client ID</span>
            <input type="text" className="mono" value={form.client_id} onChange={set("client_id")}
              aria-label="Client ID" disabled={busy} />
          </label>
          <label className="conn-field">
            <span>Client secret{existing ? " (leave blank to keep)" : ""}</span>
            <input type="password" value={form.client_secret} onChange={set("client_secret")}
              aria-label="Client secret" disabled={busy} />
          </label>
          <label className="conn-field">
            <span>Scopes</span>
            <input type="text" className="mono" value={form.scopes} onChange={set("scopes")}
              placeholder="read write" aria-label="Scopes" disabled={busy} />
          </label>
          <label className="conn-field">
            <span>Issuer (optional)</span>
            <input type="text" className="mono" value={form.issuer} onChange={set("issuer")}
              placeholder="https://acme.example.com" aria-label="Issuer" disabled={busy} />
          </label>
          <label className="conn-field">
            <span>Authorize URL</span>
            <input type="text" className="mono" value={form.auth_url} onChange={set("auth_url")}
              placeholder="https://acme.example.com/oauth/authorize" aria-label="Authorize URL" disabled={busy} />
          </label>
          <label className="conn-field">
            <span>Token URL</span>
            <input type="text" className="mono" value={form.token_url} onChange={set("token_url")}
              placeholder="https://acme.example.com/oauth/token" aria-label="Token URL" disabled={busy} />
          </label>
        </div>
        <p className="hint">
          Give an <b>issuer</b> and hostit finds the authorize and token URLs itself. Otherwise fill
          both in from the service&rsquo;s documentation.
        </p>
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !valid}>
            {busy ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </Modal>
  );
};

// One of the two cards. Both are the same shape -- what is attached, and one
// button to attach more -- so they share a component rather than being copied
// with the nouns changed.
const ConnectionsCard = ({ title, hint, emptyText, cta, items, providers, singleProvider, presets, loading, onAdd, onRename, onReconnect, onRemove, noProvidersText, onAddOwn }) => (
  <div className="card">
    <div className="conn-head">
      <h2>{title}</h2>
      {/* One provider means no menu: a dropdown with a single entry is a button
          wearing a costume. */}
      {singleProvider !== undefined && (presets || []).length > 0 ? (
        // Named servers turn "remember a URL" into "pick a name". Pasting a URL
        // is still right there, because the named list is a shortcut and never
        // a restriction.
        <PresetMenu label={cta} presets={presets} provider={singleProvider} onPick={onAdd} />
      ) : singleProvider !== undefined ? (
        <button
          type="button"
          className="btn btn-primary btn-small"
          onClick={() => onAdd(singleProvider)}
          disabled={!singleProvider}
          title={!singleProvider ? noProvidersText : undefined}
        >
          {cta}
        </button>
      ) : (
        <AddMenu label={cta} providers={providers} onPick={onAdd} disabledText={noProvidersText} onAddOwn={onAddOwn} />
      )}
    </div>
    <p className="hint">{hint}</p>
    {loading && <Skeleton rows={3} label={`Loading ${title.toLowerCase()}...`} />}
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
            {c.kind === "mcp" && <MCPDetail conn={c} />}
          </div>
          <RowMenu conn={c} onRename={onRename} onReconnect={onReconnect} onRemove={onRemove} />
        </div>
      ))}
    {noProvidersText && singleProvider === undefined && providers.length === 0 && (
      <p className="hint">{noProvidersText}</p>
    )}
  </div>
);

// What an MCP server actually offers. The endpoint sits on the row; the tools do
// NOT -- a server is entitled to expose hundreds, and inlining them would bury
// every other connection under one of them. The count is the affordance and the
// list is a dialog, which is also the only place a search fits.
const MCPDetail = ({ conn }) => {
  const [open, setOpen] = useState(false);
  const tools = conn.tools || [];
  // The dialog is a SIBLING of the note, not a child of it: a modal is a div,
  // and a div inside a span is invalid nesting -- which also let the note's
  // break-all leak into the dialog and hyphenate its prose mid-word.
  return (
    <>
      <span className="conn-note conn-mcp">
        <span className="mono conn-mcp-url">{conn.url}</span>
        {tools.length === 0 ? (
          <> {"--"} no tools listed yet</>
        ) : (
          <>
            {" -- "}
            <button type="button" className="linkbtn" onClick={() => setOpen(true)}>
              {tools.length} tool{tools.length === 1 ? "" : "s"}
            </button>
          </>
        )}
      </span>
      {open && <ToolsDialog conn={conn} tools={tools} onClose={() => setOpen(false)} />}
    </>
  );
};

// The tool list. Search appears only once there are enough tools to need it:
// a box above three entries is furniture, above ninety it is the only way in.
const toolsSearchThreshold = 8;

const ToolsDialog = ({ conn, tools, onClose }) => {
  const [query, setQuery] = useState("");
  const shown = filterTools(tools, query);
  return (
    <Modal wide onClose={onClose} title={`${conn.label || conn.slug}: ${tools.length} tool${tools.length === 1 ? "" : "s"}`}>
      <p className="hint">
        What this server offers. A granted app calls any of them by name at{" "}
        <span className="mono">/api/container/mcp/{conn.slug}/call</span>, and they are also in the
        assistant&rsquo;s own tool list.
      </p>
      {tools.length >= toolsSearchThreshold && (
        <input
          type="text"
          className="conn-menu-search conn-tools-search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search tools..."
          aria-label="Search tools"
          autoFocus
        />
      )}
      {query.trim() !== "" && shown.length > 0 && (
        <p className="hint conn-tools-count">
          {shown.length} of {tools.length}
        </p>
      )}
      <ul className="conn-tools conn-tools-list">
        {shown.map((t) => (
          <li key={t.name}>
            <span className="mono">{t.name}</span>
            {t.description ? <span className="conn-tool-desc">{t.description}</span> : null}
          </li>
        ))}
      </ul>
      {shown.length === 0 && <p className="hint">Nothing matches &ldquo;{query}&rdquo;</p>}
      <div className="btn-row">
        {/* "Done" rather than "Close": the modal chrome already has a Close in
            its corner, and two controls with the same name in one dialog is
            exactly what a screen reader cannot disambiguate. */}
        <button type="button" className="btn" onClick={onClose}>Done</button>
      </div>
    </Modal>
  );
};

// Three dots rather than three buttons. The row is about WHAT is attached; the
// things you can do to it are one click away instead of competing with it.
const RowMenu = ({ conn, onRename, onReconnect, onRemove }) => {
  const { open, setOpen, ref } = useDropdown();
  const pick = (fn) => () => {
    setOpen(false);
    fn(conn);
  };
  return (
    <div className="menu conn-rowmenu" ref={ref}>
      <button
        type="button"
        className="btn btn-icon conn-kebab"
        onClick={() => setOpen(!open)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Actions for ${conn.label || conn.slug}`}
      >
        <DotsIcon />
      </button>
      {open && (
        <div className="menu-items" role="menu">
          <button type="button" role="menuitem" onClick={pick(onRename)}>Edit</button>
          {(conn.kind === "oauth" || conn.kind === "mcp") && (
            <button type="button" role="menuitem" onClick={pick(onReconnect)}>Reconnect</button>
          )}
          <button type="button" role="menuitem" className="menu-item-danger" onClick={pick(onRemove)}>Remove</button>
        </div>
      )}
    </div>
  );
};

// The MCP call to action once there are named servers to pick from.
const PresetMenu = ({ label, presets, provider, onPick }) => {
  const { open, setOpen, ref } = useDropdown();
  const choose = (preset) => {
    setOpen(false);
    onPick({ ...provider, preset });
  };
  return (
    <div className="menu" ref={ref}>
      <button type="button" className="btn btn-primary btn-small" onClick={() => setOpen(!open)}
        aria-haspopup="menu" aria-expanded={open} disabled={!provider}>
        {label}
        <span className="conn-caret" aria-hidden="true">&#9662;</span>
      </button>
      {open && (
        <div className="menu-items" role="menu">
          {presets.map((p) => (
            <button key={p.name} type="button" role="menuitem" onClick={() => choose(p)}>
              {p.label}
              {p.personal && <span className="conn-menu-badge">yours</span>}
            </button>
          ))}
          <div className="conn-menu-divider" role="separator" />
          <button type="button" role="menuitem" className="conn-menu-other" onClick={() => choose(null)}>
            Any other server
            <span className="conn-menu-sub">paste a URL</span>
          </button>
        </div>
      )}
    </div>
  );
};

const DotsIcon = () => (
  <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
    <circle cx="8" cy="3.1" r="1.35" />
    <circle cx="8" cy="8" r="1.35" />
    <circle cx="8" cy="12.9" r="1.35" />
  </svg>
);

// One call to action per card, dropping a menu of what can be attached. The
// catch-all sits below a divider and is styled apart: it is the escape hatch for
// anything hostit does not know, not just another entry in the list.
const AddMenu = ({ label, providers, onPick, disabledText, onAddOwn }) => {
  const { open, setOpen, ref } = useDropdown();
  const [query, setQuery] = useState("");
  const { named, other } = filterProviders(providers, query);
  // With "add your own" in the menu there is always something to pick, so the
  // button is only dead when there is genuinely nothing at all.
  const empty = (providers || []).length === 0 && !onAddOwn;

  // Reopening should not inherit the last search.
  const toggle = () => {
    setQuery("");
    setOpen(!open);
  };
  const choose = (p) => {
    setOpen(false);
    setQuery("");
    onPick(p);
  };

  return (
    <div className="menu" ref={ref}>
      <button
        type="button"
        className="btn btn-primary btn-small"
        onClick={toggle}
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
          <input
            type="text"
            className="conn-menu-search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search..."
            aria-label="Search providers"
            autoFocus
          />
          {/* Two columns: the list is long enough that one would run off the
              bottom of the screen. */}
          <div className="conn-menu-grid">
            {named.map((p) => (
              <button key={p.name} type="button" role="menuitem" className="conn-menu-item" onClick={() => choose(p)}>
                {p.label}
                {p.custom && <span className="conn-menu-badge">yours</span>}
              </button>
            ))}
          </div>
          {named.length === 0 && <p className="conn-menu-none">Nothing matches &ldquo;{query}&rdquo;</p>}
          {other && (
            <>
              <div className="conn-menu-divider" role="separator" />
              <button type="button" role="menuitem" className="conn-menu-item conn-menu-other" onClick={() => choose(other)}>
                Add other credential
                <span className="conn-menu-sub">anything with an API key</span>
              </button>
            </>
          )}
          {onAddOwn && (
            <>
              <div className="conn-menu-divider" role="separator" />
              <button
                type="button"
                role="menuitem"
                className="conn-menu-item conn-menu-other"
                onClick={() => {
                  setOpen(false);
                  setQuery("");
                  onAddOwn();
                }}
              >
                Add your own service
                <span className="conn-menu-sub">register an OAuth app and paste the client</span>
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
  const preset = provider.preset || null;
  const [label, setLabel] = useState(preset?.label || "");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [values, setValues] = useState(preset ? { url: preset.url } : {});
  const scopeOptions = provider.scope_options || [];
  const [scopeKeys, setScopeKeys] = useState(() => defaultScopeKeys(provider));
  const toggleScope = (key, on) => setScopeKeys((keys) => (on ? [...keys, key] : keys.filter((k) => k !== key)));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const oauth = provider.kind === "oauth";
  const mcp = provider.kind === "mcp";

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
      const body = { provider: provider.name, slug: reference, label: label.trim(), values };
      if (scopeOptions.length > 0) {
        body.scope_keys = scopeKeys;
      }
      const res = await api.post("/api/connections", body);
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
            placeholder={provider.name_hint || provider.label}
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
          <span className="mono">
            {mcp
              ? `/api/container/mcp/${reference || "issues"}/call`
              : `/api/container/connections/${reference || "work-calendar"}/token`}
          </span>
          . Renaming it later breaks any app already using the old one.
        </p>
        {scopeOptions.length > 0 && (
          <fieldset className="conn-scopes">
            <legend>What should this connection be able to read?</legend>
            {scopeOptions.map((o) => (
              <label key={o.key} className="conn-scope">
                <input
                  type="checkbox"
                  checked={scopeKeys.includes(o.key)}
                  onChange={(e) => toggleScope(o.key, e.target.checked)}
                  disabled={busy}
                />
                <span className="conn-scope-text">
                  <span className="conn-scope-label">{o.label}</span>
                  {o.help && <span className="conn-scope-help">{o.help}</span>}
                </span>
              </label>
            ))}
          </fieldset>
        )}
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
            {oauth ? `Continue to ${provider.label}` : busy ? mcpBusyLabel(mcp) : mcp ? "Connect" : "Save"}
          </button>
        </div>
      </form>
    </Modal>
  );
};

// An MCP server is contacted while the dialog waits: discovery is a round trip
// to a server that may want authorization, which takes longer than saving a
// pasted key and should not look like a hang.
const mcpBusyLabel = (mcp) => (mcp ? "Contacting server..." : "Saving...");

// Editing a connection. The name and the reference do different jobs, so the
// dialog says which is which -- and for a PASTED credential the secret itself is
// editable here too.
//
// It was not, which made a rotated API key mean deleting the credential and
// adding it again, losing every grant in the process. The API always supported
// it; only the dialog did not ask.
const EditConnectionDialog = ({ conn, provider, busy, onClose, onSave }) => {
  const [label, setLabel] = useState(conn.label || "");
  const [slug, setSlug] = useState(conn.slug);
  const [values, setValues] = useState({});
  const editableFields = conn.kind === "static" ? provider?.fields || [] : [];
  const touched = Object.values(values).some((v) => (v || "").trim() !== "");
  // A partly-filled credential would replace the whole thing with half of it,
  // so every required field has to be present once any of them is.
  const incomplete =
    touched && editableFields.some((f) => !f.optional && !(values[f.name] || "").trim());
  const changed =
    slug.trim().toLowerCase() !== conn.slug || label.trim() !== (conn.label || "") || touched;

  return (
    <Modal onClose={onClose} title={`Edit ${conn.label || conn.slug}`}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSave(conn, slug.trim().toLowerCase(), label.trim(), touched ? values : null);
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
        {editableFields.length > 0 && (
          <>
            <div className="conn-editdiv" />
            <p className="hint">
              Replace the credential -- after rotating a key, say. Leave these blank to keep what is
              stored; every grant survives either way, so this is always better than removing it and
              adding it again.
            </p>
            {editableFields.map((f) => (
              <label key={f.name} className="conn-field">
                <span>{f.label}{f.optional ? " (optional)" : ""}</span>
                {f.multiline ? (
                  <textarea
                    rows={7}
                    className="mono conn-textarea"
                    value={values[f.name] || ""}
                    onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
                    placeholder={f.secret ? "unchanged" : f.placeholder}
                    aria-label={f.label}
                    disabled={busy}
                    spellCheck={false}
                  />
                ) : (
                  <input
                    type={f.secret ? "password" : "text"}
                    value={values[f.name] || ""}
                    onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
                    placeholder={f.secret ? "unchanged" : f.placeholder}
                    aria-label={f.label}
                    disabled={busy}
                  />
                )}
              </label>
            ))}
            {incomplete && (
              <p className="hint conn-warn">
                Replacing the credential replaces all of it, so fill in every field above or clear
                them to leave it alone.
              </p>
            )}
          </>
        )}
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={busy || !changed || incomplete || slug.trim().length < 3}>
            {busy ? "Saving..." : "Save"}
          </button>
        </div>
      </form>
    </Modal>
  );
};

// The shared modal chrome, so the two dialogs differ only in their fields.
const Modal = ({ title, help, wide, onClose, children }) => (
  <div className="modal-backdrop" role="dialog" aria-modal="true" onMouseDown={onClose}>
    <div className={"card modal modal-sheet" + (wide ? " modal-tools" : "")} onMouseDown={(e) => e.stopPropagation()}>
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
