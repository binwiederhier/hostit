import { useEffect, useMemo, useState } from "react";
import { Snippet, Wordmark } from "../components";
import { DOCS_GUIDES, docsHref, docsPages, findDocsPage } from "../docs";
import dashboardShot from "../assets/docs/dashboard.png";
import workspaceShot from "../assets/docs/workspace.png";
import editorShot from "../assets/docs/editor.png";
import terminalShot from "../assets/docs/terminal.png";
import snapshotsShot from "../assets/docs/snapshots.png";
import adminShot from "../assets/docs/admin.png";
import assistantShot from "../assets/docs/assistant.png";
import resourcesShot from "../assets/docs/resources.png";
import domainsShot from "../assets/docs/domains.png";
import clusterShot from "../assets/docs/cluster.png";
import profileShot from "../assets/docs/profile.png";

// The user documentation, served by hostit itself so it is always the docs for THIS
// instance: every example uses the reader's own hostname. The layout mirrors a docs
// site (docs.ntfy.sh): a sidebar with a User guide and an Administration guide, and
// one page shown at a time, selected by the URL hash so links are shareable.
const origin = window.location.origin;
const host = window.location.host;
// The web app is served at the base domain and nowhere else, so the hostname in
// the address bar IS the base domain. This used to strip the first label, which
// was right only while a "hostit.<base>" alias existed -- on the base domain
// itself it turned apps.example.com into example.com and told people to ssh to
// the wrong host.
const baseDomain = host;

const Endpoint = ({ method, path, what }) => (
  <tr>
    <td className="mono docs-method">{method}</td>
    <td className="mono">{path}</td>
    <td>{what}</td>
  </tr>
);

// A captioned screenshot of the running app.
const Figure = ({ src, alt, caption }) => (
  <figure className="docs-fig">
    <img className="docs-shot" src={src} alt={alt} loading="lazy" />
    {caption ? <figcaption>{caption}</figcaption> : null}
  </figure>
);

// ---- User guide pages ---------------------------------------------------------

const IntroPage = () => (
  <>
    <h2>Introduction</h2>
    <p>
      hostit gives every app its own container, a subdomain with HTTPS, SSH
      access, and an API an AI assistant can drive. This is the documentation
      for <span className="mono">{host}</span>.
    </p>
    <p>
      Sign in with Google. If your account is new and your email domain is not
      pre-approved, an administrator has to approve you once; the dashboard will
      say so.
    </p>
    <Figure
      src={dashboardShot}
      alt="The hostit dashboard listing apps"
      caption="The dashboard: your apps, their status and resource use, and New app."
    />
    <p>
      Two views, switched next to <strong>New app</strong>: cards, or a dense
      list that fits a lot more apps on the screen. Your choice is remembered.
      Archived apps are hidden until you turn them on with the toggle beside it,
      which appears only once you have some.
    </p>
    <p>
      Then click <strong>New app</strong> and give it a name (lowercase letters,
      digits and dashes). Within a few seconds the app exists and already serves
      a placeholder page at{" "}
      <span className="mono">https://&lt;name&gt;.{baseDomain}</span>. Nothing
      is broken while you build: there is always something at that URL.
    </p>
    <p>
      Every app gets its own container. You are root inside it, other apps are
      invisible, and an app cannot reach another app's files, processes or
      ports. Each container comes with the common runtimes already installed:
      Python 3 (venv and pip), the Go toolchain, Node.js with npm, PHP, and
      sqlite3, plus git, curl, rsync, htop, vim and nano. Anything else installs
      with <span className="mono">apt-get</span>.
    </p>
    <h3>The workspace</h3>
    <p>
      The app's page is a workspace: a built-in AI assistant that builds and
      deploys the app for you, a live preview beside it, and a browser terminal
      -- so you can do everything here, or over SSH and the API, or with your
      own agent.
    </p>
    <Figure
      src={workspaceShot}
      alt="The app workspace: assistant chat beside a live preview"
      caption="The app workspace: the assistant on the left, a live preview on the right, and tabs across the top."
    />
    <p>
      The same page has tabs for the app's <strong>Files</strong> (a browser
      editor), its <strong>Logs</strong> (a feed of actions taken on the app,
      alongside its live, timestamped output), its <strong>Snapshots</strong>,
      and <strong>Settings</strong> (custom domains, rename, the API token and
      SSH keys). Each is covered in its own page in this guide.
    </p>
  </>
);

const AppsPage = () => (
  <>
    <h2>Apps and hostit.yml</h2>
    <p>
      One file decides how an app runs: <span className="mono">hostit.yml</span>{" "}
      in the app's home. Pick a mode, then apply it with{" "}
      <span className="mono">hostit deploy</span> over SSH or{" "}
      <span className="mono">POST /api/apps/&lt;app&gt;/deploy</span>. Keys
      hostit does not know are an error, so a typo is reported rather than
      ignored.
    </p>
    <h3>mode: static</h3>
    <Snippet
      text={`description: What this app is, in one line\nmode: static`}
    />
    <p>
      hostit serves <span className="mono">public/</span> itself. Nothing to
      run, nothing to install -- the directory is always
      <span className="mono"> public/</span>. This is the right mode for plain
      HTML and for the built output of any frontend.
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
      <span className="mono">prepare:</span> is optional and runs before the app
      starts, every deploy: compile, install dependencies, build a frontend into{" "}
      <span className="mono">public/</span>. A failing build stops the deploy
      rather than starting a broken app. The command must listen on{" "}
      <span className="mono">0.0.0.0:$PORT</span>;{" "}
      <span className="mono">$PORT</span> is provided, and hostit restarts the
      command if it exits.
    </p>
    <p>
      While you are still getting the build to work,{" "}
      <span className="mono">POST /api/apps/&lt;app&gt;/run</span> runs a single
      command in the container and returns its output, so an assistant can
      iterate on a compile error without SSH. It is bounded (one minute by
      default, five at most); once the command works, move it into{" "}
      <span className="mono">prepare:</span>.
    </p>
    <h3>Keep the source here, and let hostit build it</h3>
    <p>
      Put the source in <span className="mono">src/</span> and give{" "}
      <span className="mono">hostit.yml</span> a build step. It builds on the
      machine that runs it, so there is no cross-compiling and you need no
      toolchain of your own -- which matters if you are driving all of this from
      a chat window. And the app stays editable: the next session, yours or an
      assistant's, can read the source and change it.
    </p>
    <Snippet
      text={`prepare: cd src && go build -o ../bin/myapp .\nrun: ./bin/myapp`}
    />
    <h3>snapshot hooks (optional)</h3>
    <p>
      If the app has a database, add hooks so a snapshot captures a consistent
      copy: <span className="mono">pre</span> runs before the snapshot,{" "}
      <span className="mono">post</span> after. If{" "}
      <span className="mono">pre</span> fails, the snapshot is skipped rather
      than saving a torn state.
    </p>
    <Snippet
      text={`snapshot:
  pre:  sqlite3 data/app.db ".backup data/app.snap.db"
  post: rm -f data/app.snap.db`}
    />
    <p className="hint">
      Keep <span className="mono">description:</span> current. The dashboard
      shows it, and it is what the next assistant session starts from.
    </p>
  </>
);

const AssistantPage = () => (
  <>
    <h2>The AI assistant</h2>
    <Figure
      src={assistantShot}
      alt="The assistant tab: a chat beside a live preview of the app"
      caption="The assistant works on the app while you watch it change: chat on the left, the running app on the right."
    />
    <p>
      This is what hostit is for. Every app has an assistant that can build and
      change it for you, and the same app works with your own agent (like Claude
      Code) over the API.
    </p>
    <h3>The built-in chat</h3>
    <p>
      Open the app's page and use the <strong>Assistant</strong> tab. Ask in
      plain English ("add a leaderboard", "make the header dark"); it reads and
      writes the app's files, runs commands in the container, and deploys, with
      a live preview beside the chat. Its only tools are that one app's own
      operations, so it can never touch another app or the host.
    </p>
    <p>
      The picker beside the message box chooses the model. It lists everything
      this server can run, grouped by where the turn runs: the operator's Claude
      subscription above the rule, the metered Anthropic API below it. Both can
      offer the same model, so "Sonnet 5" may appear twice -- the Claude or
      Anthropic mark beside each name says which is which. Each app remembers
      what it last used.
    </p>
    <p className="hint">
      The built-in assistant needs the server to be configured with an AI key.
      If it is off, use SSH or your own agent (below).
    </p>
    <h3>Using it with Claude Code (or any agent)</h3>
    <p>
      Open your app's page and copy the prompt at the top. Paste it into Claude
      Code (or any assistant that can make HTTP requests) and it has everything
      it needs: the app's URL, a token scoped to that one app, and the address
      of an endpoint that explains the rest of the API to it.
    </p>
    <p>
      The prompt changes with the app. While the app is still a placeholder it
      asks the assistant to build something; once the app describes itself in{" "}
      <span className="mono">hostit.yml</span>, the prompt says the app already
      exists and asks the assistant to pick up where the last session left off.
    </p>
    <p>
      The token in that prompt can only touch that one app. It cannot read your
      account, your other apps, or anything administrative, which is what makes
      it safe to paste into a chat.
    </p>
    <p className="hint">
      Assistants sometimes start building from the app name alone. The prompt
      tells them not to, and to ask you first.
    </p>
  </>
);

// An app calling the model itself, rather than being built by it.
const AiAppsPage = () => (
  <>
    <h2>Apps that think</h2>
    <p>
      The assistant on the left <b>builds</b> your app. This is the other direction: your app asking
      a model a question <b>while it runs</b>, over the same socket it reads its connections from,
      with no API key of its own.
    </p>
    <p>
      That last part is the point. A key pasted into an app is a key nobody can rotate, nobody can
      meter and every process in that container can read. Going through hostit means the key stays
      on the server, every call is counted against your app, and revoking is something you can
      actually do.
    </p>

    <h3>Asking a question</h3>
    <Snippet
      text={`curl --unix-socket /run/hostit/hostit.sock http://x/api/container/assistant \\
  -d '{"prompt":"Summarise this in one line: ...","max_tokens":100}'`}
    />
    <p>
      Answers <span className="mono">{`{"text":"...","model":"...","usage":{...}}`}</span>. The
      usage is there so an app that wants to stay inside a budget can see what it is spending.
    </p>

    <h3>Holding a conversation</h3>
    <p>
      The model remembers nothing between calls, so a chat sends its whole history each time.
      Use <span className="mono">messages</span> instead of <span className="mono">prompt</span>:
    </p>
    <Snippet
      text={`{
  "system": "You are a helpful assistant who answers as a pirate.",
  "messages": [
    {"role": "user", "content": "What is a container?"},
    {"role": "assistant", "content": "Arrr, a container be..."},
    {"role": "user", "content": "And a subdomain?"}
  ]
}`}
    />

    <h3>What you can ask for</h3>
    <p>Two things this makes easy, both of which used to need an account somewhere:</p>
    <ul>
      <li>
        <b>An app that judges its own output.</b> Read your logs, ask the model whether anything in
        them is serious, and push a notification only when it says yes -- so you are woken for the
        thing that matters and not for every stack trace.
      </li>
      <li>
        <b>An app that talks.</b> A chat page in whatever voice you like, with the conversation kept
        in the browser and each turn posted here.
      </li>
    </ul>
    <p>
      Ask the assistant for either in plain English -- it knows this endpoint exists and will build
      on it rather than telling you to obtain an API key.
    </p>

    <h3>The limits, and why</h3>
    <table className="docs-table">
      <thead>
        <tr><th>Limit</th><th>Why</th></tr>
      </thead>
      <tbody>
        <tr>
          <td>No tools, no file access</td>
          <td>
            The assistant's tools act <i>on</i> an app. An app that could run them against itself
            is a self-modifying loop with nobody in the room.
          </td>
        </tr>
        <tr>
          <td><span className="mono">max_tokens</span> capped, and clamped rather than refused</td>
          <td>The budget being spent is the operator's, not the app's.</td>
        </tr>
        <tr>
          <td>Rate limited per account</td>
          <td>
            The same limit an interactive turn spends. An app looping on this is the cheapest way
            to spend a month of budget by accident, so it gets a{" "}
            <span className="mono">429</span> with the reason rather than a bill.
          </td>
        </tr>
        <tr>
          <td>A cheaper model by default</td>
          <td>
            Pass <span className="mono">"model": "sonnet-5"</span> or{" "}
            <span className="mono">"opus-5"</span> when a job needs it. An app summarising log
            lines every minute should not silently be doing it on the most expensive one.
          </td>
        </tr>
      </tbody>
    </table>
    <p className="hint">
      Available when this server has the assistant configured. Your usage shows up in the same
      per-app total the chat does, so an administrator sees one number per app either way.
    </p>
  </>
);

const FilesPage = () => (
  <>
    <h2>Files and the editor</h2>
    <Figure
      src={editorShot}
      alt="The in-browser file editor"
      caption="The Files tab: a file browser and editor over the app's home directory."
    />
    <p>Every app's home directory has a place for each kind of thing:</p>
    <table className="docs-table">
      <tbody>
        <tr>
          <td className="mono">public/</td>
          <td>
            Files served on the web. Static apps serve exactly this directory.
          </td>
        </tr>
        <tr>
          <td className="mono">bin/</td>
          <td>
            Binaries and scripts the app runs, e.g.{" "}
            <span className="mono">run: ./bin/myapp</span>.
          </td>
        </tr>
        <tr>
          <td className="mono">log/</td>
          <td>
            The app's output, written by hostit.{" "}
            <span className="mono">hostit logs</span> reads it.
          </td>
        </tr>
        <tr>
          <td className="mono">src/</td>
          <td>Source, if you keep the app's source on the host.</td>
        </tr>
        <tr>
          <td className="mono">docs/</td>
          <td>
            The app's own documentation: how it works and why. Read it before
            changing the app, update it after.
          </td>
        </tr>
        <tr>
          <td className="mono">hostit.yml</td>
          <td>How the app runs.</td>
        </tr>
        <tr>
          <td className="mono">README.md</td>
          <td>
            What the app is, and its worklog. Assistants read this first and
            write back to it.
          </td>
        </tr>
      </tbody>
    </table>
    <p>
      Directories appear as you write into them. The Files tab edits any of
      them; "Save &amp; deploy" writes the file and redeploys.
    </p>
    <p className="hint">
      If your app serves files itself (rather than{" "}
      <span className="mono">mode: static</span>), point it at{" "}
      <span className="mono">public/</span> and never at the home directory. The
      home also holds <span className="mono">hostit.yml</span> and{" "}
      <span className="mono">.ssh/</span>, and serving it puts them on the open
      internet.
    </p>
  </>
);

