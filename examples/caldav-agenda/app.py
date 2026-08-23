"""The next seven days, merged across every CalDAV calendar this app was granted.

The part worth copying is read_connections()/read_token(): the credential is
fetched from the app's own unix socket at the moment it is needed, never stored
in a file or an environment variable. That is what makes revoking a grant take
effect immediately rather than at the next deploy.
"""

import datetime as dt
import html
import http.client
import json
import os
import socket
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import caldav

SOCKET = os.environ.get("HOSTIT_SOCKET", "/run/hostit/hostit.sock")
DAYS = 7


# ---- hostit: reading the credential ---------------------------------------


class UnixHTTPConnection(http.client.HTTPConnection):
    """http.client over a unix socket, which is how an app talks to hostit."""

    def __init__(self, path):
        super().__init__("localhost")
        self.socket_path = path

    def connect(self):
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(10)
        s.connect(self.socket_path)
        self.sock = s


def hostit_get(path):
    conn = UnixHTTPConnection(SOCKET)
    try:
        conn.request("GET", path)
        resp = conn.getresponse()
        body = resp.read()
        if resp.status != 200:
            raise RuntimeError(f"hostit {path} -> {resp.status}: {body.decode(errors='replace')[:200]}")
        return json.loads(body)
    finally:
        conn.close()


def parse_meta(meta):
    """hostit returns a static connection's non-secret fields as "k=v k=v"."""
    out = {}
    for part in (meta or "").split(" "):
        if "=" in part:
            k, v = part.split("=", 1)
            out[k] = v
    return out


def caldav_connections():
    """Every CalDAV connection this app holds, newest grant last."""
    return [c for c in hostit_get("/v1/connections") if c.get("provider") == "caldav"]


def read_credential(slug):
    """A usable credential for one connection. Asked for per request on purpose."""
    tok = hostit_get(f"/v1/connections/{slug}/token")
    meta = parse_meta(tok.get("meta"))
    return meta.get("url"), meta.get("username"), tok["access_token"]


# ---- Calendars ------------------------------------------------------------


def as_datetime(value):
    """CalDAV gives dates for all-day events and datetimes for the rest."""
    if isinstance(value, dt.datetime):
        return value if value.tzinfo else value.replace(tzinfo=dt.timezone.utc)
    if isinstance(value, dt.date):
        return dt.datetime(value.year, value.month, value.day, tzinfo=dt.timezone.utc)
    return None


def fetch_events(conn, start, end):
    """Events from one connection, tagged with which one they came from.

    Errors are returned rather than raised: one unreachable calendar should not
    blank the page for the others.
    """
    slug = conn["slug"]
    label = conn.get("label") or slug
    try:
        url, username, password = read_credential(slug)
        client = caldav.DAVClient(url=url, username=username, password=password)
        events = []
        for calendar in client.principal().calendars():
            try:
                found = calendar.search(start=start, end=end, event=True, expand=True)
            except Exception:
                # Not every server supports server-side expansion of recurrences.
                found = calendar.search(start=start, end=end, event=True)
            for item in found:
                for component in item.icalendar_instance.walk("VEVENT"):
                    begins = as_datetime(component.get("dtstart").dt) if component.get("dtstart") else None
                    if begins is None or not (start <= begins <= end):
                        continue
                    ends = as_datetime(component.get("dtend").dt) if component.get("dtend") else None
                    events.append({
                        "start": begins,
                        "end": ends,
                        "summary": str(component.get("summary") or "(no title)"),
                        "location": str(component.get("location") or ""),
                        "all_day": not isinstance(component.get("dtstart").dt, dt.datetime),
                        "source": label,
                        "slug": slug,
                    })
        return events, None
    except Exception as err:
        return [], f"{label}: {err}"


def group_by_day(events):
    """Events bucketed by local date, each bucket sorted by start time."""
    days = {}
    for e in sorted(events, key=lambda e: (e["start"], e["summary"])):
        days.setdefault(e["start"].date(), []).append(e)
    return days


