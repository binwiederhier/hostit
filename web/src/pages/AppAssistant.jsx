import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";
import {
  reduceChatEvent,
  formatTokens,
  formatDuration,
  filesFromClipboard,
} from "../chat";

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

// Bespoke, minimal outline icons per tool -- one shared 16x16 stroke style, drawn
// in the accent colour (see .asst-tool-svg). Replaces the emoji.
const TOOL_ICON_PATHS = {
  list_files: (
    <path d="M2.2 4.4c0-.5.4-.9.9-.9h2.4l1.3 1.4h6.1c.5 0 .9.4.9.9v5.9c0 .5-.4.9-.9.9H3.1a.9.9 0 0 1-.9-.9z" />
  ),
  read_file: (
    <>
      <path d="M9 2.6H5.1a.8.8 0 0 0-.8.8v9.2c0 .5.3.8.8.8h5.8a.8.8 0 0 0 .8-.8V5.5z" />
      <path d="M9 2.6v3h3M6.3 8.6h3.4M6.3 10.6h3.4" />
    </>
  ),
  write_file: (
    <>
      <path d="M10.6 3.1l2.3 2.3" />
      <path d="M11.7 1.9l2.4 2.4-8 8-3.2.8.8-3.2z" />
    </>
  ),
  run_command: (
    <>
      <rect x="2" y="3" width="12" height="10" rx="1.4" />
      <path d="M4.6 6.6 6.4 8l-1.8 1.4M8.4 10h3" />
    </>
  ),
  read_logs: <path d="M3.2 4.7h9.6M3.2 8h9.6M3.2 11.3h5.5" />,
  deploy: (
    <>
      <path d="M3 12.6h10" />
      <path d="M8 10V3.2M5.3 5.9 8 3.2l2.7 2.7" />
    </>
  ),
  refresh_preview: (
    <>
      <path d="M12.8 8a4.8 4.8 0 1 1-1.4-3.4" />
      <path d="M12.9 3.4V6H10.3" />
    </>
  ),
  snapshot: (
    <>
      <circle cx="8" cy="8" r="5.3" />
      <path d="M8 5.2V8l2 1.3" />
    </>
  ),
  list_snapshots: (
    <path d="M6 4.6h6.5M6 8h6.5M6 11.4h6.5M3.4 4.6h.01M3.4 8h.01M3.4 11.4h.01" />
  ),
  rollback: (
    <>
      <path d="M3.4 8a4.6 4.6 0 1 0 1.4-3.3" />
      <path d="M3.3 3.4V6H5.9" />
    </>
  ),
  _default: <path d="M8 2.6l4.7 2.7v5.4L8 13.4 3.3 10.7V5.3z" />,
};

const ToolIcon = ({ tool }) => (
  <svg
    className="asst-tool-svg"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.4"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
  >
    {TOOL_ICON_PATHS[tool] || TOOL_ICON_PATHS._default}
  </svg>
);

// A run of tool calls gets a "layers" mark.
const GroupIcon = () => (
  <svg
    className="asst-tool-svg"
    viewBox="0 0 16 16"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.4"
    strokeLinecap="round"
    strokeLinejoin="round"
    aria-hidden="true"
  >
    <path d="M8 2.4l5.6 3-5.6 3-5.6-3z" />
    <path d="M2.6 8.6l5.4 2.9 5.4-2.9" />
  </svg>
);

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
    case "refresh_preview":
      return "Refreshed the preview";
    case "snapshot":
      return a.label
        ? `Saved snapshot "${truncate(a.label, 40)}"`
        : "Saved a snapshot";
    case "list_snapshots":
      return "Listed snapshots";
    case "rollback":
      return a.id ? `Rolled back to ${truncate(a.id, 40)}` : "Rolled back";
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
  const html = useMemo(
    () => DOMPurify.sanitize(marked.parse(text || "")),
    [text],
  );
  return <div className="asst-md" dangerouslySetInnerHTML={{ __html: html }} />;
};

