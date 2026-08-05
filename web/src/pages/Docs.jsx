import { Snippet, Wordmark } from "../components";

// The user documentation, served by hostit itself so it is always the docs for
// THIS instance: every example uses the reader's own hostname.
const origin = window.location.origin;
const host = window.location.host;

// Section headings double as the table of contents, so they are declared once
const sections = [
  { id: "getting-started", title: "Getting started" },
  { id: "claude", title: "Using it with Claude Code" },
  { id: "layout", title: "Where things go" },
  { id: "config", title: "hostit.yml" },
  { id: "ssh", title: "SSH, scp and rsync" },
  { id: "api", title: "API reference" },
];

const Endpoint = ({ method, path, what }) => (
  <tr>
    <td className="mono docs-method">{method}</td>
    <td className="mono">{path}</td>
    <td>{what}</td>
  </tr>
);

const Docs = () => (
  <div className="docs">
    <div className="docs-header">
      <Wordmark big />
      <p className="empty-pitch">
        Small web apps, each in its own container, with a subdomain, HTTPS, SSH access and an API an AI assistant can drive.
        This is the documentation for <span className="mono">{host}</span>.
      </p>
    </div>

    <nav className="docs-toc" aria-label="Contents">
      {sections.map((s) => (
        <a key={s.id} href={`#${s.id}`}>
          {s.title}
        </a>
      ))}
    </nav>

    <section id="getting-started">
      <h2>Getting started</h2>
      <p>
        Sign in with Google. If your account is new and your email domain is not pre-approved, an administrator has to approve
        you once; the dashboard will say so.
      </p>
      <p>
        Then click <strong>New app</strong> and give it a name (lowercase letters, digits and dashes). Within a few seconds the
        app exists and already serves a placeholder page at{" "}
        <span className="mono">https://&lt;name&gt;.{host.replace(/^[^.]*\./, "")}</span>. Nothing is broken while you build:
        there is always something at that URL.
      </p>
      <p>
        Every app gets its own container. You are root inside it, other apps are invisible, and an app cannot reach another
        app's files, processes or ports.
      </p>
    </section>

    <section id="claude">
      <h2>Using it with Claude Code</h2>
      <p>
        This is what hostit is for. Open your app's page and copy the prompt at the top. Paste it into Claude Code (or any
        assistant that can make HTTP requests) and it has everything it needs: the app's URL, a token scoped to that one app,
        and the address of an endpoint that explains the rest of the API to it.
      </p>
      <p>
        The prompt changes with the app. While the app is still a placeholder it asks the assistant to build something; once
        the app describes itself in <span className="mono">hostit.yml</span>, the prompt says the app already exists and asks
        the assistant to pick up where the last session left off.
      </p>
      <p>
        The token in that prompt can only touch that one app. It cannot read your account, your other apps, or anything
        administrative, which is what makes it safe to paste into a chat.
      </p>
      <p className="hint">
        Assistants sometimes start building from the app name alone. The prompt tells them not to, and to ask you first.
      </p>
    </section>

    <section id="layout">
      <h2>Where things go</h2>
      <p>Every app's home directory has a place for each kind of thing:</p>
      <table className="docs-table">
        <tbody>
          <tr>
            <td className="mono">public/</td>
            <td>Files served on the web. Static apps serve exactly this directory.</td>
          </tr>
          <tr>
            <td className="mono">bin/</td>
            <td>
              Binaries and scripts the app runs, e.g. <span className="mono">run: ./bin/myapp</span>.
            </td>
          </tr>
          <tr>
            <td className="mono">log/</td>
            <td>
              The app's output, written by hostit. <span className="mono">hostit logs</span> reads it.
            </td>
          </tr>
          <tr>
            <td className="mono">src/</td>
            <td>Source, if you keep the app's source on the host.</td>
          </tr>
          <tr>
            <td className="mono">docs/</td>
            <td>
              The app's own documentation: how it works and why. Read it before changing the app, update it after. An
              assistant is told to do both.
            </td>
          </tr>
          <tr>
            <td className="mono">hostit.yml</td>
            <td>How the app runs.</td>
          </tr>
          <tr>
            <td className="mono">README.md</td>
            <td>What the app is, and its worklog. Assistants read this first and write back to it.</td>
          </tr>
        </tbody>
      </table>
      <p>Directories appear as you write into them.</p>
      <p className="hint">
        If your app serves files itself (rather than using <span className="mono">mode: static</span>), point it at{" "}
        <span className="mono">public/</span> and never at the home directory. The home also holds{" "}
        <span className="mono">hostit.yml</span> and <span className="mono">.ssh/</span>, and serving it puts them on the open
        internet.
      </p>
      <h3>Keep the source here, and let hostit build it</h3>
      <p>
        Put the source in <span className="mono">src/</span> and give <span className="mono">hostit.yml</span> a build step. It
        runs before the app starts, on every deploy; if it fails the app is left alone and the error is in the logs.
      </p>
      <Snippet
        text={`prepare: cd src && go build -o ../bin/myapp .
run: ./bin/myapp`}
      />
      <p>
        This is the better default, and not only for convenience. It builds on the machine that runs it, so there is no
        cross-compiling and you need no toolchain of your own -- which matters if you are driving all of this from a chat
        window. And the app stays editable: the next session, yours or an assistant's, can read the source and change it.
      </p>
      <p>
        Uploading a prebuilt binary to <span className="mono">bin/</span> also works and is faster (
        <span className="mono">CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build</span>), but then the app is just a binary, and
        whoever comes next has nothing to work with.
      </p>
    </section>

    <section id="config">
      <h2>hostit.yml</h2>
      <p>
        One file decides how the app runs. Pick a mode, then apply it with <span className="mono">hostit up</span>{" "}
        over SSH or <span className="mono">POST /api/apps/&lt;app&gt;/deploy</span>. Keys hostit does not know are an error, so a
        typo is reported rather than ignored.
      </p>
      <h3>mode: static</h3>
      <Snippet
        text={`description: What this app is, in one line
mode: static`}
      />
      <p>
        hostit serves <span className="mono">public/</span> itself. Nothing to run, nothing to install, and nothing else to
        configure -- the directory is always <span className="mono">public/</span>. This is the right mode for plain HTML and
        for the built output of any frontend.
      </p>
      <h3>mode: app</h3>
      <Snippet
        text={`description: What this app is, in one line
mode: app
prepare: cd src && go build -o ../bin/myapp .
run: ./bin/myapp
env:
  DEBUG: "true"`}
      />
      <p>
        <span className="mono">prepare:</span> is optional and runs before the app starts, every deploy: compile, install
        dependencies, build a frontend into <span className="mono">public/</span>. A failing build stops the deploy rather than
        starting a broken app.
      </p>
      <p>
        While you are still getting the build to work, <span className="mono">POST /api/apps/&lt;app&gt;/run</span> runs a single
        command in the container and returns its output, so an assistant can iterate on a compile error without SSH. It is
        bounded (one minute by default, five at most); once the command works, move it into{" "}
        <span className="mono">prepare:</span>.
      </p>
      <p className="hint">
        A command you background (<span className="mono">&amp;</span>, with its output redirected) keeps running after the
        request returns. Nothing supervises it, so if it misbehaves, <span className="mono">restart</span> the app -- that
        replaces the container and takes every stray process with it. Each app has 512 processes and its memory limit to work
        with: a runaway is contained to its own container, though it will slow the box while it lasts.
      </p>
      <p>
        The command must listen on <span className="mono">0.0.0.0:$PORT</span>. <span className="mono">$PORT</span> is provided;
        nothing else is reachable from outside. hostit restarts the command if it exits.
      </p>
      <p className="hint">
        Keep <span className="mono">description:</span> current. The dashboard shows it, and it is what the next assistant
        session starts from.
      </p>
    </section>

    <section id="ssh">
      <h2>SSH, scp and rsync</h2>
      <p>
        Add an SSH key to your <a href="/profile">profile</a> first -- it applies to every app you own, present and future.
        Without a key, SSH cannot work and the app page says so.
      </p>
      <Snippet
        text={`ssh <app>@${host.replace(/^[^.]*\./, "")}
scp ./index.html <app>@${host.replace(/^[^.]*\./, "")}:public/
rsync -av ./site/ <app>@${host.replace(/^[^.]*\./, "")}:public/`}
      />
      <p>
        The session lands <em>inside</em> the app's container, where you are root and can{" "}
        <span className="mono">apt-get install</span> whatever you need. Installed packages last until the container is
        recreated, so anything permanent belongs in a prepare: step.
      </p>
      <p>Inside, these commands manage the app:</p>
      <Snippet
        text={`hostit up          # apply hostit.yml and (re)start
hostit down        # stop
hostit restart     # restart
hostit status      # is it running?
hostit logs -f     # watch the output`}
      />
    </section>

    <section id="api">
      <h2>API reference</h2>
      <p>
        Two kinds of credential. An <strong>app token</strong> (shown on the app's page) can only touch that app, through{" "}
        <span className="mono">/api/apps/&lt;app&gt;/</span>. An <strong>account token</strong> (Profile → API tokens) can do
        anything you can, including creating and deleting apps.
      </p>
      <Snippet text={`curl -H "Authorization: Bearer <token>" ${origin}/api/apps/<app>/info`} />
      <p>
        <span className="mono">/api/apps/&lt;app&gt;/info</span> returns the app's state and a full description of the API, so an
        assistant pointed at that one URL needs nothing else.
      </p>
      <h3>App API</h3>
      <div className="table-wrap">
        <table className="docs-table">
          <tbody>
            <Endpoint method="GET" path="/api/apps/{app}/info" what="State, README, file list, config, and the guide" />
            <Endpoint method="GET" path="/api/apps/{app}/logs?lines=N" what="Recent output" />
            <Endpoint method="GET" path="/api/apps/{app}/files?path=" what="List one directory (type file|dir) of the app's files" />
            <Endpoint method="GET" path="/api/apps/{app}/files/{path}" what="Read one file" />
            <Endpoint method="PUT" path="/api/apps/{app}/files/{path}?mode=755" what="Write one file; mode makes it executable" />
            <Endpoint method="DELETE" path="/api/apps/{app}/files/{path}" what="Delete one file" />
            <Endpoint method="POST" path="/api/apps/{app}/files" what="Upload a tar archive (Content-Type: application/x-tar)" />
            <Endpoint method="PUT" path="/api/apps/{app}/readme" what={`Replace README.md: {"readme": "..."}`} />
            <Endpoint
              method="POST"
              path="/api/apps/{app}/run"
              what={`Run one command in the container: {"command": "cd src && go build ./..."}`}
            />
            <Endpoint method="POST" path="/api/apps/{app}/deploy" what="Apply hostit.yml and (re)start" />
            <Endpoint method="POST" path="/api/apps/{app}/start|stop|restart" what="Lifecycle; POST only" />
          </tbody>
        </table>
      </div>
      <h3>Account API</h3>
      <div className="table-wrap">
        <table className="docs-table">
          <tbody>
            <Endpoint method="GET" path="/api/account" what="Who you are, your limits and usage" />
            <Endpoint method="GET|POST" path="/api/apps" what="List your apps, or create one" />
            <Endpoint method="GET|DELETE" path="/api/apps/{name}" what="One app, or delete it" />
            <Endpoint method="POST" path="/api/apps/{name}/token" what="Rotate the app's agent token" />
            <Endpoint method="GET|POST" path="/api/account/keys" what="Your SSH keys" />
            <Endpoint method="GET|POST" path="/api/account/tokens" what="Your account tokens" />
          </tbody>
        </table>
      </div>
    </section>

    <p className="docs-foot">
      hostit &middot; <a href="/">back to the dashboard</a>
    </p>
  </div>
);

export default Docs;
