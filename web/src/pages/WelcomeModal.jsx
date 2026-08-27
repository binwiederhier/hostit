import { useState } from "react";
import { api } from "../api";
import { TechLevelCards } from "../components";
import { presetTabs, presetPrompt, tabsToCsv } from "../tabs";

// The first-run welcome: what hostit is, and one question -- how technical are
// you -- whose answer pre-fills the assistant prompt and the default tabs
// (tabs.js presets). Shown once, gated on account.onboarded, and changeable
// later from the profile. "Skip" just marks onboarded so it never nags.
const WelcomeModal = ({ account, refreshAccount }) => {
  const [level, setLevel] = useState("");
  const [busy, setBusy] = useState(false);
  const assistantEnabled = !!account.assistant_enabled;

  const finish = async (chosen) => {
    setBusy(true);
    const patch = { onboarded: true };
    if (chosen) {
      patch.tech_level = chosen;
      patch.default_tabs = tabsToCsv(presetTabs(chosen, assistantEnabled));
      if (assistantEnabled) {
        patch.assistant_prompt = presetPrompt(chosen);
      }
    }
    try {
      await api.patch("/api/account", patch);
      await refreshAccount(); // clears account.onboarded=false -> this modal unmounts
    } catch {
      setBusy(false); // leave it open so they can retry
    }
  };

  return (
    <div className="modal-backdrop welcome-backdrop" role="dialog" aria-modal="true">
      <div className="card modal welcome-modal">
        <div className="welcome-hero">
          <div className="welcome-mark" aria-hidden="true">{"\u{1F680}"}</div>
          <h1>Welcome to hostit</h1>
          <p>
            hostit runs small web apps &mdash; each gets its own container, subdomain and HTTPS
            certificate. Describe what you want and the built-in assistant builds it, or bring your
            own code and deploy it. Your apps are private until you choose to share them.
          </p>
        </div>
        <p className="welcome-q">To tailor things to you, how technical are you?</p>
        <TechLevelCards value={level} onChange={setLevel} disabled={busy} />
        <div className="btn-row welcome-actions">
          <button type="button" className="btn" onClick={() => finish("")} disabled={busy}>
            Skip for now
          </button>
          <button type="button" className="btn btn-primary" onClick={() => finish(level)} disabled={busy || !level}>
            {busy ? "Setting up..." : "Get started"}
          </button>
        </div>
      </div>
    </div>
  );
};

export default WelcomeModal;