// A tool call, collapsed to a one-line summary by default; click to see the exact
// input and output. Spins only while the turn is running AND this call has no
// result yet -- so a dropped/late result can never leave it spinning after the turn
// is done. Red edge when it errored.
const ToolCall = ({ item, busy }) => {
  const [open, setOpen] = useState(false);
  const running = item.output == null && busy;
  const cls = [
    "asst-tool",
    item.isError ? "asst-tool-error" : "",
    running ? "asst-tool-running" : "",
  ]
    .join(" ")
    .trim();
  return (
    <div className={cls}>
      <button
        type="button"
        className="asst-tool-head"
        onClick={() => setOpen((o) => !o)}
      >
        <span className="asst-tool-icon" aria-hidden="true">
          <ToolIcon tool={item.tool} />
        </span>
        <span className="asst-tool-summary">
          {summarize(item.tool, item.input)}
        </span>
        {running && <span className="asst-tool-spinner" aria-hidden="true" />}
        {!running && item.isError && (
          <span className="asst-tool-badge">error</span>
        )}
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

// Consecutive tool calls collapse into one group -- expanded while they run so
// progress is visible, collapsed once done to keep the history tidy. Each call
// inside stays individually expandable.
const ToolGroup = ({ tools, active, busy }) => {
  // Hooks must run unconditionally and in the same order every render. This fiber
  // is reused as a run grows from one call to many (renderTranscript keeps a stable
  // key), so useState has to come BEFORE the length-1 early return -- otherwise the
  // hook count jumps 0 -> 1 when the second call arrives and React crashes the tree.
  const [override, setOverride] = useState(null); // null = follow the running state
  // A lone tool call renders on its own, no group chrome -- but it still comes
  // through ToolGroup so that as more calls stream into the run the element type
  // at this position never switches (ToolCall <-> ToolGroup), which would remount
  // and make the summary chip flicker.
  if (tools.length === 1) {
    return <ToolCall item={tools[0]} busy={busy} />;
  }
  const running = tools.some((t) => t.output == null);
  const anyError = tools.some((t) => t.isError);
  // `active` keeps the still-growing group (the last one while the turn runs) open
  // and labelled with the current action, so it does not flap collapsed->expanded in
  // the brief gap between one tool finishing and the next starting. Gated on busy so
  // the group never keeps spinning once the turn is done.
  const busyGroup = active || (busy && running);
  const open = override ?? busyGroup;
  const current =
    tools.find((t) => t.output == null) || tools[tools.length - 1];
  return (
    <div className="asst-group">
      <button
        type="button"
        className="asst-group-head"
        onClick={() => setOverride(!open)}
      >
        <span className="asst-tool-icon" aria-hidden="true">
          <GroupIcon />
        </span>
        <span className="asst-tool-summary">
          {busyGroup
            ? summarize(current.tool, current.input)
            : `${tools.length} actions`}
        </span>
        {busyGroup && <span className="asst-tool-spinner" aria-hidden="true" />}
        {!busyGroup && anyError && (
          <span className="asst-tool-badge">error</span>
        )}
        <span className="asst-tool-chev" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
      </button>
      {open && (
        <div className="asst-group-body">
          {tools.map((t) => (
            <ToolCall key={t.id} item={t} busy={busy} />
          ))}
        </div>
      )}
    </div>
  );
};

// renderTranscript walks the items, folding runs of tool calls into groups. busy
// is the turn-running flag: the trailing group is still growing while it is true,
// so it is marked active and stays open instead of flickering between tools.
const renderTranscript = (items, busy, modes) => {
  const out = [];
  let i = 0;
  while (i < items.length) {
    if (items[i].kind === "tool") {
      const group = [];
      while (i < items.length && items[i].kind === "tool") {
        group.push(items[i]);
        i++;
      }
      // Always a ToolGroup (it renders a bare ToolCall for a run of one), so a run
      // growing from one call to many keeps the same element type and key here and
      // updates in place instead of remounting. The trailing run while busy is the
      // active one.
      out.push(
        <ToolGroup
          key={group[0].id}
          tools={group}
          active={busy && i === items.length}
          busy={busy}
        />,
      );
    } else {
      out.push(<Turn key={items[i].id} item={items[i]} modes={modes} />);
      i++;
    }
  }
  return out;
};

// modelLabel turns a mode id into its human name, so a reply reads "Claude
// Sonnet 5", not "claude-sonnet-5". The backend is part of the name here even
// though the dropdown leaves it to the mark: a caption has no group above it to
// borrow context from, and both backends offer a "Sonnet 5".
const modelLabel = (id, modes) => {
  if (!id) return "";
  // Every subscription reply from before the picker rework is tagged with the
  // one mode id that existed then. Show it the name it was shown under at the
  // time rather than a dead slug -- these are real replies in real histories.
  if (id === "external-claude") return "Claude.ai";
  const list = modes || [];
  // Replies recorded before options existed carry the provider's model string
  // ("claude-sonnet-5"). Those were always metered-API turns -- a subscription
  // turn was tagged "external-claude" -- so the API option is the right name for
  // them, and the subscription option is only a last resort.
  const m =
    list.find((o) => o.id === id) ||
    list.find((o) => o.model === id && o.backend !== "claude") ||
    list.find((o) => o.model === id);
  if (!m) return id;
  const vendor = m.backend ? m.backend[0].toUpperCase() + m.backend.slice(1) : "";
  return vendor ? `${vendor} ${m.label}` : m.label;
};

// formatTime renders a reply's timestamp like "11:43 pm".
const formatTime = (t) =>
  t
    ? new Date(t * 1000)
        .toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
        .toLowerCase()
    : "";

// responseMeta is the hover caption for a reply, e.g. "11:43 pm, Claude Opus 5".
const responseMeta = (item, modes) => {
  const parts = [];
  if (item.time) parts.push(formatTime(item.time));
  const ml = modelLabel(item.model, modes);
  if (ml) parts.push(ml);
  return parts.join(", ");
};

// AssistantText renders one assistant reply; on hover it reveals a small caption
// after the last line with the reply's time and model ("11:43 pm, Claude Opus 5").
const AssistantText = ({ item, modes }) => {
  const meta = responseMeta(item, modes);
  return (
    <div className="asst-turn asst-text">
      <span className="asst-text-body">
        <Markdown text={item.text} />
        {meta && (
          <>
            {/* A real space (not a margin) so it collapses when the caption wraps to
                its own line -- CSS can't detect line-start, but a space handles it. */}{" "}
            <span className="asst-response-meta">{meta}</span>
          </>
        )}
      </span>
    </div>
  );
};

// BackendMark is the vendor's own mark, drawn in the current text colour: the
// Claude burst for the operator's subscription, the Anthropic A for the metered
// API. Two rows can read "Sonnet 5" and differ only by this glyph, so it has to
// be the one a person already recognises rather than a shape we invented.
const BackendMark = ({ backend }) =>
  backend === "claude" ? (
    <svg className="asst-modeldd-mark" viewBox="0 0 24 24" aria-hidden="true">
      <path
        d="m4.714 15.956 4.718-2.648.079-.23-.08-.128h-.23l-.79-.048-2.695-.073-2.337-.097-2.265-.122-.57-.121-.535-.704.055-.353.48-.321.685.06 1.518.104 2.277.157 1.651.098 2.447.255h.389l.054-.158-.133-.097-.103-.098-2.356-1.596-2.55-1.688-1.336-.972-.722-.491L2 6.223l-.158-1.008.656-.722.88.06.224.061.893.686 1.906 1.476 2.49 1.833.364.304.146-.104.018-.072-.164-.274-1.354-2.446-1.445-2.49-.644-1.032-.17-.619a3 3 0 0 1-.103-.729L6.287.133 6.7 0l.995.134.42.364.619 1.415L9.735 4.14l1.555 3.03.455.898.243.832.09.255h.159V9.01l.127-1.706.237-2.095.23-2.695.08-.76.376-.91.747-.492.583.28.48.685-.067.444-.286 1.851-.558 2.903-.365 1.942h.213l.243-.242.983-1.306 1.652-2.064.728-.82.85-.904.547-.431h1.032l.759 1.129-.34 1.166-1.063 1.347-.88 1.142-1.263 1.7-.79 1.36.074.11.188-.02 2.853-.606 1.542-.28 1.84-.315.832.388.09.395-.327.807-1.967.486-2.307.462-3.436.813-.043.03.049.061 1.548.146.662.036h1.62l3.018.225.79.522.473.638-.08.485-1.213.62-1.64-.389-3.825-.91-1.31-.329h-.183v.11l1.093 1.068 2.003 1.81 2.508 2.33.127.578-.321.455-.34-.049-2.204-1.657-.85-.747-1.925-1.62h-.127v.17l.443.649 2.343 3.521.122 1.08-.17.353-.607.213-.668-.122-1.372-1.924-1.415-2.168-1.141-1.943-.14.08-.674 7.254-.316.37-.728.28-.607-.461-.322-.747.322-1.476.388-1.924.316-1.53.285-1.9.17-.632-.012-.042-.14.018-1.432 1.967-2.18 2.945-1.724 1.845-.413.164-.716-.37.066-.662.401-.589 2.386-3.036 1.439-1.882.929-1.086-.006-.158h-.055L4.138 18.56l-1.13.146-.485-.456.06-.746.231-.243 1.907-1.312Z"
        fill="currentColor"
      />
    </svg>
  ) : (
    <svg className="asst-modeldd-mark" viewBox="0 0 24 24" aria-hidden="true">
      <path
        d="M17.304 3.541h-3.672l6.696 16.918H24Zm-10.608 0L0 20.459h3.744l1.37-3.553h7.005l1.369 3.553h3.744L10.536 3.541Zm-.371 10.223L8.616 7.82l2.291 5.945Z"
        fill="currentColor"
      />
    </svg>
  );

// ModelDropdown is a subtle model picker inside the input row (right-aligned, in
// the ChatGPT style): a borderless grey text pill showing the current model with a
// caret, opening a menu of models with a checkmark on the selected one.
const ModelDropdown = ({ modes, mode, onChange, disabled }) => {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    if (!open) return undefined;
    const onDoc = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [open]);
  const current = modes.find((m) => m.id === mode);
  return (
    <div className="asst-modeldd" ref={ref}>
      <button
        type="button"
        className="asst-modeldd-btn"
        onClick={() => setOpen((o) => !o)}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={open}
        title="Choose model"
      >
        {current && <BackendMark backend={current.backend} />}
        <span className="asst-modeldd-label">
          {current ? current.label : "Model"}
        </span>
        {/* On a phone the label collapses to a thin vertical kebab (see CSS). */}
        <svg
          className="asst-modeldd-kebab"
          viewBox="0 0 16 16"
          fill="currentColor"
          aria-hidden="true"
        >
          <circle cx="8" cy="3.1" r="1.35" />
          <circle cx="8" cy="8" r="1.35" />
          <circle cx="8" cy="12.9" r="1.35" />
        </svg>
      </button>
      {open && (
        <div className="asst-modeldd-menu" role="menu">
          {modes.map((m, i) => (
            <button
              type="button"
              key={m.id}
              role="menuitem"
              // A rule between the groups: the same model can appear in both,
              // and what separates them is who pays.
              className={
                "asst-modeldd-item" +
                (m.id === mode ? " active" : "") +
                (i > 0 && modes[i - 1].backend !== m.backend
                  ? " asst-modeldd-divider"
                  : "")
              }
              onClick={() => {
                onChange(m.id);
                setOpen(false);
              }}
            >
              <BackendMark backend={m.backend} />
              <span className="asst-modeldd-item-label">{m.label}</span>
              {m.id === mode && (
                <svg
                  className="asst-modeldd-check"
                  viewBox="0 0 16 16"
                  aria-hidden="true"
                >
                  <path
                    d="M3 8.5l3.5 3.5L13 4.5"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.9"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
};

const Turn = ({ item, modes }) => {
  switch (item.kind) {
    case "user":
      return <div className="asst-turn asst-user">{item.text}</div>;
    case "thinking":
      return <div className="asst-turn asst-thinking">{item.text}</div>;
    case "text":
      return <AssistantText item={item} modes={modes} />;
    case "tool":
      return <ToolCall item={item} />;
    case "notice":
      return <div className="asst-turn asst-notice">{item.text}</div>;
    case "paused":
      return (
        <div className="asst-turn asst-paused">
          <svg
            className="asst-paused-icon"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <circle cx="12" cy="12" r="9" />
            <path d="M12 11v5" />
            <path d="M12 8h.01" />
          </svg>
          <span>{item.text}</span>
        </div>
      );
    case "error":
      return <div className="asst-turn asst-error">{item.text}</div>;
    default:
      return null;
  }
};

// A rotating "Working... / Baking..." status shown while the assistant runs. Once a
// turn has been going for more than 10s it also shows how long it has taken and the
// tokens produced so far, so a slow turn visibly keeps making progress.
const WorkingIndicator = ({ tokens }) => {
  const [i, setI] = useState(0);
  const [elapsed, setElapsed] = useState(0); // seconds since this turn started
  const startRef = useRef(Date.now());
  useEffect(() => {
    const words = setInterval(
      () => setI((n) => (n + 1) % WORKING_WORDS.length),
      2500,
    );
    const clock = setInterval(
      () => setElapsed(Math.floor((Date.now() - startRef.current) / 1000)),
      1000,
    );
    return () => {
      clearInterval(words);
      clearInterval(clock);
    };
  }, []);
  // · is a middle dot, ↓ a down arrow (output tokens); escaped to keep
  // the source ASCII.
  const meta =
    elapsed >= 10
      ? `(${formatDuration(elapsed)}${tokens > 0 ? ` \u00b7 \u2193 ${formatTokens(tokens)} tokens` : ""})`
      : null;
  return (
    <div className="asst-working">
      <span className="asst-working-spinner" aria-hidden="true" />
      <span>{WORKING_WORDS[i]}</span>
      <span className="ellipsis" aria-hidden="true">
        <span>.</span>
        <span>.</span>
        <span>.</span>
      </span>
      {meta && <span className="asst-working-meta">{meta}</span>}
    </div>
  );
};

const AppAssistant = ({
  name,
  onClose,
  embedded = false,
  onPreviewRefresh,
}) => {
  const [items, setItems] = useState([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [turnTokens, setTurnTokens] = useState(0); // output tokens for the running/last turn
  const [loaded, setLoaded] = useState(false);
  const [attachments, setAttachments] = useState([]); // uploaded files pending on the next message
  const [dragOver, setDragOver] = useState(false);
  const [modes, setModes] = useState([]); // available agent modes (External Claude + models)
  const [mode, setMode] = useState(""); // the selected mode/model
  const currentModelRef = useRef(""); // model actually answering the running turn
  const scrollRef = useRef(null);
  const taRef = useRef(null);
  const fileRef = useRef(null);
  const itemsRef = useRef(items); // mirrors items, so same-tick events chain correctly
  const turnMutatedRef = useRef(false); // did this turn change the app (drives the end-of-turn preview refresh)
  useEffect(() => {
    itemsRef.current = items;
  }, [items]);

  // Grow the input with its content, up to a few lines, then scroll. Only show a
  // scrollbar once it actually overflows the cap, not at rest.
  useEffect(() => {
    const ta = taRef.current;
    if (ta) {
      ta.style.height = "auto";
      ta.style.height = `${Math.min(ta.scrollHeight, 140)}px`;
      ta.style.overflowY = ta.scrollHeight > 140 ? "auto" : "hidden";
    }
  }, [input]);

  // Load the persisted conversation and whether a turn is running -- what a reload
  // or another device continues from.
  const loadTranscript = useCallback(async () => {
    try {
      const r = await fetch(`/api/apps/${encodeURIComponent(name)}/assistant`, {
        credentials: "same-origin",
      });
      const data = r.ok
        ? await r.json()
        : { items: [], running: false, modes: [], mode: "" };
      // Stable, position-based ids: the transcript is append-only and never
      // reorders, so keying by index means the done-reconcile reuses the existing
      // DOM instead of remounting the whole list (which flickered).
      setItems((data.items || []).map((it, idx) => ({ ...it, id: idx })));
      setBusy(!!data.running);
      setModes(data.modes || []);
      // Adopt the server's remembered mode only when we have none yet, so a
      // reconcile from another device's turn never clobbers an unsent choice here.
      setMode((cur) => cur || data.mode || "");
    } finally {
      setLoaded(true);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name]);

  useEffect(() => {
    loadTranscript();
  }, [loadTranscript]);

  useEffect(() => {
    scrollRef.current?.scrollTo(0, scrollRef.current.scrollHeight);
  }, [items, busy]);

  // Apply one streamed event. The run is server-owned and broadcast, so these
  // arrive for anyone's turn on this app, not just ours. itemsRef mirrors items so
  // several events in one tick chain correctly and the reducer sees fresh state.
  const handleEvent = useCallback(
    (ev) => {
      if (ev.type === "done") {
        setBusy(false);
        const mutated = turnMutatedRef.current;
        turnMutatedRef.current = false;
        // Reconcile to the committed transcript, then -- only if this turn actually
        // changed the app -- reload the preview ONCE and show it as an explicit
        // "Refreshing preview" action. We deliberately do not reload mid-turn: a
        // burst of file writes would otherwise reload the preview out from under
        // someone testing the app. The action is a client-only UI affordance, not
        // part of the model's transcript, so it is added after the reconcile.
        loadTranscript().then(() => {
          if (!mutated) {
            return;
          }
          setItems((cur) => {
            const next = [
              ...cur,
              {
                id: cur.length,
                kind: "tool",
                tool: "refresh_preview",
                input: "{}",
                output: "",
              },
            ];
            itemsRef.current = next;
            return next;
          });
          if (onPreviewRefresh) {
            onPreviewRefresh();
          }
        });
        return;
      }
      if (ev.type === "error") {
        setBusy(false);
        const next = [
          ...itemsRef.current,
          { id: itemsRef.current.length, kind: "error", text: ev.error },
        ];
        itemsRef.current = next;
        setItems(next);
        return;
      }
      if (ev.type === "paused") {
        // The turn hit its step limit: nothing failed, so show a calm "say continue"
        // notice rather than an error.
        setBusy(false);
        const next = [
          ...itemsRef.current,
          { id: itemsRef.current.length, kind: "paused", text: ev.text },
        ];
        itemsRef.current = next;
        setItems(next);
        return;
      }
      if (ev.type === "model") {
        // Which model is answering this turn; tag the replies that follow with it.
        currentModelRef.current = ev.text || "";
        setTurnTokens(0); // a new turn is starting; reset the counter
        turnMutatedRef.current = false; // ...and its "did anything change" flag
        setBusy(true);
        return;
      }
      if (ev.type === "usage") {
        // Running token total for the turn; drives the live counter.
        setTurnTokens(ev.usage?.output_tokens || 0);
        setBusy(true);
        return;
      }
      setBusy(true);
      const { items: next, refreshPreview } = reduceChatEvent(
        itemsRef.current,
        ev,
        currentModelRef.current,
      );
      itemsRef.current = next;
      setItems(next);
      // Note a successful mutating tool; the reload itself waits for end of turn.
      if (refreshPreview) {
        turnMutatedRef.current = true;
      }
    },
    [loadTranscript, onPreviewRefresh],
  );

  // Live event stream (SSE). Every watcher subscribes, so a run started on any
  // device shows up here; EventSource reconnects on its own if the link drops.
  useEffect(() => {
    const es = new EventSource(
      `/api/apps/${encodeURIComponent(name)}/assistant/stream`,
    );
    es.onmessage = (e) => {
      try {
        handleEvent(JSON.parse(e.data));
      } catch {
        // ignore a malformed frame
      }
    };
    return () => es.close();
  }, [name, handleEvent]);

  // Upload dropped/picked files into the app's uploads/ folder. Each file shows a
  // placeholder chip with a spinner immediately, replaced by its real in-app path
  // (from the server) once uploaded; on failure the placeholder is dropped.
  const uploadFiles = useCallback(
    async (fileList) => {
      const files = Array.from(fileList || []);
      if (files.length === 0) return;
      const stamp = Date.now();
      const temps = files.map((f, i) => ({
        tempId: `${stamp}-${i}`,
        name: f.name,
        is_image: (f.type || "").startsWith("image/"),
        uploading: true,
      }));
      setAttachments((prev) => [...prev, ...temps]);
      const dropTemps = () =>
        setAttachments((prev) =>
          prev.filter((a) => !temps.some((t) => t.tempId === a.tempId)),
        );
      try {
        const form = new FormData();
        files.forEach((f) => form.append("file", f));
        const r = await fetch(
          `/api/apps/${encodeURIComponent(name)}/assistant/upload`,
          {
            method: "POST",
            credentials: "same-origin",
            body: form,
          },
        );
        if (!r.ok) {
          const body = await r.json().catch(() => null);
          handleEvent({
            type: "error",
            error: body?.error || `upload failed (${r.status})`,
          });
          dropTemps();
          return;
        }
        const added = await r.json();
        setAttachments((prev) => [
          ...prev.filter((a) => !temps.some((t) => t.tempId === a.tempId)),
          ...(added || []),
        ]);
      } catch (err) {
        handleEvent({ type: "error", error: err.message });
        dropTemps();
      }
    },
    [name, handleEvent],
  );

  const onDrop = (e) => {
    e.preventDefault();
    setDragOver(false);
    if (e.dataTransfer?.files?.length) uploadFiles(e.dataTransfer.files);
  };
  // Paste an image (a screenshot) or any file straight into the chat; a plain-text
  // paste carries no files, so the default paste is left untouched.
  const onPaste = (e) => {
    const files = filesFromClipboard(e.clipboardData);
    if (files.length === 0) return;
    e.preventDefault();
    uploadFiles(files);
  };
  const onDragOver = (e) => {
    if (e.dataTransfer?.types?.includes("Files")) {
      e.preventDefault();
      setDragOver(true);
    }
  };
  const onDragLeave = () => setDragOver(false);

  // Removing a not-yet-sent attachment also deletes the uploaded file, so an
  // abandoned attachment does not orphan in uploads/. (Sent attachments stay: they
  // are the app's files by design.)
  const removeAttachment = (i) => {
    const a = attachments[i];
    setAttachments((prev) => prev.filter((_, j) => j !== i));
    if (a?.path) {
      fetch(
        `/api/apps/${encodeURIComponent(name)}/assistant/upload?path=${encodeURIComponent(a.path)}`,
        {
          method: "DELETE",
          credentials: "same-origin",
        },
      ).catch(() => {});
    }
  };

  // Send a message: the server starts the turn in the background and everything
  // comes back on the stream. We do not render it optimistically -- the stream
  // echoes the message so every device shows it the same way.
  const send = async () => {
    const message = input.trim();
    const ready = attachments.filter((a) => a.path); // uploaded, not still-uploading
    if (attachments.some((a) => a.uploading) || busy) return; // wait for uploads to finish
    if (!message && ready.length === 0) return;
    setInput("");
    setAttachments([]);
    setTurnTokens(0); // reset the token counter for the new turn
    setBusy(true);
    try {
      const r = await fetch(`/api/apps/${encodeURIComponent(name)}/assistant`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({
          message,
          mode,
          attachments: ready.map((a) => ({
            path: a.path,
            media_type: a.media_type,
          })),
        }),
      });
      if (r.status === 409) {
        return; // a turn is already running; it will stream in
      }
      if (!r.ok) {
        // The server sends {"error": "..."} (e.g. rate limited); show that, not raw JSON.
        const body = await r.json().catch(() => null);
        handleEvent({
          type: "error",
          error: body?.error || `request failed (${r.status})`,
        });
      }
    } catch (err) {
      handleEvent({ type: "error", error: err.message });
    }
  };

  // Stop the running turn. The server cancels it and publishes a done on the
  // stream, which flips us out of the busy state -- so we do not touch state here.
  const stop = async () => {
    try {
      await fetch(`/api/apps/${encodeURIComponent(name)}/assistant/stop`, {
        method: "POST",
        credentials: "same-origin",
      });
    } catch {
      // If the request fails the run continues; the stream stays the source of truth.
    }
  };

  // Escape closes the modal, matching the terminal.
  useEffect(() => {
    if (!onClose) return undefined;
    const onKey = (e) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  const onKeyDown = (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  };

  const inner = (
    <>
      {dragOver && (
        <div className="asst-drop" aria-hidden="true">
          Drop files to attach
        </div>
      )}
      {!embedded && (
        <header className="asst-header">
          <span className="asst-title">
            <span className="asst-title-app">{name}</span> &middot; AI assistant
            (preview)
          </span>
          <button
            type="button"
            className="term-btn asst-close"
            onClick={onClose}
            title="Close"
            aria-label="Close"
          >
            <svg
              viewBox="0 0 16 16"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        </header>
      )}

      <div className="asst-transcript" ref={scrollRef}>
        {loaded && items.length === 0 && (
          <p className="asst-empty">
            Ask me to build or change <strong>{name}</strong> &mdash; in plain
            English. I can read and write its files and run commands in its
            container, then publish. Try: &ldquo;add a leaderboard&rdquo;.
          </p>
        )}
        {renderTranscript(items, busy, modes)}
        {busy && <WorkingIndicator tokens={turnTokens} />}
      </div>

      {attachments.length > 0 && (
        <div className="asst-attachments">
          {attachments.map((a, i) => (
            <span
              className={"asst-chip" + (a.is_image ? " asst-chip-img" : "")}
              key={a.tempId || a.path}
            >
              {a.uploading && (
                <span className="asst-chip-spin" aria-hidden="true" />
              )}
              <span className="asst-chip-name" title={a.path || a.name}>
                {a.name || a.path}
              </span>
              <button
                type="button"
                className="asst-chip-x"
                onClick={() => removeAttachment(i)}
                aria-label="Remove attachment"
              >
                &times;
              </button>
            </span>
          ))}
        </div>
      )}
      <div className="asst-input">
        <input
          ref={fileRef}
          type="file"
          multiple
          className="asst-file-input"
          onChange={(e) => {
            uploadFiles(e.target.files);
            e.target.value = "";
          }}
          tabIndex={-1}
          aria-hidden="true"
        />
        <button
          type="button"
          className="btn btn-icon asst-plus"
          onClick={() => fileRef.current?.click()}
          disabled={busy}
          title="Attach files"
          aria-label="Attach files"
        >
          <svg
            viewBox="0 0 16 16"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            aria-hidden="true"
          >
            <path d="M8 3.5v9M3.5 8h9" />
          </svg>
        </button>
        <textarea
          ref={taRef}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          placeholder="Build or change your app..."
          rows={1}
          disabled={busy}
        />
        {modes.length > 1 && (
          <ModelDropdown
            modes={modes}
            mode={mode}
            onChange={setMode}
            disabled={busy}
          />
        )}
        {busy ? (
          <button
            type="button"
            className="btn asst-send asst-stop"
            onClick={stop}
            title="Stop"
            aria-label="Stop"
          >
            <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
              <rect x="4" y="4" width="8" height="8" rx="1.5" />
            </svg>
          </button>
        ) : (
          <button
            type="button"
            className="btn btn-primary asst-send"
            onClick={() => send()}
            disabled={
              attachments.some((a) => a.uploading) ||
              (!input.trim() && !attachments.some((a) => a.path))
            }
            title="Send"
            aria-label="Send"
          >
            <svg
              viewBox="0 0 16 16"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.8"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden="true"
            >
              <path d="M3 8h10M9 4l4 4-4 4" />
            </svg>
          </button>
        )}
      </div>
    </>
  );

  // Embedded (Direction C): the chat is a panel on the app page, not a modal.
  if (embedded) {
    return (
      <div
        className="asst-window asst-embedded"
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
      >
        {inner}
      </div>
    );
  }
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true">
      <div
        className="asst-window"
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
      >
        {inner}
      </div>
    </div>
  );
};

export default AppAssistant;