const SSHPage = () => (
  <>
    <h2>SSH, the terminal, and the CLI</h2>
    <Figure
      src={terminalShot}
      alt="The in-browser terminal inside the app container"
      caption="The Terminal tab drops you inside the app's container; the same session you get over SSH."
    />
    <p>
      The <strong>Terminal</strong> tab drops you straight inside the app's
      container in the browser. For your own machine, add an SSH key to your{" "}
      <a href="/profile">profile</a> first -- it applies to every app you own,
      present and future. Without a key, SSH cannot work and the app page says
      so.
    </p>
    <Snippet
      text={`ssh <app>@${baseDomain}
scp ./index.html <app>@${baseDomain}:public/
rsync -av ./site/ <app>@${baseDomain}:public/`}
    />
    <p>
      The session lands <em>inside</em> the app's container, where you are root
      and can <span className="mono">apt-get install</span> whatever you need.
      Installed packages persist: the container's filesystem is the app's own
      durable disk, so redeploys, reboots and platform upgrades keep them.
      Installs count against the app's disk budget.
    </p>
    <p>Inside, these commands manage the app:</p>
    <Snippet
      text={`hostit deploy      # apply hostit.yml and (re)start
hostit status      # is it running?
hostit logs -f     # watch the output
hostit start/stop/restart   # the run: command (container stays up)
hostit poweron/poweroff/reboot   # the container itself`}
    />
  </>
);

const SnapshotsPage = () => (
  <>
    <h2>Snapshots, rollback and fork</h2>
    <Figure
      src={snapshotsShot}
      alt="The Snapshots tab listing snapshots"
      caption="The Snapshots tab: take one now, roll back, or fork a new app from any snapshot."
    />
    <p>
      hostit snapshots your whole app -- its files and everything it installed
      -- before every deploy and every few hours, so you can always go back. Snapshots
      are instant and cheap; take one yourself anytime from the Snapshots tab,{" "}
      <span className="mono">hostit control app snapshot</span>, or the API.
    </p>
    <p>
      The automatic cadence is every three hours by default, and apps are spread
      across that window rather than all snapshotting at once. Change it per app
      in <strong>Settings</strong> or in <span className="mono">hostit.yml</span>:
      set <span className="mono">snapshot.interval</span> to a duration like{" "}
      <span className="mono">45m</span>, or to <span className="mono">0</span> to
      turn the automatic ones off -- the pre-deploy snapshot still happens either
      way. The same section holds the <span className="mono">pre</span> and{" "}
      <span className="mono">post</span> hook commands, which are how an app with
      a database gets a consistent copy.
    </p>
    <h3>Archiving</h3>
    <p>
      <strong>Archive</strong> an app you are done with but do not want to
      delete: it powers off and stays off -- it cannot be powered on, deployed
      to, or started by an SSH login -- and it stops taking new snapshots, your
      own included, since it can no longer change. Its files stay readable and
      writable, so you can look at it or prepare it before bringing it back. Its existing history keeps thinning, but monthly
      snapshots are kept for a year and the most recent one is never removed, so
      an archived app is still there to bring back later.{" "}
      <strong>Unarchive</strong> returns it to an ordinary powered-off app, which
      you then power on when you want it. Both are in the app's Actions menu.
    </p>
    <h3>Rollback</h3>
    <p>
      Roll back to any snapshot from the chat, the Snapshots tab,{" "}
      <span className="mono">hostit control app rollback</span>, or{" "}
      <span className="mono">
        POST /api/apps/&#123;app&#125;/snapshots/&#123;id&#125;/restore
      </span>
      . Rollback restores everything together: your files and the installed
      software around them, so a broken <span className="mono">apt-get</span>{" "}
      run or a deleted system file is undone the same way as a bad edit. It is
      also reversible: the current state is snapshotted first, so you can undo
      an undo.
    </p>
    <h3>Fork</h3>
    <p>
      <strong>Fork</strong> an app into a new one seeded from a copy of its
      entire filesystem -- files, data and installed packages (its own
      subdomain, user and container). Use the snapshot menu on the app page,{" "}
      <span className="mono">hostit control app fork</span>, or{" "}
      <span className="mono">POST /api/apps/&#123;app&#125;/fork</span>. Fork
      the current state, or a specific snapshot.
    </p>
    <p className="hint">
      Retention keeps history useful without growing forever (a
      grandfather-father-son policy). Snapshots live on the same disk and are
      not a substitute for an off-box backup.
    </p>
  </>
);

const DomainsPage = () => (
  <>
    <h2>Custom domains and renaming</h2>
    <Figure
      src={domainsShot}
      alt="The custom domains section of an app's settings"
      caption="Attach your own hostname in Settings; hostit shows the DNS records to create and issues the certificate once they resolve."
    />
    <h3>Custom domains</h3>
    <p>
      Serve an app on your own hostname on top of its subdomain. Add it from the
      app page's Actions menu (or{" "}
      <span className="mono">hostit control app domain add</span>); hostit shows the
      two DNS records to create and obtains the certificate over DNS-01, which
      works even when the server is not publicly reachable. The app is then
      reachable at both its{" "}
      <span className="mono">&lt;name&gt;.{baseDomain}</span> subdomain and your
      domain.
    </p>
    <h3>Renaming</h3>
    <p>
      Change an app's name from the app page's Settings tab. The app keeps
      running and nothing moves, so its files, container and custom domains all
      follow it; only the{" "}
      <span className="mono">&lt;name&gt;.{baseDomain}</span> subdomain and the
      SSH login change. Links to the old subdomain stop working.
    </p>
  </>
);

const VisibilityPage = () => (
  <>
    <h2>Private apps</h2>
    <p>
      Every app is <strong>public</strong> by default: anyone who knows the URL
      can open it, with no sign-in and no account. That is the right default for
      a blog or a demo, and it is what every app did before this setting
      existed.
    </p>
    <p>
      A <strong>private</strong> app is reachable only by you, the people you add
      as collaborators, and administrators. Pick it when you create the app, or
      flip it any time under Settings -&gt; Visibility. It applies to every
      hostname the app answers to, custom domains included, and takes effect
      within about a second.
    </p>
    <h3>What a visitor sees</h3>
    <p>
      Someone already signed in with access goes straight through; the first
      visit bounces through hostit to pick up a per-app credential, which is
      invisible apart from the address bar settling. Someone signed out is sent
      to sign in and then returned to the app they were opening, so your own
      app still works from a browser you have never used it on.
    </p>
    <p>
      Someone signed in <em>without</em> access is told plainly that the app is
      private and to ask its owner for access, along with which account they
      are currently signed in as -- being signed in as the wrong one is the
      usual reason. A hostname nobody has deployed still says nothing at all.
    </p>
    <h3>Scripts, webhooks and agents</h3>
    <p>
      Send an API token and no redirect happens:{" "}
      <span className="mono">curl -H &quot;Authorization: Bearer &lt;token&gt;&quot;
      https://&lt;name&gt;.{baseDomain}/</span>. Your account token works for any
      app you can reach, and the app&apos;s own scoped token works for that app.
    </p>
    <h3>Letting other people in</h3>
    <p>
      Two different grants, and the difference is the point. A{" "}
      <strong>collaborator</strong> can deploy, edit files, use the terminal and
      SSH in -- and can therefore open the app too. Someone with{" "}
      <strong>access</strong> can only open the app&apos;s URL: no files, no
      terminal, no deploys, and it does not appear on their dashboard. Add
      those under Settings -&gt; Visibility, by the email of an existing
      account.
    </p>
    <p>
      An app with people on that list reads as <strong>Restricted</strong>{" "}
      rather than Private. It is the same setting -- just a way to tell &quot;only
      me&quot; from &quot;me and two others&quot; at a glance.
    </p>
    <h3>Signing out of one app</h3>
    <p>
      Opening <span className="mono">/hostit/logout</span> on the app&apos;s own
      hostname drops the credential for that app and returns you here. Your
      hostit session is untouched, so opening the app again lets you straight
      back in; to leave properly, sign out of hostit itself.{" "}
      <span className="mono">/hostit/auth</span> is the other direction -- it
      asks for access without waiting to be refused first.
    </p>
    <p>
      One thing to know: no screenshots are taken of a private app, so its
      dashboard card shows a placeholder rather than a picture.
    </p>
  </>
);

// Connections, for the person using them rather than the operator wiring them up.
const LimitsPage = () => (
  <>
    <h2>Resource limits and pools</h2>
    <p>
      Every app runs inside three enforced caps: <b>RAM</b> (allocating past it
      gets the process killed and restarted), <b>disk</b> (a hard budget over
      everything in the app -- files, installed packages and snapshots together;
      writing past it fails with <span className="mono">Disk quota exceeded</span>),
      and <b>CPU</b> (a cores cap; the app is throttled, never killed). New apps
      start small -- 128 MB RAM, 256 MB disk, half a core -- because most apps
      are small; raise what a real one needs.
    </p>
    <Figure
      src={resourcesShot}
      alt="The resources dialog on an app's settings page"
      caption="The pencil on the Resources card opens this: pick from the presets, and the dialog tells you what is left in your pool."
    />
    <p>
      As an owner you edit RAM and disk yourself: open the app&apos;s{" "}
      <b>Settings</b> tab and hit the pencil on the <b>Resources</b> card (or{" "}
      <b>Actions &rarr; Change resources</b>). Your budget is a per-user{" "}
      <b>pool</b>: all your apps&apos; limits together must fit it, the dialog
      shows what is left, and choices that no longer fit are grayed out. The
      dashboard header shows your pool at a glance, and readings turn yellow at
      75% and red at 90% everywhere they appear. Disk changes apply immediately;
      RAM and CPU at the next reboot or deploy -- nothing restarts just because
      you saved a number. CPU caps and pool sizes are set by an admin.
    </p>
    <p>
      Your own AI agent can read the app&apos;s budget from{" "}
      <span className="mono">GET /api/apps/&#123;app&#125;/info</span> (the{" "}
      <span className="mono">limits</span> object) but deliberately cannot change
      it -- an assistant must not raise its own caps.
    </p>
  </>
);

// A link to another docs page, from inside the docs. Real anchors, same as
// everywhere else -- see docs.js on why.
const DocsPageLink = ({ guide, section, sub, children }) => (
  <a className="docs-link" href={docsHref(guide, section, sub)}>{children}</a>
);

// Connections: the front page of the three kinds.
const ConnectionsPage = () => (
  <>
    <h2>Connections</h2>
    <p>
      <b>Connections</b> is the word for all of it: attach an account, a secret or a tool server
      <b> once</b>, then grant it to whichever apps should use it. An app asks hostit for what it
      needs when it needs it -- it never holds your password, and nothing is baked into a file you
      would have to redeploy to change.
    </p>
    <p>Three kinds, each with its own card on the <b>Connections</b> page:</p>
    <ul>
      <li>
        <DocsPageLink guide="user" section="connections" sub="accounts">Accounts</DocsPageLink>{" "}
        are services you sign in to -- Google Calendar, Gmail, Slack, Discord, GitHub, Jira,
        Linear, HubSpot. You approve them at the provider and hostit keeps the permission.
      </li>
      <li>
        <DocsPageLink guide="user" section="connections" sub="credentials">Credentials</DocsPageLink>{" "}
        are secrets you paste -- an API key, an SSH key, a database URL, a mailbox password.
        Nothing to approve and nothing that expires.
      </li>
      <li>
        <DocsPageLink guide="user" section="connections" sub="mcp">MCP servers</DocsPageLink> are
        tool servers you point hostit at by URL. hostit signs in if the server asks it to, holds
        the token, and calls the tools for your apps.
      </li>
    </ul>

    <h3>Name and reference</h3>
    <p>
      Every connection has two labels, and they do different jobs. The <b>name</b> is for you --
      &ldquo;Work calendar&rdquo; -- and changing it affects nothing else. The <b>reference</b> is
      what an app asks for, a short lowercase handle like <span className="mono">work-calendar</span>,
      and it is filled in from the name so you do not have to invent both. Changing a reference
      breaks any app already using the old one, so the dialog says so.
    </p>
    <p>
      This is why you can attach the <b>same service twice</b>: a work calendar and a personal one
      are two connections with two references, and an app asks for whichever it was granted.
      References are unique across all three kinds -- an app addresses a connection by reference
      alone, so no two can share one.
    </p>

    <h3>Granting an app</h3>
    <p>
      Attaching something gives no app access to it. Open the app, go to its <b>Connections</b>{" "}
      tab, and grant it there. Revoking takes effect immediately -- no redeploy, no restart.
      Removing the connection entirely cuts off every app at once.
    </p>
    <p>
      A grant is per app and per connection, whichever kind it is. Granting the work calendar does
      not hand over the personal one.
    </p>
    <p>
      Then see{" "}
      <DocsPageLink guide="user" section="connections" sub="using">Using them in an app</DocsPageLink>{" "}
      for what an app actually does with what it was granted.
    </p>
  </>
);

