import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { StatusDot } from "../components";

// A viewer-only account's home: the apps others have shared with them, and
// nothing about creating or managing apps of their own. The list comes from
// /api/apps, which for a viewer returns exactly their view grants.
const SharedApps = () => {
  const [apps, setApps] = useState(null); // null = loading
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    api
      .get("/api/apps")
      .then(setApps)
      .catch(() => setFailed(true));
  }, []);

  const shared = (apps || []).filter((a) => !a.archived);
  return (
    <div className="shared-apps">
      <h1>Shared with you</h1>
      <p className="hint">Apps others have given you access to open.</p>
      {failed && <p className="shared-apps-note">Couldn't load your apps.</p>}
      {apps && shared.length === 0 && !failed && (
        <p className="shared-apps-note">Nothing has been shared with you yet.</p>
      )}
      <div className="shared-apps-list">
        {shared.map((a) => (
          <Link key={a.name} to={`/app/${a.name}`} className="shared-app-row">
            <StatusDot running={a.running} appRunning={a.app_running} />
            <span className="shared-app-name">{a.name}</span>
          </Link>
        ))}
      </div>
    </div>
  );
};

export default SharedApps;
