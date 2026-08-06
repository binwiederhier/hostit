import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";

// The in-browser coding agent (PoC). It POSTs a message to the daemon's assistant
// endpoint and reads the loop back as Server-Sent Events -- the model's thinking,
// its text, and every tool it runs -- so a phone watching a build sees it unfold.

// parseSSE pulls complete "data: {...}\n\n" frames out of a growing buffer.
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

const ToolCall = ({ item }) => {
  const [open, setOpen] = useState(false);
  return (
    <div className={`asst-tool${item.isError ? " asst-tool-error" : ""}`}>
      <button type="button" className="asst-tool-head" onClick={() => setOpen((v) => !v)}>
        <span className="asst-tool-name">{item.tool}</span>
        <span className="asst-tool-arg">{item.input}</span>
        {item.output != null && <span className="asst-tool-toggle">{open ? "hide" : "output"}</span>}
      </button>
      {open && item.output != null && <pre className="asst-tool-output">{item.output}</pre>}
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
      return <div className="asst-turn asst-text">{item.text}</div>;
    case "tool":
      return <ToolCall item={item} />;
    case "error":
      return <div className="asst-turn asst-error">{item.text}</div>;
    default:
      return null;
  }
};

const AppAssistant = () => {
  const { name } = useParams();
  const [items, setItems] = useState([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const scrollRef = useRef(null);

  useEffect(() => {
    scrollRef.current?.scrollTo(0, scrollRef.current.scrollHeight);
  }, [items]);

  const onEvent = useCallback((ev) => {
    setItems((prev) => {
      const next = [...prev];
      if (ev.type === "tool_result") {
        // Attach the result to the most recent tool call that is still waiting
        for (let i = next.length - 1; i >= 0; i--) {
          if (next[i].kind === "tool" && next[i].output == null) {
            next[i] = { ...next[i], output: ev.output, isError: ev.is_error };
            return next;
          }
        }
        return next;
      }
      if (ev.type === "thinking") next.push({ kind: "thinking", text: ev.text });
      else if (ev.type === "text") next.push({ kind: "text", text: ev.text });
      else if (ev.type === "tool_use") next.push({ kind: "tool", tool: ev.tool, input: ev.input, output: null });
      else if (ev.type === "error") next.push({ kind: "error", text: ev.error });
      return next;
    });
  }, []);

  const send = async (reset = false) => {
    const message = input.trim();
    if (!message || busy) return;
    setInput("");
    setItems((prev) => [...prev, { kind: "user", text: message }]);
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
        <button type="button" className="btn btn-small" onClick={() => setItems([])} disabled={busy}>
          Clear
        </button>
      </header>

      <div className="asst-transcript" ref={scrollRef}>
        {items.length === 0 && (
          <p className="asst-empty">
            Ask me to build or change <strong>{name}</strong>. I can read and write its files and run commands in its
            container. Try: &ldquo;make the homepage say hello in big letters&rdquo;.
          </p>
        )}
        {items.map((item, i) => (
          <Turn key={i} item={item} />
        ))}
        {busy && <div className="asst-turn asst-thinking asst-working">working...</div>}
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