const AccountsPage = () => (
  <>
    <h2>Accounts</h2>
    <p>
      An <b>account</b> is a service you sign in to. hostit sends you to the provider, you approve
      it there, and hostit keeps the permission -- then hands your apps a short-lived token
      whenever they ask, refreshing it in the background.
    </p>

    <h3>Connecting one</h3>
    <ol className="docs-steps">
      <li>Open <b>Connections</b> and click <b>Add account</b>.</li>
      <li>Pick the service. Only services your administrator has set up are listed.</li>
      <li>
        Give it a name you will recognise (&ldquo;Work calendar&rdquo;). The reference is filled in
        for you.
      </li>
      <li>
        Click <b>Continue to&hellip;</b>. You land on the provider&rsquo;s own consent screen; approve
        it and you come back to hostit with the account attached.
      </li>
    </ol>
    <p>
      If the service you want is not in the menu, it has no OAuth client on this instance yet. Any
      service can be added -- see{" "}
      <DocsPageLink guide="admin" section="connections" sub="custom">your own provider</DocsPageLink>{" "}
      in the administration guide, or ask your administrator.
    </p>

    <h3>Reconnecting</h3>
    <p>
      If a provider expires or revokes its permission, apps start failing and the fix is{" "}
      <b>Reconnect</b> on the connection&rsquo;s menu. That keeps the reference and every grant, and
      only replaces the credential underneath -- no app needs regranting.
    </p>
    <p className="hint">
      Google is the one provider likely to need this regularly: while an instance is unverified,
      Google expires its permission after seven days. Attaching a calendar over <b>CalDAV</b>, or a
      mailbox over <b>IMAP</b> with an app password, avoids that entirely -- both are{" "}
      <DocsPageLink guide="user" section="connections" sub="credentials">credentials</DocsPageLink>{" "}
      and need no review at all.
    </p>

    <h3>What each account gives an app</h3>
    <table className="docs-table">
      <thead>
        <tr><th>Account</th><th>What an app can do with it</th></tr>
      </thead>
      <tbody>
        <tr><td>Google Calendar</td><td>Read and write the calendars on that Google account</td></tr>
        <tr><td>Gmail</td><td>Read that mailbox (read-only)</td></tr>
        <tr><td>Slack (bot)</td><td>Read and post in channels the bot has been invited to, look up users</td></tr>
        <tr><td>Slack (personal)</td><td>Read the public and private channels you are already in, and search across them, as you -- no bot to invite</td></tr>
        <tr><td>Discord</td><td>Your profile and which servers you are in. Reading a server&rsquo;s channels needs a <b>Discord bot</b> credential instead</td></tr>
        <tr><td>GitHub</td><td>Your repositories, at the scopes the instance registered</td></tr>
        <tr><td>Jira</td><td>Issues and projects on your Atlassian site</td></tr>
        <tr><td>Linear</td><td>Issues, projects and teams</td></tr>
        <tr><td>HubSpot</td><td>Contacts and deals on the connected portal</td></tr>
      </tbody>
    </table>
  </>
);

const CredentialsPage = () => (
  <>
    <h2>Credentials</h2>
    <p>
      A <b>credential</b> is a secret you paste in: an API key, a token, an SSH key, a database
      URL, a mailbox password. hostit encrypts it and hands it back to the apps you grant it to.
      Nothing to approve, nothing to review, and nothing that expires.
    </p>
    <p>
      This is often the <b>better</b> option even when the service also does OAuth. A Gmail app
      password over IMAP needs no Google review and never expires; the OAuth route needs both.
    </p>

    <h3>Adding one</h3>
    <ol className="docs-steps">
      <li>Open <b>Connections</b> and click <b>Add credential</b>.</li>
      <li>Search the menu, or pick <b>Add other credential</b> at the bottom for anything not listed.</li>
      <li>Name it, fill in the fields, and save. The secret is never shown again.</li>
    </ol>

    <h3>What you can store</h3>
    <table className="docs-table">
      <thead>
        <tr><th>Credential</th><th>What it needs</th></tr>
      </thead>
      <tbody>
        <tr><td>Fastmail</td><td>One API token for mail, calendars and contacts. Settings &rarr; Privacy &amp; Security &rarr; API tokens</td></tr>
        <tr><td>IMAP mailbox</td><td>Server, username, password. Gmail works at <span className="mono">imap.gmail.com:993</span> with an app password</td></tr>
        <tr><td>SMTP</td><td>Server, username, password, optional From address</td></tr>
        <tr><td>CalDAV calendar</td><td>Server URL, username, password. Fastmail, iCloud, Nextcloud</td></tr>
        <tr><td>CardDAV contacts</td><td>The same, for address books</td></tr>
        <tr><td>PostgreSQL / MySQL</td><td>A connection URL</td></tr>
        <tr><td>OpenSearch / Elasticsearch</td><td>Endpoint, and either basic auth or an API key</td></tr>
        <tr><td>S3 storage</td><td>Access key and secret. Works with DO Spaces, R2, B2, MinIO</td></tr>
        <tr><td>ntfy</td><td>An access token, and optionally a server and default topic</td></tr>
        <tr><td>Home Assistant</td><td>Base URL and a long-lived access token</td></tr>
        <tr><td>SSH key</td><td>A private key with <b>no passphrase</b> -- an app cannot type one</td></tr>
        <tr><td>Discord bot</td><td>A bot token from your Discord application&rsquo;s Bot tab. This, not the Discord account, is what reads a server&rsquo;s channels</td></tr>
        <tr><td>API key or token</td><td>The catch-all: any secret at all, with an optional endpoint and note</td></tr>
      </tbody>
    </table>

    <h3>Editing and removing</h3>
    <p>
      A credential&rsquo;s menu offers <b>Edit</b> -- which renames it, or replaces the secret in place
      while keeping every grant -- and <b>Remove</b>, which deletes it and cuts off every app at
      once. The dialog says how many apps are using it before you confirm.
    </p>
  </>
);

const McpPage = () => (
  <>
    <h2>MCP servers</h2>
    <p>
      An <b>MCP server</b> is a service that exposes <b>tools</b> over a standard protocol -- search
      this mailbox, list those issues, create that document. Growing numbers of products publish
      one. You add it with nothing but its URL.
    </p>

    <h3>Adding one</h3>
    <ol className="docs-steps">
      <li>Open <b>Connections</b> and click <b>Add MCP server</b>.</li>
      <li>Name it, and paste the server&rsquo;s endpoint URL.</li>
      <li>
        Click <b>Connect</b>. hostit asks the server what it needs. If it wants you to sign in, you
        are sent to approve it and come back; if it does not, it is attached straight away.
      </li>
    </ol>
    <p>
      Nothing has to be registered by an administrator first -- this is the one kind you can add
      entirely by yourself. Once attached, the row shows the endpoint and a tool count; click the
      count to see exactly what the server offers before you grant it to anything.
    </p>
    <p>Public servers you can try right now, with no account and no sign-in:</p>
    <Snippet text={`https://mcp.deepwiki.com/mcp`} />

    <h3>What makes them different</h3>
    <p>
      hostit does <b>not</b> hand the token to your app. An MCP token opens the whole server --
      every tool, your whole account there -- so giving it to an app would make the grant
      meaningless. Instead the app sends a tool name and arguments and hostit makes the call.
    </p>
    <p>
      That means an app needs no MCP client, no sign-in of its own, and nothing to refresh. It also
      means the <b>assistant</b> can use the tools directly: grant an app an MCP server and its
      tools appear in the assistant&rsquo;s own tool list, so you can just ask for what you want.
    </p>
    <p className="hint">
      Some servers cannot be connected: if the service behind one only accepts OAuth clients
      registered by hand, hostit says so when you add it and names the server, so you know what
      your administrator would have to register. GitHub&rsquo;s MCP endpoint is currently one of
      these.
    </p>
  </>
);

const OwnServicesPage = () => (
  <>
    <h2>Your own services</h2>
    <p>
      If the service you want is not in the <b>Add account</b> menu, you can add it yourself. You do
      not need an administrator, and you do not need hostit to have heard of the service: you
      register an OAuth app with them, and paste the client into hostit.
    </p>
    <p>
      Nothing about OAuth requires the client to belong to the server. The only piece that is
      hostit&rsquo;s is the callback URL, which is why the dialog shows it to you first.
    </p>

    <h3>Adding one</h3>
    <ol className="docs-steps">
      <li>
        Open <b>Connections</b>, click <b>Add account</b>, and pick{" "}
        <b>Add your own service</b> at the bottom of the menu. Copy the callback URL it shows.
      </li>
      <li>
        Go to the service&rsquo;s developer settings and create an OAuth application, pasting that
        callback URL as its redirect URI.
      </li>
      <li>
        Copy the <b>client ID</b> and <b>client secret</b> it gives you back into the hostit dialog,
        along with the scopes you want.
      </li>
      <li>
        Give it the <b>authorize</b> and <b>token</b> URLs from the service&rsquo;s documentation --
        or, if they publish OAuth metadata, just the <b>issuer</b> and hostit will find both.
      </li>
    </ol>
    <p>
      It then appears in your Add account menu like any other service, marked as yours. Connect it,
      grant it to an app, and it behaves exactly like one hostit ships.
    </p>

    <h3>What is yours and what is not</h3>
    <ul>
      <li>
        <b>Only you see it.</b> Another user on this instance sees neither the definition nor any
        account you connect through it, and the name is free for them to use for something else.
      </li>
      <li>
        <b>You cannot redefine a name hostit or your administrator already uses.</b>{" "}
        <span className="mono">github</span> has to keep meaning GitHub, for everybody.
      </li>
      <li>
        <b>The client secret is encrypted</b> the same way every other credential here is, and is
        never shown again once saved.
      </li>
      <li>
        <b>Removing the definition</b> leaves accounts connected through it working until their
        token expires, after which they cannot be refreshed. Your OAuth app at the service itself is
        untouched.
      </li>
    </ul>

    <h3>This is not how you add an MCP server</h3>
    <p>
      An <b>MCP server</b> needs none of this. You add one straight from the <b>MCP servers</b>{" "}
      card by pasting its URL: nothing to register, no client, no secret. This page is only for a
      service you <i>sign in to</i>, where somebody has to have registered an OAuth app.
    </p>
    <p className="hint">
      Your administrator can give an MCP server a <b>name</b>, so it becomes a pick in the Add
      menu rather than a URL to remember. That is a convenience for everyone on the instance, and
      not something you set up per person -- if you want a server, just connect it.
    </p>

    <h3>When a service will not have you</h3>
    <p>
      Some services only accept OAuth clients they have approved in advance, and refuse to register
      a self-hosted instance at all. If that happens hostit says so and names the service. There is
      nothing to fix -- it is their policy, not your mistake. Check whether they offer an API token
      instead, which you can store as a <DocsPageLink guide="user" section="connections" sub="credentials">credential</DocsPageLink>{" "}
      with no OAuth at all.
    </p>
  </>
);

const UsingPage = () => (
  <>
    <h2>Using them in an app</h2>
    <p>
      An app reads what it was granted over its own unix socket. No token, no hostname, no
      configuration -- the socket is inside the container and hostit knows which app is calling.
    </p>

    <h3>What am I holding?</h3>
    <Snippet text={`curl --unix-socket /run/hostit/hostit.sock http://x/api/container/connections`} />
    <p>
      Answers a list, each entry carrying the reference to ask for, the provider, and the kind.
    </p>

    <h3>An account or a credential</h3>
    <Snippet
      text={`curl --unix-socket /run/hostit/hostit.sock \\
  http://x/api/container/connections/work-calendar/token`}
    />
    <p>
      Answers <span className="mono">{`{"access_token": "...", "expires_at": "..."}`}</span>, with{" "}
      <span className="mono">expires_at</span> absent when the credential does not expire. Fetch it
      when you need it rather than saving it: an account token expires within the hour, and asking
      again is what makes revoking work.
    </p>
    <p className="hint">
      <b>Never</b> print a token, echo it into a log, or write it to a file. Read it at the moment
      it is used.
    </p>

    <h3>An MCP server</h3>
    <Snippet
      text={`curl --unix-socket /run/hostit/hostit.sock http://x/api/container/mcp/issues/tools

curl --unix-socket /run/hostit/hostit.sock \\
  http://x/api/container/mcp/issues/call \\
  -d '{"tool":"list_issues","arguments":{"team":"core"}}'`}
    />
    <p>
      The call answers <span className="mono">{`{"text": "...", "is_error": false}`}</span>.{" "}
      <span className="mono">is_error</span> means the <b>tool</b> failed -- the call happened and
      the answer is bad news -- which is still an HTTP 200, because there is nothing to retry.
    </p>

    <h3>Asking for the wrong thing</h3>
    <p>
      An MCP server has no credential to hand out, so asking one for a token answers <b>404</b> and
      says where to call its tools instead. Asking a credential for tools answers the same way. A
      connection you were not granted answers <b>403</b>; one that does not exist answers{" "}
      <b>404</b> -- two different fixes, in two different places, so they are two different codes.
    </p>

    <h3>The assistant already knows</h3>
    <p>
      The built-in assistant is told which connections an app holds, so you can ask it to build
      something that uses one without explaining any of the above. Granted MCP tools go further:
      they become tools the assistant can call directly.
    </p>
    <p className="hint">
      Both roots work: <span className="mono">/api/container/&hellip;</span> and the original{" "}
      <span className="mono">/v1/&hellip;</span>. They are the same surface on the same socket, and{" "}
      <span className="mono">/v1</span> will keep answering forever.
    </p>
  </>
);

