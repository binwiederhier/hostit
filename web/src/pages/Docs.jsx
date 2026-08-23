import { useEffect, useMemo, useState } from "react";
import { Snippet, Wordmark } from "../components";
import { DOCS_GUIDES } from "../docs";
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
const baseDomain = host.replace(/^[^.]*\./, "");

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
        </tbody>
      </table>
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
            <td className="mono">listen-http / -https / -api</td>
            <td>
              Listener addresses. <span className="mono">listen-api</span> is an
              optional extra plain-HTTP admin listener, e.g.{" "}
              <span className="mono">127.0.0.1:2900</span>.
            </td>
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
      front of it, and nothing is enrolled: control mints the proxy's
      credentials itself. Two files, and neither is long.
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
      <span className="mono">/var/lib/hostit/ipc/</span>. Enable{" "}
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
      dials the socket at <span className="mono">/run/hostit/cluster.sock</span>{" "}
      and presents no certificate.
    </p>
    <Snippet
      text={`# /etc/hostit/control/control.yml  (host 1: 10.0.0.1)
base-domain: apps.example.com
admin-token: CHANGE-ME
listen-http: 127.0.0.1:2910
listen-cluster: 10.0.0.1:2930   # members on OTHER machines dial in here`}
    />
    <p>
      Enroll each node from the control host. There is no join protocol and no
      token:
    </p>
    <Snippet
      text={`hostit-control node add --address 10.0.0.2 worker-1
hostit-control node add --address 10.0.0.3 worker-2`}
    />
    <p>
      Each prints three PEM blocks -- the node's certificate, its key, and the
      cluster CA. Save them on that node (mode 0600, owned by root) and name
      them in its config:
    </p>
    <Snippet
      text={`# /etc/hostit/node/node.yml  (host 2: 10.0.0.2)
node-id: worker-1
control-url: 10.0.0.1:2930
node-cert-file: /etc/hostit/node/node.pem
node-key-file: /etc/hostit/node/node.key
cluster-ca-cert-file: /etc/hostit/node/cluster-ca.pem

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
      <span className="mono">node remove</span> and{" "}
      <span className="mono">proxy remove</span> revoke a member -- the registry
      row, not the certificate, is what membership hangs on.
    </p>

    <h3>A proxy of its own</h3>
    <p>
      A proxy enrolls exactly like a node (
      <span className="mono">hostit-control proxy add edge-1</span>), and names
      the credentials it printed plus <span className="mono">proxy-id</span> and{" "}
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
const ConnectionsSetupPage = () => (
  <>
    <h2>Connections setup</h2>
    <p>
      Users connect accounts and paste credentials in their profile, then grant them to
      individual apps. <b>Credentials</b> -- an IMAP mailbox, a CalDAV calendar, a database
      URL, any API key -- need nothing from you: they are always offered.
    </p>
    <p>
      <b>Connections</b> are OAuth accounts, and each provider needs a client <i>you</i>{" "}
      register with that provider. There is no shared hostit client to inherit, because
      getting one reviewed is your own relationship with them. A provider with no client
      configured is hidden from the UI rather than shown and broken.
    </p>

    <h3>The redirect URI</h3>
    <p>
      Every provider asks for a callback URL. hostit builds it from whichever hostname the
      user is browsing, and two serve the web app:
    </p>
    <Snippet
      text={`https://${baseDomain}/auth/callback\nhttps://${host}/auth/callback`}
    />
    <p>
      Register <b>every hostname hostit is browsed at</b>. All of these providers accept
      several -- GitHub takes up to ten via <i>Add callback URL</i> -- so one client can
      serve a staging and a production instance together. hostit always sends an explicit{" "}
      <span className="mono">redirect_uri</span>, so the provider matches the right one.
      Avoid wildcard matching where a provider offers it: it lets an authorization code be
      sent to any subdomain.
    </p>

    <h3>Where to register each one</h3>
    <table className="docs-table">
      <thead>
        <tr><th>Provider</th><th>Console</th><th>Steps</th></tr>
      </thead>
      <tbody>
        <tr>
          <td><b>GitHub</b></td>
          <td className="mono">github.com/settings/developers</td>
          <td>
            OAuth Apps &rarr; New OAuth App. Set the Authorization callback URL. Copy the
            Client ID, then Generate a new client secret. No review.
          </td>
        </tr>
        <tr>
          <td><b>Slack</b></td>
          <td className="mono">api.slack.com/apps</td>
          <td>
            Create New App &rarr; From scratch. Under <i>OAuth &amp; Permissions</i> add the
            redirect URL and the <b>Bot Token Scopes</b> listed below, then Install to
            Workspace. Credentials are under <i>Basic Information</i>.
          </td>
        </tr>
        <tr>
          <td><b>Discord</b></td>
          <td className="mono">discord.com/developers/applications</td>
          <td>
            New Application &rarr; <i>OAuth2</i>. Add the redirect under <i>Redirects</i> and
            Save. Copy the Client ID; use <i>Reset Secret</i> to reveal a Client Secret.
          </td>
        </tr>
        <tr>
          <td><b>Linear</b></td>
          <td className="mono">linear.app/settings/api</td>
          <td>
            OAuth applications &rarr; Create. Set the callback URL. The cleanest of the set
            and a good one to try first.
          </td>
        </tr>
        <tr>
          <td><b>Jira</b></td>
          <td className="mono">developer.atlassian.com</td>
          <td>
            Console &rarr; Create &rarr; OAuth 2.0 integration. Add the <i>Jira API</i>{" "}
            permission and its scopes, then set the callback under <i>Authorization</i>.
          </td>
        </tr>
        <tr>
          <td><b>HubSpot</b></td>
          <td className="mono">developers.hubspot.com</td>
          <td>
            Create an app in a developer account. Set the redirect and scopes under{" "}
            <i>Auth</i>. Needs a test portal to try against.
          </td>
        </tr>
        <tr>
          <td><b>Google</b><br />(Calendar, Gmail)</td>
          <td className="mono">console.cloud.google.com</td>
          <td>
            Uses the same client as web login, so nothing extra is needed if login works.
            You must <b>enable the API</b> per product (Calendar, Gmail) in the project, and
            add each account as a <i>test user</i> while the consent screen is in Testing.
          </td>
        </tr>
      </tbody>
    </table>

    <h3>Scopes to request</h3>
    <p>
      Where the provider asks you to declare scopes up front, use these -- they are what
      hostit requests, and a mismatch fails consent with an invalid-scope error.
    </p>
    <table className="docs-table">
      <thead><tr><th>Provider</th><th>Scopes</th></tr></thead>
      <tbody>
        <tr><td>GitHub</td><td className="mono">repo, read:user</td></tr>
        <tr><td>Slack</td><td className="mono">channels:read, channels:history, chat:write, users:read</td></tr>
        <tr><td>Discord</td><td className="mono">identify, guilds</td></tr>
        <tr><td>Linear</td><td className="mono">read</td></tr>
        <tr><td>Jira</td><td className="mono">read:jira-work, read:jira-user, offline_access</td></tr>
        <tr><td>HubSpot</td><td className="mono">oauth, crm.objects.contacts.read, crm.objects.companies.read, crm.objects.deals.read</td></tr>
        <tr><td>Google Calendar</td><td className="mono">calendar.readonly</td></tr>
        <tr><td>Gmail</td><td className="mono">gmail.readonly</td></tr>
      </tbody>
    </table>

    <h3>Configure hostit</h3>
    <p>
      Put the clients in <span className="mono">control.yml</span> under{" "}
      <span className="mono">connections:</span>, keyed by provider name. List only what you
      have; the rest stay hidden.
    </p>
    <Snippet
      text={`connections:\n  github:\n    client-id: YOUR_GITHUB_CLIENT_ID\n    client-secret: YOUR_SECRET\n  slack:\n    client-id: YOUR_SLACK_CLIENT_ID\n    client-secret: YOUR_SECRET\n  discord:\n    client-id: YOUR_DISCORD_CLIENT_ID\n    client-secret: YOUR_SECRET`}
    />
    <p>
      Restart <span className="mono">hostit-control</span> to pick them up. Anything you
      configured now appears in each user&rsquo;s Profile under <b>Connections</b>.
    </p>

    <h3>A note on Google</h3>
    <p>
      Google is the most expensive provider to offer and the only one that gates on review.
      Calendar is a <i>sensitive</i> scope (free verification); Gmail is <i>restricted</i>{" "}
      (verification plus an annual paid CASA assessment). While the consent screen is in{" "}
      <b>Testing</b>, only accounts you add as test users can connect, and Google{" "}
      <b>expires refresh tokens after seven days</b>, so every connection needs reconnecting
      weekly.
    </p>
    <p>
      For personal instances there is a way around it entirely: the <b>IMAP</b> credential
      reaches Gmail at <span className="mono">imap.gmail.com:993</span> with a Google app
      password, and <b>CalDAV</b> reaches Fastmail, iCloud or Nextcloud calendars the same
      way. No OAuth client, no review, and nothing that expires.
    </p>
  </>
);

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
      text={`sqlite3 /var/lib/hostit/hostit.db ".backup /backup/hostit.db"   # hot registry copy`}
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
      text={`sqlite3 /var/lib/hostit/hostit.db "select id, name, port from apps;"`}
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
      <span className="mono">/var/lib/hostit/apps/&lt;id&gt;/home/app/</span>{" "}
      (the <span className="mono">/home/app</span> inside the app's subvolume):
    </p>
    <Snippet
      text={`cat  /var/lib/hostit/apps/<id>/home/app/log/state      # running | stopped | crashed | failed | idle
tail -f /var/lib/hostit/apps/<id>/home/app/log/app.log # the run: command's output`}
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
      <span className="mono">/var/lib/hostit/apps/&lt;id&gt;</span>: the entire
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
      text={`btrfs subvolume list /var/lib/hostit/apps          # every app/base/snapshot subvolume
ls /var/lib/hostit/apps/                           # one subvolume per app, by id (plus .bases, .snapshots)
ls /var/lib/hostit/apps/<id>/home/app/             # the app's files inside its subvolume
ls /var/lib/hostit/apps/.bases/                    # read-only base rootfs, one per image tag
ls /var/lib/hostit/apps/.snapshots/<id>/           # an app's whole-app snapshots, by id
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
      <span className="mono">/var/lib/hostit/certs</span>; the app-side CLI
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
const renderers = {
  intro: IntroPage,
  apps: AppsPage,
  assistant: AssistantPage,
  files: FilesPage,
  ssh: SSHPage,
  snapshots: SnapshotsPage,
  domains: DomainsPage,
  limits: LimitsPage,
  visibility: VisibilityPage,
  api: ApiPage,
  install: InstallPage,
  config: ConfigPage,
  deployment: DeploymentPage,
  connections: ConnectionsSetupPage,
  admin: AdminPage,
  troubleshooting: TroubleshootingPage,
};

const guides = DOCS_GUIDES.map((guide) => ({
  ...guide,
  items: guide.items.map((item) => ({ ...item, render: renderers[item.id] })),
}));

const Docs = () => {
  const currentKey = window.location.pathname.startsWith("/docs/admin")
    ? "admin"
    : "user";
  const current = useMemo(
    () => guides.find((g) => g.key === currentKey),
    [currentKey],
  );
  const [activeId, setActiveId] = useState(
    () => window.location.hash.slice(1) || current.items[0].id,
  );

  // Clicking a sidebar anchor updates the hash; reflect it in the active link at once.
  useEffect(() => {
    const onHash = () => {
      const id = window.location.hash.slice(1);
      if (id) setActiveId(id);
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  // Scrollspy: the sidebar tracks which section is in view as you scroll.
  useEffect(() => {
    const sections = current.items
      .map((it) => document.getElementById(it.id))
      .filter(Boolean);
    if (!sections.length) return undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        const inView = entries
          .filter((e) => e.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        if (inView[0]) setActiveId(inView[0].target.id);
      },
      { rootMargin: "0px 0px -70% 0px", threshold: 0 },
    );
    sections.forEach((s) => observer.observe(s));
    return () => observer.disconnect();
  }, [current]);

  // Client-rendered SPA: the browser cannot scroll to the initial #hash because the
  // section did not exist at parse time, so do it once after mount.
  useEffect(() => {
    const id = window.location.hash.slice(1);
    const el = id && document.getElementById(id);
    if (el) el.scrollIntoView();
    else window.scrollTo(0, 0);
  }, []);

  return (
    <div className="docs-shell">
      <nav className="docs-nav" aria-label="Documentation">
        <a className="docs-nav-brand" href="/docs/user">
          <Wordmark />
        </a>
        {guides.map((g) => {
          const isCurrent = g.key === currentKey;
          return (
            <div className="docs-nav-group" key={g.key}>
              <div className="docs-nav-title">{g.title}</div>
              <ul>
                {g.items.map((it) => {
                  const active = isCurrent && it.id === activeId;
                  return (
                    <li key={it.id}>
                      <a
                        href={isCurrent ? `#${it.id}` : `${g.path}#${it.id}`}
                        className={active ? "active" : ""}
                        aria-current={active ? "location" : undefined}
                      >
                        {it.title}
                      </a>
                    </li>
                  );
                })}
              </ul>
            </div>
          );
        })}
        <a className="docs-nav-back" href="/">
          &larr; Back to the dashboard
        </a>
      </nav>

      <article className="docs docs-main">
        {current.items.map((it) => {
          const Body = it.render;
          return (
            <section id={it.id} key={it.id} className="docs-section">
              <Body />
            </section>
          );
        })}
      </article>
    </div>
  );
};

export default Docs;
