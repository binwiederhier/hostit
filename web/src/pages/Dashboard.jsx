import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { CopyButton, ErrorBanner, Loading, Snippet } from "../components";

const nameRe = /^[a-z][a-z0-9-]{0,30}[a-z0-9]$/;

// The SPA is served from the hostit daemon itself, so the API host is our own
// origin; the SSH host is the apps domain, i.e. the hostname without the
// leading "hostit." label (hostit.apps.example.com -> apps.example.com).
const apiHost = window.location.origin;
const sshHost = window.location.hostname.replace(/^hostit\./, "");

// Shown once after app creation; includes the SSH command and, if hostit
// generated a keypair, the private key with a save-it-now warning.
const CreatedBox = ({ created, onDismiss }) => (
  <div className="card created-card">
    <div className="card-header">
      <h3>
        App <span className="mono">{created.name}</span> created
      </h3>
      <button type="button" className="btn btn-small" onClick={onDismiss}>
        Dismiss
      </button>
    </div>
    <p>
      It will be served at{" "}
      <a href={created.url} target="_blank" rel="noreferrer">
        {created.url}
      </a>{" "}
      (port {created.port}). Log in via SSH to upload your files:
    </p>
    {created.ssh && <Snippet text={created.ssh.command} />}
    {created.private_key && (
      <div className="warn-box">
        <p>
          <strong>This private key is shown only once. Save it now.</strong> hostit generated it for you because no SSH key was
          provided; it grants SSH access to your apps.
        </p>
        <pre className="key-block">{created.private_key}</pre>
        <CopyButton text={created.private_key} small={false}>
          Copy private key
        </CopyButton>
      </div>
    )}
  </div>
);

const ClaudePanel = () => (
  <div className="card claude-panel">
    <h2>Use with Claude</h2>
    <p>
      The fastest way to use hostit is to let an agent drive it. Create an{" "}
      <Link to="/profile">API token in your profile</Link>, then point the <span className="mono">hostit</span> CLI at this
      server:
    </p>
    <Snippet
      text={`export HOSTIT_HOST=${apiHost}\nexport HOSTIT_TOKEN=<your token>\nhostit admin add myapp -k ~/.ssh/id_ed25519.pub`}
    />
    <p>
      After creating an app, SSH into it, put your files there, write a <span className="mono">hostit.yml</span>, and bring it
      up:
    </p>
    <Snippet text={`ssh myapp@${sshHost}\n# upload files, write hostit.yml, then:\nhostit up`} />
    <p className="hint">
      Tip: paste this page's instructions into Claude and tell it what to deploy; it can do the rest on its own.
    </p>
  </div>
);

const AppRow = ({ app, account, isAdmin, onDelete }) => {
  const own = !isAdmin || app.owner_email === account.email;
  return (
    <tr>
      <td>
        <a className="mono app-link" href={app.url} target="_blank" rel="noreferrer">
          {app.name}
        </a>
      </td>
      {isAdmin && <td className="cell-muted">{app.owner_email}</td>}
      <td className="mono">{app.port}</td>
      <td>
        {app.disk_mb} MB{own && ` of ${account.limits.disk_mb} MB`}
        {app.over_quota && <span className="badge badge-danger">over quota</span>}
      </td>
      <td className="cell-actions">
        <button type="button" className="btn btn-small btn-danger" onClick={() => onDelete(app)}>
          Delete
        </button>
      </td>
    </tr>
  );
};

const Dashboard = ({ account, refreshAccount }) => {
  const [apps, setApps] = useState(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState(null);

  const isAdmin = account.role === "admin";
  const atLimit = account.usage.apps >= account.limits.app_limit;
  const nameValid = nameRe.test(name);

  const load = useCallback(async () => {
    try {
      setApps(await api.get("/v1/apps"));
    } catch (err) {
      setError(err.message);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const create = async (e) => {
    e.preventDefault();
    if (!nameValid || creating) {
      return;
    }
    setCreating(true);
    setError("");
    try {
      const res = await api.post("/v1/apps", { name });
      setCreated(res);
      setName("");
      await load();
      refreshAccount();
    } catch (err) {
      setError(err.message);
    } finally {
      setCreating(false);
    }
  };

  const remove = async (app) => {
    if (!window.confirm(`Delete app "${app.name}"? This permanently deletes its container, files and user.`)) {
      return;
    }
    setError("");
    try {
      await api.del(`/v1/apps/${app.name}`);
      await load();
      refreshAccount();
    } catch (err) {
      setError(err.message);
    }
  };

  return (
    <>
      <div className="page-header">
        <h1>Apps</h1>
        <span className="usage">
          {account.usage.apps} of {account.limits.app_limit} apps used
        </span>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {created && <CreatedBox created={created} onDismiss={() => setCreated(null)} />}
      <div className="card">
        {apps === null && !error && <Loading label="Loading apps..." />}
        {apps !== null && apps.length === 0 && (
          <p className="empty">No apps yet. Create one below, or let Claude do it for you.</p>
        )}
        {apps !== null && apps.length > 0 && (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  {isAdmin && <th>Owner</th>}
                  <th>Port</th>
                  <th>Disk</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {apps.map((app) => (
                  <AppRow key={app.name} app={app} account={account} isAdmin={isAdmin} onDelete={remove} />
                ))}
              </tbody>
            </table>
          </div>
        )}
        <form className="inline-form" onSubmit={create}>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="app name, e.g. blog"
            disabled={atLimit || creating}
            aria-label="New app name"
          />
          <button type="submit" className="btn btn-primary" disabled={atLimit || creating || !nameValid}>
            {creating ? "Creating..." : "New app"}
          </button>
        </form>
        {atLimit && <p className="hint">You have reached your app limit. Delete an app to create a new one.</p>}
        {!atLimit && name !== "" && !nameValid && (
          <p className="hint">Names are 2-32 characters: lowercase letters, digits and dashes, starting with a letter.</p>
        )}
      </div>
      <ClaudePanel />
    </>
  );
};

export default Dashboard;