# ---- Rendering ------------------------------------------------------------


PAGE = """<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Agenda</title>
<style>
  :root {{ color-scheme: light dark; --line: color-mix(in srgb, currentColor 15%, transparent); }}
  body {{ font: 16px/1.5 system-ui, sans-serif; max-width: 44rem; margin: 3rem auto; padding: 0 1.25rem; }}
  h1 {{ font-size: 1.5rem; margin-bottom: .25rem; }}
  .sub {{ opacity: .6; font-size: .875rem; margin-bottom: 2rem; }}
  h2 {{ font-size: .8rem; text-transform: uppercase; letter-spacing: .06em; opacity: .55;
        margin: 2rem 0 .5rem; padding-bottom: .35rem; border-bottom: 1px solid var(--line); }}
  .ev {{ display: flex; gap: .9rem; padding: .45rem 0; align-items: baseline; }}
  .time {{ font-variant-numeric: tabular-nums; opacity: .7; min-width: 5.5rem; font-size: .875rem; }}
  .what {{ flex: 1; min-width: 0; }}
  .where {{ opacity: .55; font-size: .8rem; }}
  .src {{ font-size: .7rem; opacity: .6; border: 1px solid var(--line); border-radius: 10px; padding: .05rem .5rem; }}
  .err {{ border-left: 3px solid #c0392b; padding: .6rem .9rem; margin: .5rem 0; font-size: .875rem; }}
  .empty {{ opacity: .6; }}
  code {{ background: var(--line); padding: .1rem .35rem; border-radius: 4px; }}
</style>
<h1>Next {days} days</h1>
<div class="sub">{sub}</div>
{errors}
{body}
"""


def render(days, sources, errors, generated):
    parts = []
    for day, events in sorted(days.items()):
        parts.append(f"<h2>{html.escape(day.strftime('%A %-d %B'))}</h2>")
        for e in events:
            when = "all day" if e["all_day"] else e["start"].strftime("%H:%M")
            where = f'<div class="where">{html.escape(e["location"])}</div>' if e["location"] else ""
            src = f'<span class="src">{html.escape(e["source"])}</span>' if len(sources) > 1 else ""
            parts.append(
                f'<div class="ev"><div class="time">{html.escape(when)}</div>'
                f'<div class="what">{html.escape(e["summary"])}{where}</div>{src}</div>'
            )
    if not parts:
        if not sources:
            parts.append(
                '<p class="empty">No CalDAV calendar is granted to this app yet. '
                "Add one under Profile &rarr; Credentials, then grant it on this app's "
                "Settings tab. Nothing needs redeploying.</p>"
            )
        else:
            parts.append('<p class="empty">Nothing scheduled.</p>')

    sub = f"{len(sources)} calendar{'s' if len(sources) != 1 else ''} &middot; generated {generated:%H:%M}"
    err_html = "".join(f'<div class="err">{html.escape(e)}</div>' for e in errors)
    return PAGE.format(days=DAYS, sub=sub, errors=err_html, body="".join(parts))


# ---- Server ---------------------------------------------------------------


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        if self.path.startswith("/healthz"):
            return self.send_body(b"ok", "text/plain")
        try:
            now = dt.datetime.now(dt.timezone.utc)
            conns = caldav_connections()
            events, errors = [], []
            for conn in conns:
                got, err = fetch_events(conn, now, now + dt.timedelta(days=DAYS))
                events.extend(got)
                if err:
                    errors.append(err)
            page = render(group_by_day(events), conns, errors, now.astimezone())
            self.send_body(page.encode(), "text/html; charset=utf-8")
        except Exception:
            traceback.print_exc()
            self.send_body(b"<h1>Agenda failed</h1><p>See the app log.</p>", "text/html", status=500)

    def send_body(self, body, content_type, status=200):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)


if __name__ == "__main__":
    port = int(os.environ.get("PORT", "8080"))
    print(f"agenda listening on :{port}, socket {SOCKET}", flush=True)
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