const ApiPage = () => (
  <>
    <h2>API reference</h2>
    <Figure
      src={profileShot}
      alt="The profile page: SSH keys and API tokens"
      caption="API tokens live on your Profile page. A token authenticates the CLI and every call below; each app also has its own scoped token."
    />
    <p>
      Two kinds of credential. An <strong>app token</strong> (shown on the app's
      page) can only touch that app, through{" "}
      <span className="mono">/api/apps/&lt;app&gt;/</span>. An{" "}
      <strong>account token</strong> (Profile -&gt; API tokens) can do anything
      you can, including creating and deleting apps.
    </p>
    <Snippet
      text={`curl -H "Authorization: Bearer <token>" ${origin}/api/apps/<app>/info`}
    />
    <p>
      <span className="mono">/api/apps/&lt;app&gt;/info</span> returns the app's
      state and a full description of the API, so an assistant pointed at that
      one URL needs nothing else.
    </p>
    <h3>App API</h3>
    <div className="table-wrap">
      <table className="docs-table">
        <tbody>
          <Endpoint
            method="GET"
            path="/api/apps/{app}/info"
            what="State, README, file list, config, and the guide"
          />
          <Endpoint
            method="GET"
            path="/api/apps/{app}/logs?lines=N"
            what="Recent output"
          />
          <Endpoint
            method="GET"
            path="/api/apps/{app}/assistant/transcript"
            what="The built-in assistant's chat history for this app, as markdown"
          />
          <Endpoint
            method="GET"
            path="/api/apps/{app}/files?path="
            what="List one directory (type file|dir) of the app's files"
          />
          <Endpoint
            method="GET"
            path="/api/apps/{app}/files/{path}"
            what="Read one file"
          />
          <Endpoint
            method="PUT"
            path="/api/apps/{app}/files/{path}?mode=755"
            what="Write one file; mode makes it executable"
          />
          <Endpoint
            method="DELETE"
            path="/api/apps/{app}/files/{path}"
            what="Delete one file"
          />
          <Endpoint
            method="POST"
            path="/api/apps/{app}/move"
            what={`Rename or move a file: {"from": "src/old.go", "to": "src/new.go"}`}
          />
          <Endpoint
            method="POST"
            path="/api/apps/{app}/mkdir"
            what={`Create a directory: {"path": "src/handlers"}`}
          />
          <Endpoint
            method="POST"
            path="/api/apps/{app}/files"
            what="Upload a tar archive (Content-Type: application/x-tar)"
          />
          <Endpoint
            method="PUT"
            path="/api/apps/{app}/readme"
            what={`Replace README.md: {"readme": "..."}`}
          />
          <Endpoint
            method="POST"
            path="/api/apps/{app}/run"
            what={`Run one command in the container: {"command": "cd src && go build ./..."}`}
          />
          <Endpoint
            method="POST"
            path="/api/apps/{app}/deploy"
            what="Apply hostit.yml and (re)start"
          />
          <Endpoint
            method="GET|POST"
            path="/api/apps/{app}/snapshots"
            what={`List snapshots, or take one now: {"label": "short reason"}`}
          />
          <Endpoint
            method="POST"
            path="/api/apps/{app}/snapshots/{id}/restore"
            what="Roll back to a snapshot (a safety snapshot is taken first)"
          />
          <Endpoint
            method="DELETE"
            path="/api/apps/{app}/snapshots/{id}"
            what="Delete one snapshot"
          />
          <Endpoint
            method="POST"
            path="/api/apps/{app}/start|stop|restart"
            what="The run: command: start / stop / restart it (container stays up)"
          />
          <Endpoint
            method="POST"
            path="/api/apps/{app}/poweron|poweroff|reboot"
            what="The container: power on / off / reboot"
          />
        </tbody>
      </table>
    </div>
    <h3>Account API</h3>
    <div className="table-wrap">
      <table className="docs-table">
        <tbody>
          <Endpoint
            method="GET"
            path="/api/account"
            what="Who you are, your limits and usage"
          />
          <Endpoint
            method="GET|POST"
            path="/api/apps"
            what="List your apps, or create one"
          />
          <Endpoint
            method="GET|DELETE"
            path="/api/apps/{name}"
            what="One app, or delete it"
          />
          <Endpoint
            method="POST"
            path="/api/apps/{name}/rename"
            what={`Rename an app: {"new_name": "..."} (nothing moves; the app keeps running)`}
          />
          <Endpoint
            method="POST"
            path="/api/apps/{name}/fork"
            what={`Duplicate an app: {"new_name": "...", "snapshot_id": "optional"}`}
          />
          <Endpoint
            method="GET|POST|DELETE"
            path="/api/apps/{name}/domains"
            what="List, attach, or remove a custom domain"
          />
          <Endpoint
            method="POST"
            path="/api/apps/{name}/token"
            what="Rotate the app's agent token"
          />
          <Endpoint
            method="GET|POST"
            path="/api/account/keys"
            what="Your SSH keys"
          />
          <Endpoint
            method="GET|POST"
            path="/api/account/tokens"
            what="Your account tokens"
          />
          <Endpoint
            method="PUT|DELETE"
            path="/api/account/keys/{id}"
            what={`Rename an SSH key ({"label":"..."}) or remove it`}
          />
          <Endpoint
            method="GET|POST"
            path="/api/connections?kind="
            what={`Your connections and credentials, plus what this server can attach. Add ?kind=oauth, ?kind=static or ?kind=mcp to narrow both lists to one kind; an unknown kind is refused rather than ignored. POST {"provider":"...","slug":"...","label":"...","values":{...}} -- a pasted credential saves immediately, an OAuth one answers with a redirect_url to send the browser to. An MCP server is provider "mcp" with values {"url":"https://..."}, and answers either way depending on whether that server wants authorization`}
          />
          <Endpoint
            method="PUT|DELETE"
            path="/api/connections/{slug}"
            what={`Rename it or replace a pasted credential ({"slug":"...","label":"...","values":{...}}), or remove it. Removing cuts off every app at once`}
          />
          <Endpoint
            method="POST"
            path="/api/connections/{slug}/reconnect"
            what="Re-consent an OAuth account or MCP server in place, keeping its reference and every grant"
          />
          <Endpoint
            method="GET"
            path="/api/connections/{slug}/mcp/tools"
            what="Re-read what an MCP connection's server offers"
          />
          <Endpoint
            method="GET"
            path="/api/apps/{name}/connections"
            what="What this app was granted, and what else could be granted to it"
          />
          <Endpoint
            method="PUT|DELETE"
            path="/api/apps/{name}/connections/{slug}"
            what="Grant one of your connections to an app, or revoke it. Takes effect immediately -- no deploy, no restart"
          />
          <Endpoint method="PUT" path="/api/apps/{name}/description" what={`Set the app's one-line description: {"description":"..."}`} />
          <Endpoint method="PUT" path="/api/apps/{name}/visibility" what={`Public or private: {"private":true}`} />
          <Endpoint method="PATCH" path="/api/apps/{name}/limits" what={`Memory and disk for this app: {"memory_mb":512,"disk_mb":2048}. Bounded by your pool`} />
          <Endpoint method="PUT" path="/api/apps/{name}/snapshot-config" what={`Automatic snapshot interval: {"interval":"3h"}. Written into hostit.yml`} />
          <Endpoint method="PUT" path="/api/apps/{name}/keys" what={`SSH keys allowed into this app's container: {"ssh_keys":["ssh-ed25519 ..."]}`} />
          <Endpoint method="POST" path="/api/apps/{name}/transfer" what={`Hand the app to another account: {"email":"..."}. You lose ownership`} />
          <Endpoint method="GET|POST" path="/api/apps/{name}/collaborators" what={`Who can change this app. POST {"email":"..."}`} />
          <Endpoint method="DELETE" path="/api/apps/{name}/collaborators/{userID}" what="Remove a collaborator" />
          <Endpoint method="GET|POST" path="/api/apps/{name}/viewers" what={`Who can view a PRIVATE app. POST {"email":"..."}`} />
          <Endpoint method="DELETE" path="/api/apps/{name}/viewers/{userID}" what="Remove a viewer" />
          <Endpoint method="DELETE" path="/api/apps/{name}/domains/{domain}" what="Detach a custom domain" />
          <Endpoint method="POST" path="/api/apps/{name}/domains/{domain}/verify" what="Re-check the DNS for a pending custom domain, rather than waiting for the next sweep" />
          <Endpoint method="GET" path="/api/apps/{name}/events" what="This app's activity feed: deploys, restarts, grants, domain changes" />
          <Endpoint method="POST" path="/api/apps/{name}/preview" what="Take a fresh dashboard screenshot now" />
          <Endpoint method="GET" path="/api/apps/{name}/preview.png" what="That screenshot" />
          <Endpoint method="POST" path="/api/apps/{name}/archive" what="Shelve the app: it stops serving and stops counting against your app limit" />
          <Endpoint method="POST" path="/api/apps/{name}/unarchive" what="Bring it back" />
          <Endpoint method="DELETE" path="/api/account/tokens/{id}" what="Revoke one of your API tokens" />
          <Endpoint method="DELETE" path="/api/account/keys/{id}" what="Remove one of your SSH keys" />
          <Endpoint method="GET" path="/api/health" what="Liveness, unauthenticated" />
        </tbody>
      </table>

      <h3>The assistant</h3>
      <table className="docs-table">
        <thead>
          <tr><th>Method</th><th>Path</th><th>What it does</th></tr>
        </thead>
        <tbody>
          <Endpoint method="GET" path="/api/apps/{name}/assistant" what="The conversation so far, plus token usage and cost" />
          <Endpoint method="POST" path="/api/apps/{name}/assistant" what={`Send a message: {"text":"...","model":"..."}. The reply arrives on the stream below`} />
          <Endpoint method="DELETE" path="/api/apps/{name}/assistant/transcript" what="Clear the conversation" />
          <Endpoint method="GET" path="/api/apps/{name}/assistant/stream" what="Server-sent events: tokens, tool calls and results as they happen" />
          <Endpoint method="POST" path="/api/apps/{name}/assistant/stop" what="Interrupt the turn in progress" />
          <Endpoint method="POST" path="/api/apps/{name}/assistant/upload" what="Attach an image to the next message (multipart)" />
        </tbody>
      </table>
      <p className="hint">
        <span className="mono">/api/apps/{"{name}"}/terminal</span> exists too, but it is a
        websocket carrying a PTY rather than a REST endpoint. Use{" "}
        <span className="mono">ssh</span> for anything scripted.
      </p>

      <h3>Administrators only</h3>
      <p>Every path here answers 403 for a normal account, whatever its token.</p>
      <table className="docs-table">
        <thead>
          <tr><th>Method</th><th>Path</th><th>What it does</th></tr>
        </thead>
        <tbody>
          <Endpoint method="GET|POST" path="/api/users" what={`Every account. POST {"email":"..."} invites one`} />
          <Endpoint method="PATCH|DELETE" path="/api/users/{id}" what={`Approve, suspend, change role or set per-user pools: {"status":"active","role":"admin","memory_pool_mb":2048}`} />
          <Endpoint method="GET|POST" path="/api/domains" what={`Domains this instance will serve apps on. POST {"domain":"example.com"}`} />
          <Endpoint method="DELETE" path="/api/domains/{domain}" what="Stop serving a domain" />
          <Endpoint method="GET" path="/api/cluster" what="The nodes, their health, and what each is running" />
          <Endpoint method="GET|PATCH" path="/api/settings" what="Instance defaults: per-app memory and disk, app limits, the assistant's budget" />
          <Endpoint method="POST" path="/api/connections/rotate-key" what="Re-seal every stored credential under a fresh key. Runs in the live process; the previous key is kept as connections.key.previous until you delete it" />
        </tbody>
      </table>

      <h3>From inside an app</h3>
      <p>
        The endpoints above are the account API, driven with a token from anywhere. An app also has
        a <b>second, smaller API of its own</b>, served over a unix socket inside its container. No
        token is involved: hostit knows which app is calling from the socket&rsquo;s peer
        credentials, so an app can only ever reach its own things. It is reachable <i>only</i> on
        that socket -- never from the web, with or without a token.
      </p>
      <p className="hint">
        The same endpoints also answer under <span className="mono">/v1/...</span>, which is what
        apps written before this and the in-container <span className="mono">hostit</span> CLI use.
        Both work and will keep working; <span className="mono">/api/container</span> is simply the one
        worth writing down.
      </p>
      <table className="docs-table">
        <thead>
          <tr><th>Method</th><th>Path</th><th>What</th></tr>
        </thead>
        <tbody>
          <Endpoint method="GET" path="/api/container/connections" what="The connections and credentials this app was granted, each with the reference to ask for" />
          <Endpoint method="GET" path="/api/container/connections/{slug}/token" what={`A usable credential: {"provider":"...","access_token":"...","expires_at":"..."} -- expires_at is absent when it does not expire. An MCP server has no credential to hand out, so this answers 404 for one and says where to call its tools instead`} />
          <Endpoint method="GET" path="/api/container/mcp/{slug}/tools" what="The tools a granted MCP server offers, each with its own JSON Schema" />
          <Endpoint method="POST" path="/api/container/mcp/{slug}/call" what={`Run one tool: {"tool":"...","arguments":{...}}. Answers {"text":"...","is_error":false} -- is_error means the TOOL failed, which is still a 200: the call happened and the answer is bad news`} />
          <Endpoint method="GET" path="/api/container" what="This app: its URL, limits, domains and state" />
          <Endpoint method="POST" path="/api/container/deploy" what="Deploy the app, as the web app's button does" />
          <Endpoint method="POST" path="/api/container/start|stop|restart" what="The app process" />
          <Endpoint method="POST" path="/api/container/poweron|poweroff|reboot" what="The container" />
          <Endpoint method="GET" path="/api/container/status" what="Whether it is running" />
          <Endpoint method="GET" path="/api/container/logs" what="Recent output" />
          <Endpoint method="POST" path="/api/container/ensure" what="Provision the workspace if it does not exist yet. What an SSH login calls" />
          <Endpoint method="POST" path="/api/container/assistant" what={`Ask a model a question: {"prompt":"..."} or {"messages":[...]}, plus optional system, model and max_tokens. Answers {"text","model","usage"}. Inference only -- no tools -- metered to this app and rate limited`} />
        </tbody>
      </table>
      <Snippet
        text={`# Inside the container -- no token, no host, just the socket\ncurl --unix-socket /run/hostit/hostit.sock http://x/api/container/connections\ncurl --unix-socket /run/hostit/hostit.sock http://x/api/container/connections/work-calendar/token`}
      />
      <p className="hint">
        Ask for a credential when you need it rather than saving it: an account token expires within
        the hour, and asking again is what makes revoking a grant take effect immediately.
      </p>
      <p className="hint">
        One more path exists on this socket and is deliberately not part of the API:{" "}
        <span className="mono">POST /api/container/tool/{"{name}"}</span> runs a single assistant
        tool against the calling app. It is how the sandboxed assistant backend reaches its tools,
        and its shape follows that backend rather than any compatibility promise.
      </p>
    </div>
  </>
);

