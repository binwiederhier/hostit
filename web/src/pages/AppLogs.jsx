import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../api";
import { ErrorBanner } from "../components";

// AppLogs is the workspace "Logs" view: an activity log of who did what to the app
// (create, deploy, snapshot, rollback, domain, lifecycle) above a live tail of the
// app's own container output. It polls only while the tab is the active one.
export default function AppLogs({ name, active }) {
  const [events, setEvents] = useState(null);
  const [output, setOutput] = useState(null);
  const [error, setError] = useState("");
  const [follow, setFollow] = useState(true);
  const outRef = useRef(null);

  const loadEvents = useCallback(async () => {
    try {
      const list = await api.get(`/api/apps/${encodeURIComponent(name)}/events`);
      setEvents(Array.isArray(list) ? list : []);
    } catch (err) {
      setError(err.message);
    }
  }, [name]);

  const loadOutput = useCallback(async () => {
    try {
      const resp = await api.get(`/api/apps/${encodeURIComponent(name)}/logs?lines=300`);
      setOutput(resp.output || "");
    } catch {
      setOutput(""); // an idle app has no output yet; not an error worth showing
    }
  }, [name]);

  // Refresh on open, then poll while active (the output only when following).
  useEffect(() => {
    if (!active) return undefined;
    loadEvents();
    loadOutput();
    const id = setInterval(() => {
      loadEvents();
      if (follow) loadOutput();
    }, 5000);
    return () => clearInterval(id);
  }, [active, follow, loadEvents, loadOutput]);

  // Keep the tail pinned to the bottom while following.
  useEffect(() => {
    if (follow && outRef.current) outRef.current.scrollTop = outRef.current.scrollHeight;
  }, [output, follow]);

  return (
    <div className="ov logs-view">
      <div className="ov-hero ov-hero-lite">
        <div className="ov-id">
          <div className="ov-nm">Logs</div>
          <div className="ov-desc">
            What has been done to this app -- deploys, restarts, grants, domain changes -- above a
            live tail of what the app itself is printing.
          </div>
        </div>
      </div>
      <ErrorBanner message={error} onDismiss={() => setError("")} />

      <section className="logs-section">
        <h3>Activity</h3>
        <p className="hint">Who did what to {name} -- created, deployed, snapshots, rollbacks, domains and power actions.</p>
        {events === null ? (
          <p className="hint">Loading...</p>
        ) : events.length === 0 ? (
          <p className="hint">No activity yet.</p>
        ) : (
          <div className="logs-events">
            {events.map((e, i) => (
              <div className={"logs-event" + (e.level === "error" ? " err" : "")} key={i}>
                <span className="logs-when">{new Date(e.time).toLocaleString()}</span>
                <span className="logs-detail">{e.detail}</span>
                {e.actor && <span className="logs-actor">{e.actor}</span>}
              </div>
            ))}
          </div>
        )}
      </section>

      <section className="logs-section">
        <div className="logs-head">
          <h3>App output</h3>
          <label className="logs-follow">
            <input type="checkbox" checked={follow} onChange={(ev) => setFollow(ev.target.checked)} /> Follow
          </label>
          <button type="button" className="btn btn-small" onClick={loadOutput}>Refresh</button>
        </div>
        <p className="hint">The last 300 lines the app printed (its container's stdout/stderr).</p>
        <pre className="logs-output" ref={outRef}>{output === null ? "Loading..." : output || "No output yet."}</pre>
      </section>
    </div>
  );
}
