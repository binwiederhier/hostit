import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { marked } from "marked";
import DOMPurify from "dompurify";

// The in-browser coding agent. It POSTs a message to the daemon's assistant
// endpoint and reads the loop back as Server-Sent Events -- the model's thinking,
// its text, and every tool it runs. The conversation is persisted server-side, so
// a reload (or another device) picks it up where it left off.

marked.setOptions({ breaks: true, gfm: true });

// Fun status words that rotate while the assistant works, in the spirit of a
// certain CLI. Purely cosmetic.
const WORKING_WORDS = [
  "Working",
  "Baking",
  "Brewing",
  "Cooking",
  "Conjuring",
  "Crafting",
  "Noodling",
  "Percolating",
  "Pondering",
  "Simmering",
  "Tinkering",
  "Whirring",
];

const TOOL_ICONS = {
  list_files: "\u{1F4C1}",
  read_file: "\u{1F4C4}",
  write_file: "\u{270D}\u{FE0F}",
  run_command: "\u{1F5A5}\u{FE0F}",
  read_logs: "\u{1F4DC}",
  deploy: "\u{1F680}",
};

const truncate = (s, n) => (s && s.length > n ? s.slice(0, n) + "…" : s || "");

// summarize turns a tool call into a short human line for the collapsed chip.
const summarize = (tool, input) => {
  let a = {};
  try {
    a = JSON.parse(input || "{}");
  } catch {
    // keep defaults
  }
  switch (tool) {
    case "list_files":
      return a.path ? `Listed ${a.path}/` : "Listed files";
    case "read_file":
      return `Read ${a.path}`;
    case "write_file":
      return `Wrote ${a.path}`;
    case "run_command":
      return `Ran ${truncate(a.command, 64)}`;
    case "read_logs":
      return "Read logs";
    case "deploy":
      return "Deployed the app";
    default:
      return tool;
  }
};

const prettyInput = (input) => {
  try {
    return JSON.stringify(JSON.parse(input), null, 2);
  } catch {
    return input || "";
  }
};

const Markdown = ({ text }) => {
  const html = useMemo(() => DOMPurify.sanitize(marked.parse(text || "")), [text]);
  return <div className="asst-md" dangerouslySetInnerHTML={{ __html: html }} />;
};

// A tool call, collapsed to a one-line summary by default; click to see the exact
// input and output. Shows a spinner while it runs and a red edge when it errored.
const ToolCall = ({ item }) => {
  const [open, setOpen] = useState(false);
  const running = item.output == null;
  const cls = ["asst-tool", item.isError ? "asst-tool-error" : "", running ? "asst-tool-running" : ""].join(" ").trim();
  return (
    <div className={cls}>
      <button type="button" className="asst-tool-head" onClick={() => setOpen((o) => !o)}>
        <span className="asst-tool-icon" aria-hidden="true">
          {TOOL_ICONS[item.tool] || "\u{1F527}"}
        </span>
        <span className="asst-tool-summary">{summarize(item.tool, item.input)}</span>
        {running && <span className="asst-tool-spinner" aria-hidden="true" />}
        {!running && item.isError && <span className="asst-tool-badge">error</span>}
        <span className="asst-tool-chev" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
      </button>
      {open && (
        <div className="asst-tool-body">
          <div className="asst-tool-label">input</div>
          <pre className="asst-tool-pre">{prettyInput(item.input)}</pre>
          {item.output != null && (
            <>
              <div className="asst-tool-label">output</div>
              <pre className="asst-tool-pre">{item.output}</pre>
            </>
          )}
        </div>
      )}
    </div>
  );
};

const Turn = ({ item }) => {
  switch (item.kind) {
    case "user":
      return <div className="asst-turn asst-user">{item.text}</div>;
    case "thinking":
      return <div className="asst-turn asst-thinking">{item.text}</div>;
    case "text":
      return (
        <div className="asst-turn asst-text">
          <Markdown text={item.text} />
        </div>
      );
    case "tool":
      return <ToolCall item={item} />;
    case "error":
      return <div className="asst-turn asst-error">{item.text}</div>;
    default:
      return null;
  }
};

// A rotating "Working... / Baking..." status shown while the assistant runs.
const WorkingIndicator = () => {
  const [i, setI] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setI((n) => (n + 1) % WORKING_WORDS.length), 2500);
    return () => clearInterval(t);
  }, []);
  return (
    <div className="asst-working">
      <span className="asst-working-spinner" aria-hidden="true" />
      <span>{WORKING_WORDS[i]}</span>
      <span className="ellipsis" aria-hidden="true">
        <span>.</span>
        <span>.</span>
        <span>.</span>
      </span>
    </div>
  );
};