// ---- Administration guide pages -----------------------------------------------

const InstallPage = () => (
  <>
    <h2>Installation</h2>
    <p>
      hostit runs as a root daemon on one Linux host (Debian or Ubuntu). It
      drives podman, systemd, nftables and btrfs, terminates TLS, and serves
      everything on this page. You need a host you control, root on it, a
      domain, the apps directory on btrfs, podman 4.3 or newer, and crun 1.29 or
      newer (app containers run their subvolume through an idmapped rootfs
      mount). All of it is preflighted on start, failing with one clear message
      naming exactly what to fix. Most distributions ship an older crun; the
      README shows the two-minute static-binary drop-in.
    </p>
    <h3>1. DNS</h3>
    <p>
      Apps are served at{" "}
      <span className="mono">&lt;app&gt;.&lt;base-domain&gt;</span>, so point a
      wildcard and the bare name at the host:
    </p>
    <Snippet
      text={`*.apps.example.com.  A  <ip-of-this-host>
apps.example.com.    A  <ip-of-this-host>   ; SSH and the bare domain`}
    />
    <h3>2. Install the packages</h3>
    <p>
      hostit ships one package per component:{" "}
      <span className="mono">hostit-control</span> (the brain: dashboard, API,
      certificates, placement), <span className="mono">hostit-proxy</span> (the
      data plane on :443), and <span className="mono">hostit-node</span> (the
      machine half). On one host you install all three and run two of them:
      control does the machine work itself here, and the node package is what
      carries the <span className="mono">hostit</span> CLI that is mounted into
      every app container, the per-app systemd unit, the app login shell and the
      sudoers grant.
    </p>
    <Snippet
      text={`sudo dpkg -i hostit-control_*_linux_amd64.deb hostit-node_*_linux_amd64.deb hostit-proxy_*_linux_amd64.deb`}
    />
    <p>
      Each package lands its own config under{" "}
      <span className="mono">/etc/hostit/&lt;component&gt;/</span>, plus a{" "}
      <span className="mono">.yml.example</span> next to it. Upgrading from a
      hostit before the split? The three packages replace the old single{" "}
      <span className="mono">hostit</span> package, and your data and config are
      left alone.
    </p>
    <h3>3. btrfs for the app homes (required)</h3>
    <p>
      Snapshots, rollback, fork and hard disk quotas need{" "}
      <span className="mono">apps-dir</span> (default{" "}
      <span className="mono">/var/lib/hostit/apps</span>) on a btrfs filesystem,
      and hostit refuses to start without it. Put it on a btrfs mount before
      starting; a loopback file works for a small host, and the example Ansible
      role does this for you.
    </p>
    <h3>4. Configure and start</h3>
    <p>
      Edit <span className="mono">/etc/hostit/control/control.yml</span>. Only
      two keys are required: your <span className="mono">base-domain</span> and
      an <span className="mono">admin-token</span> for the REST API. The two
      lines below put the proxy in front, which is what terminates TLS.
    </p>
    <Snippet
      text={`base-domain: apps.example.com
admin-token: $(openssl rand -hex 24)
listen-http: 127.0.0.1:2910`}
    />
    <p>
      And point the proxy at it in{" "}
      <span className="mono">/etc/hostit/proxy/proxy.yml</span> -- one line,
      since a proxy on control's own host gets everything else by default:
    </p>
    <Snippet text={`control-url: http://127.0.0.1:2910`} />
    <Snippet
      text={`sudo systemctl enable --now hostit-control hostit-proxy\njournalctl -u hostit-control -f`}
    />
    <p>
      That is a working token-only server: create apps over the API or SSH. TLS
      is issued per subdomain on demand by Let's Encrypt. To get the web
      dashboard and Google login, and (recommended) wildcard TLS, see{" "}
      <strong>Configuration</strong>.
    </p>
  </>
);

const ConfigPage = () => (
  <>
    <h2>Configuration</h2>
    <p>
      The control plane's settings live in{" "}
      <span className="mono">/etc/hostit/control/control.yml</span>. Only{" "}
      <span className="mono">base-domain</span> and{" "}
      <span className="mono">admin-token</span> are required; the rest has sane
      defaults. Restart after editing (
      <span className="mono">systemctl restart hostit-control</span>).
    </p>
    <div className="table-wrap">
      <table className="docs-table">
        <tbody>
          <tr>
            <td className="mono">base-domain</td>
            <td>
              Apps are served at{" "}
              <span className="mono">&lt;app&gt;.&lt;base-domain&gt;</span>.
              Required.
            </td>
          </tr>
          <tr>
            <td className="mono">admin-token</td>
            <td>
              Bearer token for the admin REST API (full access). Required.{" "}
              <span className="mono">openssl rand -hex 24</span>.
            </td>
          </tr>
          <tr>
            <td className="mono">tls</td>
            <td>
              <span className="mono">letsencrypt</span> (default; on-demand
              per-subdomain certs) or <span className="mono">off</span> (plain
              HTTP, for development or behind another TLS proxy).
            </td>
          </tr>
          <tr>
            <td className="mono">dns-provider</td>
            <td>
              Set to <span className="mono">route53</span> for one wildcard{" "}
              <span className="mono">*.&lt;base-domain&gt;</span> certificate
              via DNS-01, so a brand-new app serves TLS instantly (and works
              even when the host is not publicly reachable). AWS credentials
              fall back to the usual env vars.
            </td>
          </tr>
          <tr>
            <td className="mono">google-client-id / -secret</td>
            <td>
              Google OAuth for the web dashboard. Leave empty to run token-only
              with no web login. Redirect URI is{" "}
              <span className="mono">
                https://&lt;base-domain&gt;/auth/callback
              </span>
              .
            </td>
          </tr>
          <tr>
            <td className="mono">admin-emails</td>
            <td>
              Emails that become active admins on their first Google login
              (skipping approval).
            </td>
          </tr>
          <tr>
            <td className="mono">data-dir / apps-dir</td>
            <td>
              Registry DB + certs, and app home directories. Default{" "}
              <span className="mono">/var/lib/hostit</span> and{" "}
              <span className="mono">/var/lib/hostit/apps</span> (btrfs).
            </td>
          </tr>
          <tr>
            <td className="mono">listen-http / -https</td>
            <td>Listener addresses.</td>
          </tr>
          <tr>
            <td className="mono">port-min / port-max</td>
            <td>Loopback port range hostit assigns apps.</td>
          </tr>
          <tr>
            <td className="mono">anthropic-api-key</td>
            <td>
              Enables the built-in assistant on the metered Anthropic API. See
              Users and administration.
            </td>
          </tr>
          <tr>
            <td className="mono">claude-code-oauth-token</td>
            <td>
              Additionally offers the models of a Claude Pro/Max subscription,
              run in a sandbox. Its presence is the whole switch; there is no
              backend selector.
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p className="hint">
      A full annotated example ships as{" "}
      <span className="mono">control.yml.example</span> in the source repository
      (with <span className="mono">node.yml.example</span> and{" "}
      <span className="mono">proxy.yml.example</span> beside it).
    </p>
  </>
);

const DeploymentPage = () => (
  <>
    <h2>Deployment shapes</h2>
    <Figure
      src={clusterShot}
      alt="The cluster table on the admin page, showing each member's memory, disk and load"
      caption="Whatever the shape, the admin page lists every member with what it is carrying and how its machine is doing -- amber past 75%, red past 90%."
    />
    <p>
      hostit runs as three components -- control (the brain), a node (a machine
      that runs apps), and a proxy (TLS and routing). They can all share one
      host, or sit on separate ones, and the difference is configuration rather
      than a different install. Control is always the source of truth: it tells
      nodes and proxies what state they should be in, and they apply it. Members
      dial control; control never dials them, so a node needs no inbound port
      and works behind NAT.
    </p>

    <h3>One host</h3>
    <p>
      The default. Control does the machine work in-process, the proxy sits in
      front of it, and nothing is enrolled: the proxy reaches control over the
      /run/hostit socket. Two files, and neither is long.
    </p>
    <Snippet
      text={`# /etc/hostit/control/control.yml
base-domain: apps.example.com
admin-token: CHANGE-ME          # openssl rand -hex 24
listen-http: 127.0.0.1:2910`}
    />
    <Snippet
      text={`# /etc/hostit/proxy/proxy.yml
control-url: http://127.0.0.1:2910`}
    />
    <p>
      Everything else defaults: the proxy is <span className="mono">local</span>
      , dials <span className="mono">127.0.0.1:2930</span>, and reads the
      credentials control keeps under{" "}
      <span className="mono">/var/lib/hostit/control/ipc/</span>. Enable{" "}
      <span className="mono">hostit-control</span> and{" "}
      <span className="mono">hostit-proxy</span>; leave{" "}
      <span className="mono">hostit-node</span> disabled.
    </p>

    <h3>Three hosts, two of them nodes</h3>
    <p>
      Control and the proxy stay together; two machines run nothing but apps.
      Only DNS host 1 needs to be public. On control, naming a reachable{" "}
      <span className="mono">listen-cluster</span> is what lets members on other
      machines join. A node sharing control's host needs none of this -- it
      dials the socket at <span className="mono">/run/hostit/control/cluster.sock</span>{" "}
      and presents no certificate.
    </p>
    <Snippet
      text={`# /etc/hostit/control/control.yml  (host 1: 10.0.0.1)
base-domain: apps.example.com
admin-token: CHANGE-ME
listen-http: 127.0.0.1:2910
listen-cluster: 10.0.0.1:2930   # members on OTHER machines dial in here (mTLS)
cluster-cert-file: /etc/hostit/control/cluster.crt   # control's own cert (CN=control)
cluster-key-file: /etc/hostit/control/cluster.key
cluster-ca-cert-file: /etc/hostit/control/cluster-ca.crt`}
    />
    <p>
      hostit runs no CA. Issue each member a cert from your own cluster CA (any
      tooling; openssl below) -- possession of a CA-signed cert <em>is</em>
      membership, so there is no join command and no token. Do it once for the
      CA, once for control (CN=control), and once per node/proxy:
    </p>
    <Snippet
      text={`# The cluster CA (do this once; keep cluster-ca.key secret):
openssl ecparam -name prime256v1 -genkey -noout -out cluster-ca.key
openssl req -x509 -new -key cluster-ca.key -days 3650 -out cluster-ca.crt \\
  -subj "/CN=hostit-control-ca" -addext "basicConstraints=critical,CA:TRUE" \\
  -addext "keyUsage=critical,keyCertSign"

# A member cert -- CN is its id, OU is its role (control, node or proxy):
openssl ecparam -name prime256v1 -genkey -noout -out worker-1.key
openssl req -new -key worker-1.key -out worker-1.csr -subj "/CN=worker-1/OU=node" \\
  -addext "subjectAltName=DNS:worker-1" \\
  -addext "extendedKeyUsage=serverAuth,clientAuth" \\
  -addext "keyUsage=critical,digitalSignature"
openssl x509 -req -in worker-1.csr -CA cluster-ca.crt -CAkey cluster-ca.key \\
  -days 1825 -out worker-1.crt -copy_extensions copy`}
    />
    <p>
      Save each member's three files on that host (mode 0600, owned by root) and
      name them in its config:
    </p>
    <Snippet
      text={`# /etc/hostit/node/node.yml  (host 2: 10.0.0.2)
node-id: worker-1
control-url: 10.0.0.1:2930
cluster-cert-file: /etc/hostit/node/cluster.crt
cluster-key-file: /etc/hostit/node/cluster.key
cluster-ca-cert-file: /etc/hostit/node/cluster-ca.crt

# A remote node publishes app ports on a real interface: the proxy is on
# another machine and cannot reach loopback here.
apps-bind-address: 10.0.0.2

# And only the proxies may reach them. This list is what protects the app
# ports -- proxy-to-app traffic is plain HTTP.
apps-allowed-addresses:
  - 10.0.0.1`}
    />
    <p>
      Host 3 is the same with its own <span className="mono">node-id</span> and{" "}
      <span className="mono">apps-bind-address</span>. Firewall: hosts 2 and 3
      accept <span className="mono">10000-19999</span> from host 1 (and nothing
      on 2930, since they dial out); host 1 accepts{" "}
      <span className="mono">2930</span> from the nodes, plus 80 and 443 from
      the world.
    </p>
    <p>
      Confirm with <span className="mono">hostit-control node list</span> and{" "}
      <span className="mono">hostit-control proxy list</span>: a member that has
      connected shows a recent <em>last seen</em>.{" "}
      Membership is the certificate: a node self-registers when it first dials in.{" "}
      <span className="mono">node remove</span> drops its row, but a host still
      holding a valid cert re-registers on reconnect -- to retire one for good,
      decommission the host or rotate the CA.
    </p>

    <h3>A proxy of its own</h3>
    <p>
      A proxy enrolls exactly like a node -- issue it a cert with{" "}
      <span className="mono">OU=proxy</span> -- and names its three cluster files
      plus <span className="mono">proxy-id</span> and{" "}
      <span className="mono">cluster-url</span>. Worth knowing before you spread
      these across a network you do not control: control's{" "}
      <span className="mono">listen-http</span> and the proxy-to-app hop are
      both plain HTTP. mTLS covers the cluster connection -- the configuration
      and certificate traffic -- not app traffic.
    </p>
  </>
);

