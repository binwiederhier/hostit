import { useEffect, useState } from "react";
import { api } from "../api";
import { ErrorBanner, Skeleton } from "../components";

// The public app gallery: public apps their owners chose to list. Behind the
// login (a members' gallery, not the open internet); the nav only shows the link
// when the instance has the gallery on, but the page states it plainly too.
const Explore = () => {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    api
      .get("/api/explore")
      .then((d) => live && setData(d))
      .catch((e) => live && setError(e.message));
    return () => {
      live = false;
    };
  }, []);
  const apps = data && data.enabled ? data.apps : [];
  return (
    <>
      <div className="page-header">
        <h1>Explore</h1>
      </div>
      <p className="hint">Public apps that members of this instance have chosen to share.</p>
      <ErrorBanner message={error} onDismiss={() => setError("")} />
      {data === null && !error && <Skeleton card rows={3} label="Loading apps..." />}
      {data && !data.enabled && <p className="empty">The app gallery is turned off on this instance.</p>}
      {data && data.enabled && apps.length === 0 && <p className="empty">No apps have been listed yet.</p>}
      {apps.length > 0 && (
        <div className="explore-grid">
          {apps.map((a) => (
            <a key={a.name} className="explore-card" href={a.url} target="_blank" rel="noreferrer">
              {/* The stored preview, when the instance takes screenshots and one
                  exists. It is decorative: the card already names the app, and a
                  shot that fails to load simply hides itself. */}
              {a.has_shot && (
                <span className="explore-shot">
                  <img
                    src={`/api/explore/${encodeURIComponent(a.name)}/preview.png`}
                    alt=""
                    loading="lazy"
                    onError={(e) => {
                      e.currentTarget.parentElement.style.display = "none";
                    }}
                  />
                </span>
              )}
              <span className="explore-name">{a.name}</span>
              <span className="explore-desc">{a.description || "No description yet"}</span>
              <span className="explore-host mono">{a.url.replace(/^https?:\/\//, "")}</span>
            </a>
          ))}
        </div>
      )}
    </>
  );
};

export default Explore;