// drainEvents pulls complete "data: {...}\n\n" frames out of a growing buffer.
const drainEvents = (buffer, onEvent) => {
  let rest = buffer;
  let idx;
  while ((idx = rest.indexOf("\n\n")) >= 0) {
    const frame = rest.slice(0, idx);
    rest = rest.slice(idx + 2);
    if (frame.startsWith("data: ")) {
      try {
        onEvent(JSON.parse(frame.slice(6)));
      } catch {
        // ignore a partial/garbled frame
      }
    }
  }
  return rest;
};

const AppAssistant = () => {
  const { name } = useParams();
  const [items, setItems] = useState([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const scrollRef = useRef(null);
  const idRef = useRef(0);
  const nextId = () => `i${idRef.current++}`;

  // Load the persisted conversation once, so a reload or another device continues
  // where this one left off.
  useEffect(() => {
    let cancelled = false;
    api()
      .then((data) => {
        if (cancelled) return;
        const withIds = (data.items || []).map((it) => ({ ...it, id: nextId() }));
        setItems(withIds);
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
    return () => {
      cancelled = true;
    };
    async function api() {
      const r = await fetch(`/api/apps/${encodeURIComponent(name)}/assistant`, { credentials: "same-origin" });
      return r.ok ? r.json() : { items: [] };
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name]);

  useEffect(() => {
    scrollRef.current?.scrollTo(0, scrollRef.current.scrollHeight);
  }, [items, busy]);

  const onEvent = useCallback((ev) => {
    setItems((prev) => {
      const next = [...prev];
      if (ev.type === "tool_result") {
        for (let i = next.length - 1; i >= 0; i--) {
          if (next[i].kind === "tool" && next[i].output == null) {
            next[i] = { ...next[i], output: ev.output ?? "", isError: ev.is_error };
            return next;
          }
        }
        return next;
      }
      if (ev.type === "thinking" && (ev.text || "").trim()) next.push({ id: nextId(), kind: "thinking", text: ev.text });
      else if (ev.type === "text") next.push({ id: nextId(), kind: "text", text: ev.text });
      else if (ev.type === "tool_use") next.push({ id: nextId(), kind: "tool", tool: ev.tool, input: ev.input, output: null });
      else if (ev.type === "error") next.push({ id: nextId(), kind: "error", text: ev.error });
      return next;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const send = async (reset = false) => {
    const message = input.trim();
    if (!message || busy) return;
    setInput("");
    setItems((prev) => [...prev, { id: nextId(), kind: "user", text: message }]);
    setBusy(true);
    try {
      const resp = await fetch(`/api/apps/${encodeURIComponent(name)}/assistant`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({ message, reset }),
      });
      if (!resp.ok || !resp.body) {
        const text = await resp.text().catch(() => "");
        onEvent({ type: "error", error: text || `request failed (${resp.status})` });
        return;
      }
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        buffer = drainEvents(buffer, onEvent);
      }
    } catch (err) {
      onEvent({ type: "error", error: err.message });
    } finally {
      setBusy(false);
    }
  };

  const clear = async () => {
    if (busy) return;
    try {
      await fetch(`/api/apps/${encodeURIComponent(name)}/assistant`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({ message: "(new conversation)", reset: true }),
      });
    } catch {
      // best effort
    }
    setItems([]);
  };

  const onKeyDown = (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  };

  return (
    <div className="asst-page">
      <header className="asst-header">
        <Link to={`/app/${name}`} className="asst-back">
          &larr; {name}
        </Link>
        <span className="asst-title">AI assistant (preview)</span>
        <button type="button" className="btn btn-small" onClick={clear} disabled={busy || items.length === 0}>
          New chat
        </button>
      </header>

      <div className="asst-transcript" ref={scrollRef}>
        {loaded && items.length === 0 && (
          <p className="asst-empty">
            Ask me to build or change <strong>{name}</strong>. I can read and write its files and run commands in its
            container. Try: &ldquo;make the homepage say hello in big letters&rdquo;.
          </p>
        )}
        {items.map((item) => (
          <Turn key={item.id} item={item} />
        ))}
        {busy && <WorkingIndicator />}
      </div>

      <div className="asst-input">
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="Tell the assistant what to build or change..."
          rows={2}
          disabled={busy}
        />
        <button type="button" className="btn btn-primary" onClick={() => send()} disabled={busy || !input.trim()}>
          Send
        </button>
      </div>
    </div>
  );
};

export default AppAssistant;