// Connections setup: the per-provider registration steps. This is the page an
// operator actually needs, because the hostit half is one YAML block and the
// provider half is a different console every time.
// Every provider page is COMPLETE on its own: the redirect URIs, the steps, the
// scopes, the config block and the gotchas. Nothing says "see above" -- an
// operator setting up Slack should never have to read the GitHub page.
const RedirectURIs = () => (
  <>
    <h3>Redirect URI</h3>
    <p>hostit builds the callback from the hostname you are browsing. On this one it is:</p>
    <Snippet text={`https://${host}/auth/callback`} />
    <p>
      Register <b>every hostname your users browse hostit at</b> -- most providers accept several,
      so one client can serve a staging and a production instance together. hostit always sends an
      explicit <span className="mono">redirect_uri</span>, so the provider matches the right one.
      Avoid wildcard matching where a provider offers it: it lets an authorization code be sent to
      any subdomain.
    </p>
    <p className="hint">
      This is the URL for the hostname in your address bar right now. If you reach hostit at more
      than one name, open the docs at each and register what it shows -- the value is not something
      to work out by hand, and a redirect URI that does not match exactly is the most common reason
      a consent screen refuses.
    </p>
  </>
);

// The config block each provider page ends with, so none of them has to point
// at another page to say where the credentials go.
const ProviderConfig = ({ name, note }) => (
  <>
    <h3>Where the credentials go</h3>
    <p>
      In <span className="mono">control.yml</span>, under <span className="mono">connections:</span>,
      keyed by provider name. Restart hostit-control to pick it up; the provider then appears in the
      <b> Add account</b> menu.
    </p>
    <Snippet text={`connections:\n  ${name}:\n    client-id: YOUR_CLIENT_ID\n    client-secret: YOUR_CLIENT_SECRET`} />
    {note}
    <p className="hint">
      A provider with no client configured is hidden from the UI rather than shown and broken. If
      it does not appear after a restart, the key name is the first thing to check -- it must be
      exactly <span className="mono">{name}</span>.
    </p>
  </>
);

const ConnectionsSetupPage = () => (
  <>
    <h2>Connections setup</h2>
    <p>
      Users attach accounts, credentials and MCP servers on their own <b>Connections</b> page, then
      grant them to individual apps. How much of that needs you depends on the kind:
    </p>
    <table className="docs-table">
      <thead>
        <tr><th>Kind</th><th>What you have to do</th></tr>
      </thead>
      <tbody>
        <tr>
          <td><b>Credentials</b></td>
          <td>
            <b>Nothing.</b> An IMAP mailbox, a CalDAV calendar, a database URL, an SSH key, any
            API key -- always offered, no client, no review.
          </td>
        </tr>
        <tr>
          <td><b>MCP servers</b></td>
          <td>
            <b>Nothing.</b> Users add them by URL and hostit works out the rest. See{" "}
            <DocsPageLink guide="admin" section="connections" sub="mcpsetup">MCP servers</DocsPageLink>{" "}
            for the one thing worth checking on a new instance.
          </td>
        </tr>
        <tr>
          <td><b>Accounts</b></td>
          <td>
            Register an OAuth client with each provider and put it in{" "}
            <span className="mono">control.yml</span>. One page each, below.
          </td>
        </tr>
      </tbody>
    </table>
    <p>
      There is no shared hostit client to inherit for an account provider: registering one, and
      getting it reviewed where that is required, is your own relationship with that vendor.
    </p>

    <h3>The providers</h3>
    <table className="docs-table">
      <thead>
        <tr><th>Provider</th><th>Effort</th><th>Notes</th></tr>
      </thead>
      <tbody>
        <tr>
          <td><DocsPageLink guide="admin" section="connections" sub="linear">Linear</DocsPageLink></td>
          <td>Minutes</td><td>The quickest of the set, and a good one to prove the flow with</td>
        </tr>
        <tr>
          <td><DocsPageLink guide="admin" section="connections" sub="github">GitHub</DocsPageLink></td>
          <td>Minutes</td><td>No review, no scope declaration</td>
        </tr>
        <tr>
          <td><DocsPageLink guide="admin" section="connections" sub="slack">Slack</DocsPageLink></td>
          <td>Minutes</td><td>Bot and personal variants, both never expire</td>
        </tr>
        <tr>
          <td><DocsPageLink guide="admin" section="connections" sub="discord">Discord</DocsPageLink></td>
          <td>Minutes</td><td>Reading a server&rsquo;s channels needs a bot token as well</td>
        </tr>
        <tr>
          <td><DocsPageLink guide="admin" section="connections" sub="jira">Jira</DocsPageLink></td>
          <td>Moderate</td><td>One callback URL per integration</td>
        </tr>
        <tr>
          <td><DocsPageLink guide="admin" section="connections" sub="hubspot">HubSpot</DocsPageLink></td>
          <td>Moderate</td><td>Needs a developer account</td>
        </tr>
        <tr>
          <td><DocsPageLink guide="admin" section="connections" sub="google">Google</DocsPageLink></td>
          <td>Hard</td><td>Verification; Gmail needs a paid annual security review</td>
        </tr>
        <tr>
          <td><DocsPageLink guide="admin" section="connections" sub="custom">Your own</DocsPageLink></td>
          <td>Minutes</td><td>Anything hostit does not ship, written in <span className="mono">control.yml</span></td>
        </tr>
      </tbody>
    </table>
    <p className="hint">
      Before registering anything, check whether a credential does the job. A Gmail app password
      over IMAP needs no review and never expires; the Google account route needs both.
    </p>
  </>
);

const GithubPage = () => (
  <>
    <h2>GitHub</h2>
    <p>
      Gives an app access to the user&rsquo;s repositories. No review and no scope declaration:
      GitHub takes the scopes at authorize time.
    </p>
    <RedirectURIs />
    <h3>Register the app</h3>
    <ol className="docs-steps">
      <li><span className="mono">github.com/settings/developers</span> &rarr; <b>OAuth Apps</b> &rarr; <b>New OAuth App</b>.</li>
      <li>Application name: anything. Homepage URL: your hostit URL.</li>
      <li><b>Authorization callback URL</b>: the first URL above. Then <b>Add callback URL</b> for each of the others -- GitHub takes up to ten, so one client can serve staging and production together.</li>
      <li><b>Register application</b>, then copy the <b>Client ID</b>.</li>
      <li><b>Generate a new client secret</b> and copy it -- GitHub shows it once.</li>
    </ol>
    <ProviderConfig
      name="github"
      note={
        <p className="hint">
          GitHub issues a token that does not expire and no refresh token, so hostit stores the
          access token itself. There is nothing to refresh and nothing to reconnect unless the user
          revokes it.
        </p>
      }
    />
  </>
);

const SlackPage = () => (
  <>
    <h2>Slack</h2>
    <p>
      Two providers, two ways to reach Slack. <b>Slack (bot)</b> puts a shared bot in a workspace:
      it reads and posts only in the channels it has been <b>invited</b> to. <b>Slack (personal)</b>{" "}
      acts as the person who connected it, reading the channels they are already in -- public and
      private -- with no bot to invite anywhere. Both can come from the same Slack app if it
      declares both sets of scopes, or from two separate apps.
    </p>
    <RedirectURIs />

    <h3>Slack (bot)</h3>
    <p>
      A shared bot in one workspace: read channels and their history, post messages, look up users.
    </p>
    <ol className="docs-steps">
      <li><span className="mono">api.slack.com/apps</span> &rarr; <b>Create New App</b> &rarr; <b>From scratch</b>. Pick a workspace.</li>
      <li><b>OAuth &amp; Permissions</b> &rarr; <b>Redirect URLs</b> &rarr; add each URL above &rarr; <b>Save URLs</b>.</li>
      <li>
        Same page, <b>Scopes</b> &rarr; <b>Bot Token Scopes</b> &rarr; add the four below.{" "}
        <b>Bot</b> token scopes, not User token scopes -- hostit stores the top-level{" "}
        <span className="mono">access_token</span> from{" "}
        <span className="mono">oauth.v2.access</span>, which is the bot token.
      </li>
      <li><b>Install to Workspace</b> and approve.</li>
      <li><b>Basic Information</b> &rarr; <b>App Credentials</b> &rarr; copy the <b>Client ID</b> and <b>Client Secret</b>.</li>
    </ol>
    <h3>Bot Token Scopes to add</h3>
    <Snippet text={`channels:read\nchannels:history\nchat:write\nusers:read`} />
    <p>
      These are exactly what hostit requests. A mismatch fails consent with an invalid-scope error.
      The bot sees nothing until it is invited to a channel, the same way Discord&rsquo;s bot sees
      nothing until it joins a server.
    </p>
    <ProviderConfig
      name="slack-bot"
      note={
        <p className="hint">
          Slack issues a bot token that does not expire and no refresh token, so hostit stores the
          access token itself and there is nothing to refresh.
        </p>
      }
    />

    <h3>Slack (personal)</h3>
    <p>
      Acts <b>as the person</b> who connected it, using a Slack <b>user</b> token
      (<span className="mono">xoxp-</span>). It reads the channels they are already in and can search
      across them, so there is no bot to invite. What it may read is chosen by the owner at connect
      time -- the <b>Add account</b> dialog offers <b>Public channels</b>, <b>Private channels</b>{" "}
      and <b>Search across channels</b>, all on by default, plus a baseline lookup of users so ids
      resolve to names. There are deliberately no direct-message scopes.
    </p>
    <ol className="docs-steps">
      <li><span className="mono">api.slack.com/apps</span> &rarr; the same app as the bot, or a new one <b>From scratch</b>.</li>
      <li><b>OAuth &amp; Permissions</b> &rarr; <b>Redirect URLs</b> &rarr; add each URL above (same as the bot) &rarr; <b>Save URLs</b>.</li>
      <li>
        Same page, <b>Scopes</b> &rarr; <b>User Token Scopes</b> (not Bot Token Scopes) &rarr; add
        the six below.
      </li>
      <li><b>Basic Information</b> &rarr; <b>App Credentials</b> &rarr; copy the <b>Client ID</b> and <b>Client Secret</b>.</li>
    </ol>
    <h3>User Token Scopes to add</h3>
    <Snippet text={`search:read\nchannels:read\nchannels:history\ngroups:read\ngroups:history\nusers:read`} />
    <p>
      These cover every checkbox the dialog can offer. hostit requests only the ones the owner
      actually ticked, so declaring all six here does not force all of them on a given connection.
    </p>
    <ProviderConfig
      name="slack-user"
      note={
        <p className="hint">
          Slack issues a user token from <span className="mono">authed_user.access_token</span> in
          the <span className="mono">oauth.v2.access</span> response (not the top-level bot token).
          Like the bot token it does not expire and has no refresh token, so hostit stores it as-is.
        </p>
      }
    />
  </>
);

const DiscordPage = () => (
  <>
    <h2>Discord</h2>
    <p>
      Gives an app the user&rsquo;s Discord profile and the list of servers they are in. It does{" "}
      <b>not</b> read a server&rsquo;s channels or messages -- that needs a bot token, which is a
      credential rather than an account. Most useful setups want both.
    </p>
    <RedirectURIs />
    <h3>Register the application</h3>
    <ol className="docs-steps">
      <li><span className="mono">discord.com/developers/applications</span> &rarr; <b>New Application</b>. Name it.</li>
      <li><b>OAuth2</b> &rarr; <b>Redirects</b> &rarr; add each URL above &rarr; <b>Save Changes</b>.</li>
      <li>Same page, copy the <b>Client ID</b>, then <b>Reset Secret</b> to get a <b>Client Secret</b>.</li>
    </ol>
    <h3>Scopes</h3>
    <Snippet text={`identify\nguilds`} />
    <h3>The bot half</h3>
    <p>
      To read channels and messages, add a bot to the same application and give users its token as
      a <b>Discord bot</b> credential:
    </p>
    <ol className="docs-steps">
      <li><b>Bot</b> tab &rarr; <b>Add Bot</b> &rarr; <b>Reset Token</b> and copy it.</li>
      <li>Enable <b>Message Content Intent</b> if the app needs message bodies.</li>
      <li><b>OAuth2</b> &rarr; <b>URL Generator</b> &rarr; scope <span className="mono">bot</span>, permissions <b>View Channels</b> and <b>Read Message History</b>. Open the generated URL and invite the bot to the server.</li>
      <li>The bot token is pasted as a credential, not configured here.</li>
    </ol>
    <p className="hint">
      A bot in zero servers can see nothing. If an app reports no channels, the invite step is
      almost always what was missed.
    </p>
    <ProviderConfig
      name="discord"
      note={
        <p className="hint">
          Discord rotates its refresh token on every use. hostit stores the replacement each time,
          so this is handled -- but it is why a Discord connection restored from an old database
          backup will fail with <span className="mono">invalid_grant</span> and need reconnecting.
        </p>
      }
    />
  </>
);

const LinearPage = () => (
  <>
    <h2>Linear</h2>
    <p>Issues, projects and teams. The quickest provider to set up, and a good one to prove the flow with.</p>
    <RedirectURIs />
    <h3>Register the application</h3>
    <ol className="docs-steps">
      <li><span className="mono">linear.app/settings/api</span> &rarr; <b>OAuth applications</b> &rarr; <b>Create new</b>.</li>
      <li>Name it, and set <b>Callback URLs</b> to the URLs above, one per line.</li>
      <li>Create, then copy the <b>Client ID</b> and <b>Client Secret</b>.</li>
    </ol>
    <h3>Scopes</h3>
    <Snippet text={`read\nwrite`} />
    <ProviderConfig name="linear" />
  </>
);

const JiraPage = () => (
  <>
    <h2>Jira (Atlassian)</h2>
    <p>Issues and projects on an Atlassian site.</p>
    <RedirectURIs />
    <p className="hint">
      Atlassian takes <b>one</b> callback URL per integration, unlike the others. A staging and a
      production instance therefore need separate integrations, each with its own client.
    </p>
    <h3>Register the integration</h3>
    <ol className="docs-steps">
      <li><span className="mono">developer.atlassian.com</span> &rarr; <b>Console</b> &rarr; <b>Create</b> &rarr; <b>OAuth 2.0 integration</b>.</li>
      <li><b>Permissions</b> &rarr; add the <b>Jira API</b> &rarr; <b>Configure</b> &rarr; add the scopes below.</li>
      <li><b>Authorization</b> &rarr; <b>OAuth 2.0 (3LO)</b> &rarr; <b>Configure</b> &rarr; set the callback URL to whichever hostname this instance is browsed at.</li>
      <li><b>Settings</b> &rarr; copy the <b>Client ID</b> and <b>Secret</b>.</li>
    </ol>
    <h3>Scopes</h3>
    <Snippet text={`read:jira-work\nread:jira-user\nwrite:jira-work\noffline_access`} />
    <p>
      <span className="mono">offline_access</span> is the one that matters:{" "}
      without it Atlassian issues no refresh token, and hostit refuses the connection rather than
      accepting one that works for an hour and then dies.
    </p>
    <ProviderConfig name="jira" />
  </>
);

const HubspotPage = () => (
  <>
    <h2>HubSpot</h2>
    <p>Contacts and deals on a HubSpot portal. Needs a <b>developer</b> account, which is free.</p>
    <RedirectURIs />
    <h3>Register the app</h3>
    <ol className="docs-steps">
      <li>Sign in to a developer account at <span className="mono">developers.hubspot.com</span> &rarr; <b>Create app</b>.</li>
      <li><b>Auth</b> tab &rarr; set the <b>Redirect URL</b> and add the scopes below.</li>
      <li>Copy the <b>Client ID</b> and <b>Client secret</b> from the same tab.</li>
      <li>Create a <b>test account</b> under Testing to try it against, so you are not experimenting on a real portal.</li>
    </ol>
    <h3>Scopes</h3>
    <Snippet text={`crm.objects.contacts.read\ncrm.objects.deals.read\noauth`} />
    <ProviderConfig name="hubspot" />
  </>
);

const GooglePage = () => (
  <>
    <h2>Google (Calendar and Gmail)</h2>
    <p>
      The most work of any provider, and the one most worth avoiding. Read this page before
      starting: for a personal instance a <b>credential</b> usually does the same job with none of
      it.
    </p>
    <p className="hint">
      <b>Consider CalDAV and IMAP instead.</b> A Google app password over IMAP reads the same
      mailbox with no review, no verification and no expiry. CalDAV does the same for calendars,
      and works with Fastmail and iCloud too. Both are credentials -- always available, nothing for
      you to register.
    </p>

    <h3>The client</h3>
    <p>
      There is nothing to register. Google connections reuse the same OAuth client as the web
      login: it is the same Google Cloud client, and scopes are requested per authorization rather
      than baked into the registration. An explicit entry in{" "}
      <span className="mono">connections:</span> wins if you want a separate one.
    </p>

    <h3>Enable the APIs</h3>
    <ol className="docs-steps">
      <li>
        <span className="mono">console.cloud.google.com</span> &rarr; <b>APIs &amp; Services</b>{" "}
        &rarr; <b>Enable APIs</b> &rarr; enable <b>Google Calendar API</b> and/or <b>Gmail API</b>{" "}
        in the project.
      </li>
      <li>
        Without this a perfectly valid token still gets{" "}
        <span className="mono">403 SERVICE_DISABLED</span>. It is the single most common cause of a
        Google connection that authorizes cleanly and then does nothing.
      </li>
    </ol>

    <h3>Test users, while unverified</h3>
    <ol className="docs-steps">
      <li><b>OAuth consent screen</b> &rarr; <b>Audience</b> &rarr; add every account that will connect under <b>Test users</b>.</li>
      <li>
        Those users see a &ldquo;Google hasn&rsquo;t verified this app&rdquo; screen:{" "}
        <b>Advanced</b> &rarr; <b>Go to&hellip;</b>. Expected until verification.
      </li>
      <li>
        An account that is not a test user gets <span className="mono">Error 403: access_denied</span>{" "}
        and cannot proceed at all.
      </li>
    </ol>
    <p className="hint">
      While the app is unverified, Google <b>expires refresh tokens after seven days</b>. Every
      connection needs reconnecting weekly. This is the single biggest reason to prefer CalDAV or
      IMAP.
    </p>

    <h3>Verification, if you publish</h3>
    <table className="docs-table">
      <thead>
        <tr><th>Scope tier</th><th>Example</th><th>What it costs</th></tr>
      </thead>
      <tbody>
        <tr><td>Non-sensitive</td><td>email, profile</td><td>Nothing</td></tr>
        <tr><td>Sensitive</td><td><b>Calendar</b></td><td>Verification, but <b>free</b> and no security assessment</td></tr>
        <tr><td>Restricted</td><td><b>Gmail</b></td><td>Verification <b>and</b> a CASA Tier 2 assessment, roughly $800&ndash;$6,000 per year</td></tr>
      </tbody>
    </table>
    <p>
      So Calendar is worth verifying and removes the seven-day expiry for free. Gmail is dominated
      by the IMAP credential for anything short of a commercial product.
    </p>

    <h3>Scopes hostit requests</h3>
    <Snippet
      text={`# Google Calendar\nhttps://www.googleapis.com/auth/calendar\n\n# Gmail (read-only)\nhttps://www.googleapis.com/auth/gmail.readonly`}
    />
  </>
);

const CustomProviderPage = () => (
  <>
    <h2>Your own provider</h2>
    <p>
      Any OAuth 2.0 service hostit does not ship can be added in{" "}
      <span className="mono">control.yml</span>, and behaves exactly like a built-in.
    </p>
    <p>
      That works because a catalog entry was always <b>pure data</b>. There is no per-provider code
      anywhere, and the vendor quirks that look like special cases -- Google&rsquo;s{" "}
      <span className="mono">access_type=offline</span>, Atlassian&rsquo;s audience,
      Slack&rsquo;s token that never expires -- are all fields.
    </p>

    <h3>Register a client with the service</h3>
    <ol className="docs-steps">
      <li>Create an OAuth application with whatever the service calls its developer settings.</li>
      <li>
        Set its redirect URI(s) to the URLs below, and copy the client ID and secret it gives you.
      </li>
    </ol>
    <RedirectURIs />

    <h3>Write the entry</h3>
    <p>
      Setting <span className="mono">label</span> is what marks an entry as describing a provider
      rather than just holding a client for one hostit already knows:
    </p>
    <Snippet
      text={`connections:\n  acme:\n    label: Acme                      # required: what a person reads\n    client-id: YOUR_CLIENT_ID\n    client-secret: YOUR_SECRET\n    scopes: [read, write]\n    auth-url: https://acme.example.com/oauth/authorize\n    token-url: https://acme.example.com/oauth/token\n    help: Your Acme workspace.       # optional, shown in the dialog\n    name-hint: Acme                  # optional, suggested connection name`}
    />

    <h3>Or let hostit find the endpoints</h3>
    <p>
      If the service publishes OAuth authorization-server metadata, give an{" "}
      <span className="mono">issuer</span> instead of the two URLs and hostit will ask it where its
      endpoints are:
    </p>
    <Snippet
      text={`connections:\n  acme:\n    label: Acme\n    client-id: YOUR_CLIENT_ID\n    client-secret: YOUR_SECRET\n    scopes: [read, write]\n    issuer: https://acme.example.com`}
    />
    <p>
      Verified working against the real metadata of{" "}
      <span className="mono">https://github.com/login/oauth</span>,{" "}
      <span className="mono">https://accounts.google.com</span> and{" "}
      <span className="mono">https://gitlab.com</span>, none of which need their endpoints written
      out.
    </p>

    <h3>The awkward vendors</h3>
    <table className="docs-table">
      <thead>
        <tr><th>Field</th><th>What it is for</th></tr>
      </thead>
      <tbody>
        <tr>
          <td><span className="mono">auth-params</span></td>
          <td>
            Extra query parameters this provider demands on the consent URL, as a map. Google needs{" "}
            <span className="mono">{`{access_type: offline}`}</span> or it never issues a refresh
            token.
          </td>
        </tr>
        <tr>
          <td><span className="mono">long-lived-token: true</span></td>
          <td>
            The provider issues a token that never expires and no refresh token, so hostit stores
            the access token itself. Slack and GitHub are like this. Without it, hostit refuses a
            connection that came back with no refresh token.
          </td>
        </tr>
      </tbody>
    </table>

    <h3>Or add it from the Admin page</h3>
    <p>
      <b>Admin</b> &rarr; <b>Connection providers</b> does the same thing without a restart or a
      deploy. The two are equivalent: an entry here and an entry in{" "}
      <span className="mono">control.yml</span> both become a provider everyone on this instance can
      connect, and the page lists both so you can see everything on offer. Only the database ones
      are editable there -- a <span className="mono">control.yml</span> entry lives outside the
      database and is marked as such.
    </p>
    <p>
      Use <span className="mono">control.yml</span> when the definition should be part of your
      deployment (in Ansible, in git, reproducible on a rebuild). Use the Admin page when you are
      trying something out.
    </p>

    <h3>Named MCP servers</h3>
    <p>
      The same two places also hold named MCP servers, so a user picks a name rather than
      remembering a URL. Purely a shortcut -- anyone can still paste any URL:
    </p>
    <Snippet
      text={`mcp-servers:\n  deepwiki:\n    label: DeepWiki\n    url: https://mcp.deepwiki.com/mcp\n    help: Ask questions about a GitHub repository`}
    />
    <p>
      These need no client and no secret. The name only has to be unique among MCP servers -- it
      never becomes a connection's provider, so it may safely be the same as an OAuth provider's.
    </p>

    <h3>Users can add their own</h3>
    <p>
      A user does not need you for this. Anyone can register an OAuth app with a service themselves
      and paste the client into hostit, and it is visible only to them -- see{" "}
      <DocsPageLink guide="user" section="connections" sub="own">Your own services</DocsPageLink> in
      the user guide. Define one here when it should be available to <i>everybody</i>, or when you
      want one shared client rather than one per person.
    </p>
    <p className="hint">
      A user cannot redefine a name hostit ships or you have defined, so adding a provider here
      takes that name for the whole instance.
    </p>

    <h3>Rules</h3>
    <ul>
      <li>The key must be lowercase letters, digits and dashes.</li>
      <li>
        It cannot be a name hostit already ships -- an entry named{" "}
        <span className="mono">github</span> would silently change what every existing GitHub
        connection means, so it is refused.
      </li>
      <li>
        A malformed entry <b>stops the server at start</b> rather than vanishing from a menu. The
        log names the field.
      </li>
      <li>Your own providers are marked <b>yours</b> in the Add menu.</li>
    </ul>
    <p className="hint">
      With an <span className="mono">issuer</span>, the endpoints are resolved the first time the
      provider is used rather than at startup, so a network blip during a restart does not silently
      drop it. If discovery fails the provider is not offered, and the log says why.
    </p>
  </>
);

const McpSetupPage = () => (
  <>
    <h2>MCP servers</h2>
    <p>
      <b>Nothing to configure.</b> An MCP server needs no OAuth client from you, and users add them
      themselves by URL. Where account providers require you to register hostit with each vendor in
      advance, an MCP server is discovered at the moment somebody pastes its address.
    </p>

    <h3>How hostit introduces itself</h3>
    <p>hostit asks the server what it needs, and then identifies itself in one of three ways:</p>
    <ol className="docs-steps">
      <li>
        <b>A Client ID Metadata Document.</b> hostit&rsquo;s client_id is a URL the authorization
        server fetches to learn who is asking. This replaced dynamic registration in the MCP spec,
        and is the usual case.
      </li>
      <li>
        <b>Dynamic registration</b> (RFC 7591), for a server predating that. Done once and
        remembered.
      </li>
      <li>
        <b>Neither</b> -- the server only knows clients registered by hand. hostit refuses at add
        time and names the authorization server, so the user knows what would have to be
        registered. GitHub&rsquo;s MCP endpoint is currently one of these.
      </li>
    </ol>

    <h3>The one thing to check</h3>
    <p>The metadata document is served here, publicly and unauthenticated:</p>
    <Snippet text={`${origin}/.well-known/oauth-client`} />
    <p>
      It must be reachable from the internet, or an authorization server cannot fetch it and{" "}
      <b>every MCP consent fails</b> -- at the provider, where nobody can see why. If you put hostit
      behind an authenticating proxy, this path has to stay open. It is hostit&rsquo;s public
      identity, not a secret.
    </p>
    <p>Check it from outside:</p>
    <Snippet text={`curl -sS ${origin}/.well-known/oauth-client`} />

    <h3>Outbound requests are restricted</h3>
    <p>
      Users supply the URL, and hostit fetches it from inside your network. So hostit refuses to
      connect to anything that is not publicly routable: loopback, the private ranges, and the
      link-local range where every cloud provider puts its unauthenticated metadata service. The
      check runs when the connection is made, on the address actually being dialled, so a hostname
      that resolves public once and private a moment later does not get past it.
    </p>
    <p>
      If your MCP servers really are on your own LAN -- a Home Assistant at{" "}
      <span className="mono">192.168.1.50</span>, say -- turn it off deliberately:
    </p>
    <Snippet text={`outbound-allow-private: true`} />
    <p className="hint">
      Off by default, and worth leaving off on any instance with users you do not personally trust:
      it is the same setting that stands between an ordinary account and your metadata service.
    </p>

    <h3>Security notes</h3>
    <ul>
      <li>
        hostit is a <b>public client</b> here: it holds no secret for a server it has never met, so
        PKCE stands in.
      </li>
      <li>
        Every token is bound to one server with a resource indicator (RFC 8707), so it cannot be
        replayed against another.
      </li>
      <li>
        hostit never hands an MCP token to an app. An MCP token opens the whole server, so the app
        sends a tool name and hostit makes the call.
      </li>
      <li>
        A server that issues a client <b>secret</b> during registration is refused: hostit has
        nowhere safe to keep a per-server secret.
      </li>
    </ul>
  </>
);

const AdminPage = () => (
  <>
    <h2>Users and administration</h2>
    <Figure
      src={adminShot}
      alt="The admin page: users, roles and limits"
      caption="The Admin page: approve users, set roles and per-user limits, and configure the assistant."
    />
    <p>
      Admins get an <a href="/admin">Admin</a> link in the dashboard. An admin
      is anyone whose email is in <span className="mono">admin-emails</span>, or
      whom another admin has promoted.
    </p>
    <h3>Approving people</h3>
    <p>
      A new Google sign-in lands as <em>pending</em> and cannot create apps
      until an admin approves it, so an open Google login does not mean open
      signups. Two ways to skip the wait: list a whole email domain under
      allowed domains (anyone at <span className="mono">@yourco.com</span> is
      approved automatically), or invite a specific person, which pre-approves
      their email before they ever sign in.
    </p>
    <h3>Roles and limits</h3>
    <p>
      Each person is a <strong>user</strong> or an <strong>admin</strong>, and
      carries limits: how many apps they may create, and the memory and disk
      each of their apps gets. Set them per person on the Admin page, or set the
      instance defaults that every new account inherits. Disk is a hard cap
      covering everything the app writes (home, installed packages, snapshots
      combined): a write past it fails inside the container with "Disk quota
      exceeded". A disk limit of 0 means the platform default (2 GB); nothing is
      unlimited.
    </p>
    <h3>The built-in assistant</h3>
    <p>
      The in-browser chat is off until the server has an AI key. Set{" "}
      <span className="mono">anthropic-api-key</span> (pay per token) for the
      API models, and/or a Claude Pro/Max subscription token (
      <span className="mono">claude-code-oauth-token</span> from{" "}
      <span className="mono">claude setup-token</span>) to additionally offer
      that subscription's models, run in a locked-down podman sandbox. Each
      credential's presence is the whole switch.
    </p>
    <p>
      There is no model list to maintain: the picker offers exactly what the
      configured credentials can serve, so adding a key adds its models and
      removing one takes them away. Every user who can sign in can use the
      assistant -- an instance approves its signups, and that is the control.
      What people spend is bounded per user by the AI budget on this page, and{" "}
      <span className="mono">Usage</span> shows it per owner.
    </p>
    <h3>Backups</h3>
    <p>
      All durable state is two things: the SQLite registry under{" "}
      <span className="mono">data-dir</span> (accounts, apps, tokens, domains,
      snapshot metadata) and the app files under{" "}
      <span className="mono">apps-dir</span>. Back up both. hostit already
      snapshots each app periodically, but those snapshots live on the same disk and
      are not a substitute for an off-box backup.
    </p>
    <Snippet
      text={`sqlite3 /var/lib/hostit/control/hostit.db ".backup /backup/hostit.db"   # hot registry copy`}
    />
    <p className="hint">
      hostit runs as root because it creates Unix users and drives podman,
      systemd, nftables and btrfs. Keep the host locked down; app containers are
      isolated, but the daemon is the trusted control plane.
    </p>
  </>
);

const TroubleshootingPage = () => (
  <>
    <h2>Troubleshooting</h2>
    <p>
      When something looks wrong, you can inspect every piece of an app's state
      directly on the host. Run these as root. The one thing to know first:
      durable resources (container, unit, subvolume, snapshots) are keyed by an
      app's stable <strong>id</strong>, not its name, so start by mapping the
      name to its id.
    </p>

    <h3>Is the daemon healthy?</h3>
    <Snippet
      text={`systemctl status hostit-control hostit-proxy
journalctl -u hostit-control -f              # follow the control plane's log
journalctl -u hostit-control -n 200 --no-pager   # recent history (preflight errors land here)
journalctl -u hostit-node -f                 # on a machine that only runs apps`}
    />
    <p>
      On startup hostit preflights root, the required binaries (podman, nft,
      btrfs) and that <span className="mono">apps-dir</span> is btrfs. A failed
      preflight is one clear line at the top of the log.
    </p>

    <h3>Map an app name to its id</h3>
    <p>The registry knows both. Query it read-only:</p>
    <Snippet
      text={`sqlite3 /var/lib/hostit/control/hostit.db "select id, name, port from apps;"`}
    />

    <h3>The app's container (podman)</h3>
    <p>
      Containers are named <span className="mono">hostit-app-&lt;id&gt;</span>.
    </p>
    <Snippet
      text={`podman ps -a --filter name=hostit-app-        # every app container and its status
podman logs hostit-app-<id>                   # container-level output
podman exec -it hostit-app-<id> bash          # a shell inside, as root
podman inspect hostit-app-<id>                # full config (mounts, uid map, env)
podman stats --no-stream                      # live CPU/memory per container`}
    />

    <h3>The app's systemd unit</h3>
    <p>
      Each app runs as a template unit instance{" "}
      <span className="mono">hostit-app@&lt;id&gt;.service</span>.
    </p>
    <Snippet
      text={`systemctl list-units 'hostit-app@*'           # all app units at a glance
systemctl status 'hostit-app@<id>.service'
journalctl -u 'hostit-app@<id>.service' -n 100 --no-pager`}
    />

    <h3>The app process (inside the container)</h3>
    <p>
      The in-container agent (PID 1) writes a breadcrumb and the app's output
      into the app's files at{" "}
      <span className="mono">/var/lib/hostit/node/apps/&lt;id&gt;/home/app/</span>{" "}
      (the <span className="mono">/home/app</span> inside the app's subvolume):
    </p>
    <Snippet
      text={`cat  /var/lib/hostit/node/apps/<id>/home/app/log/state      # running | stopped | crashed | failed | idle
tail -f /var/lib/hostit/node/apps/<id>/home/app/log/app.log # the run: command's output`}
    />
    <p className="hint">
      A <span className="mono">failed</span> state means the command
      crash-looped and hostit gave up restarting it (the UI shows a red "App
      crashed"). The reason is a <span className="mono">[hostit]</span> line at
      the end of <span className="mono">app.log</span>. Fix the app, then{" "}
      <span className="mono">hostit deploy</span> or{" "}
      <span className="mono">hostit start</span> to clear it.
    </p>

    <h3>Storage and snapshots (btrfs)</h3>
    <p>
      Each app is ONE subvolume at{" "}
      <span className="mono">/var/lib/hostit/node/apps/&lt;id&gt;</span>: the entire
      root filesystem its container runs, snapshotted from the read-only base of
      its image tag under <span className="mono">.bases/&lt;tag&gt;</span>, with
      the app's files at <span className="mono">home/app</span> inside it. It is
      never recreated, which is why installed packages survive container
      recreates. Snapshots under{" "}
      <span className="mono">.snapshots/&lt;id&gt;/</span> are whole-app copies
      (files and installed software), so rollback restores both. An app's disk
      budget is the qgroup <span className="mono">1/&lt;uid&gt;</span> spanning
      its subvolume and snapshots, capped on exclusive bytes -- a "Disk quota
      exceeded" inside an app means it hit that budget.
    </p>
    <Snippet
      text={`btrfs subvolume list /var/lib/hostit/node/apps          # every app/base/snapshot subvolume
ls /var/lib/hostit/node/apps/                           # one subvolume per app, by id (plus .bases, .snapshots)
ls /var/lib/hostit/node/apps/<id>/home/app/             # the app's files inside its subvolume
ls /var/lib/hostit/node/apps/.bases/                    # read-only base rootfs, one per image tag
ls /var/lib/hostit/node/apps/.snapshots/<id>/           # an app's whole-app snapshots, by id
btrfs qgroup show -re /var/lib/hostit/apps         # disk budgets: 1/<uid> rows, exclusive bytes vs cap`}
    />

    <h3>Port rules (nftables)</h3>
    <p>
      hostit keeps one table, <span className="mono">inet hostit</span>, that
      pins each app's loopback port to its owner's uid.
    </p>
    <Snippet text={`nft list table inet hostit`} />

    <h3>The registry (SQLite)</h3>
    <p>
      All non-file state is one SQLite database. Inspect it read-only while the
      daemon runs:
    </p>
    <Snippet
      text={`sqlite3 -readonly /var/lib/hostit/hostit.db
  .tables
  select id, name, owner_email, port from apps;
  select email, role, status from users;`}
    />
    <p className="hint">
      TLS certificates are under{" "}
      <span className="mono">/var/lib/hostit/control/certs</span>; the app-side CLI
      socket is <span className="mono">/run/hostit/hostit.sock</span>. When in
      doubt, <span className="mono">systemctl restart hostit-control</span> is
      safe: running app containers keep serving, the proxy serves from its
      cached routing table while control is away, and everything reconciles on
      startup.
    </p>
  </>
);

// ---- Navigation ---------------------------------------------------------------

// Two guides, each served as one long page at its own route (/docs/user, /docs/admin).
// Within a guide the section links are in-page anchors; the other guide's links point
// at its route. /docs (bare) is the user guide.
//
// The section list itself lives in ../docs, so that other pages can link to a
// section without hand-writing a URL. This file supplies the bodies.
// Renderers, keyed by GUIDE and then by page id.
//
// This used to be one flat map, which silently broke: both guides have a page
// called "connections", so the second entry won and the user guide rendered the
// admin's setup instructions. A duplicate key in an object literal is not an
// error in JavaScript, so nothing complained -- nesting by guide makes the
// collision impossible rather than merely unlikely.
const renderers = {
  user: {
    intro: IntroPage,
    apps: AppsPage,
    assistant: AssistantPage,
    aiapps: AiAppsPage,
    files: FilesPage,
    ssh: SSHPage,
    snapshots: SnapshotsPage,
    domains: DomainsPage,
    limits: LimitsPage,
    visibility: VisibilityPage,
    connections: ConnectionsPage,
    accounts: AccountsPage,
    credentials: CredentialsPage,
    mcp: McpPage,
    own: OwnServicesPage,
    using: UsingPage,
    api: ApiPage,
  },
  admin: {
    install: InstallPage,
    config: ConfigPage,
    deployment: DeploymentPage,
    connections: ConnectionsSetupPage,
    google: GooglePage,
    github: GithubPage,
    slack: SlackPage,
    discord: DiscordPage,
    linear: LinearPage,
    jira: JiraPage,
    hubspot: HubspotPage,
    custom: CustomProviderPage,
    mcpsetup: McpSetupPage,
    admin: AdminPage,
    troubleshooting: TroubleshootingPage,
  },
};

// One page per route. The guides used to render as a single enormous scroll
// with the sidebar acting as a scrollspy; that stopped being tenable once a
// section wanted real depth, which is what "a complete page per provider"
// needs.
const Docs = () => {
  const [path, setPath] = useState(() => window.location.pathname);

  // The docs live outside the SPA router, so back/forward is ours to handle.
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname);
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const { guide, page, parent } = useMemo(
    () => findDocsPage(path, window.location.hash),
    [path],
  );
  const Body = renderers[guide.key]?.[page.id];

  useEffect(() => {
    window.scrollTo(0, 0);
  }, [path]);

  // Intercept clicks on docs links so moving between pages does not reload the
  // whole app. Anything else -- a new tab, an external link -- behaves normally.
  const navigate = (e, href) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    window.history.pushState({}, "", href);
    setPath(href);
  };

  return (
    <div className="docs-shell">
      <nav className="docs-nav" aria-label="Documentation">
        <a className="docs-nav-brand" href="/docs/user" onClick={(e) => navigate(e, "/docs/user")}>
          <Wordmark />
        </a>
        {DOCS_GUIDES.map((g) => (
          <div className="docs-nav-group" key={g.key}>
            <div className="docs-nav-title">{g.title}</div>
            <ul>
              {docsPages(g.key)
                // A sub-page is listed only while its parent is the one being
                // read. Every provider of every guide at once would be a wall
                // of links rather than a table of contents.
                .filter(
                  (p) =>
                    p.depth === 0 ||
                    (g.key === guide.key && (p.parentID === page.id || p.parentID === parent?.id)),
                )
                .map((p) => {
                  const active = g.key === guide.key && p.id === page.id;
                  return (
                    <li key={p.id} className={p.depth ? "docs-nav-sub" : undefined}>
                      <a
                        href={p.href}
                        className={active ? "active" : ""}
                        aria-current={active ? "page" : undefined}
                        onClick={(e) => navigate(e, p.href)}
                      >
                        {p.title}
                      </a>
                    </li>
                  );
                })}
            </ul>
          </div>
        ))}
        <a className="docs-nav-back" href="/">
          &larr; Back to the dashboard
        </a>
      </nav>

      <article className="docs docs-main">
        <section className="docs-section">{Body ? <Body /> : <p>Nothing here.</p>}</section>
        <DocsPager guide={guide} page={page} onNavigate={navigate} />
      </article>
    </div>
  );
};

// Previous/next across the flattened guide, so a reader can work through it
// without going back to the sidebar between every page.
const DocsPager = ({ guide, page, onNavigate }) => {
  const all = docsPages(guide.key);
  const i = all.findIndex((p) => p.id === page.id);
  const prev = i > 0 ? all[i - 1] : null;
  const next = i >= 0 && i < all.length - 1 ? all[i + 1] : null;
  if (!prev && !next) return null;
  return (
    <div className="docs-pager">
      {prev ? (
        <a href={prev.href} onClick={(e) => onNavigate(e, prev.href)}>&larr; {prev.title}</a>
      ) : (
        <span />
      )}
      {next && <a href={next.href} onClick={(e) => onNavigate(e, next.href)}>{next.title} &rarr;</a>}
    </div>
  );
};

export default Docs;
