import { test, expect } from "./fixtures";

// An app asking a model a question from inside its own container. This is the
// path that lets an app be an AI app without holding an API key -- so the thing
// worth proving is that a process in the container gets an answer, and that no
// key ever reaches it.
test("an app asks the model a question over its own socket", async ({ request }) => {
  test.setTimeout(240000);
  const app = "e2eask" + Date.now().toString(36).slice(-5);
  const created = await request.post("/api/apps", { data: { name: app } });
  expect(created.status(), await created.text()).toBe(201);

  // Creating an app kicks off a background start; running a command the
  // instant the API answers 201 races that, and the loser fails with a podman
  // name collision. Wait for it to be up first.
  await expect
    .poll(async () => {
      const info = await request.get(`/api/apps/${app}`);
      return info.ok() ? (await info.json()).running : false;
    }, { timeout: 90000, intervals: [1000, 2000, 3000] })
    .toBe(true);

  const run = async (command) => {
    const res = await request.post(`/api/apps/${app}/run`, { data: { command, timeout_seconds: 90 } });
    expect(res.status(), await res.text()).toBeLessThan(300);
    return (await res.json()).output || "";
  };

  try {
    const ask = (body) =>
      run(`curl -sS --unix-socket /run/hostit/hostit.sock http://x/api/container/assistant -d '${body}'`);

    // A question with a persona, which is one of the two things this is for.
    const pirate = await ask('{"system":"Answer as a pirate in one short sentence.","prompt":"What is a subdomain?","max_tokens":80}');
    const answer = JSON.parse(pirate);
    expect(answer.text.length).toBeGreaterThan(0);
    expect(answer.model).toMatch(/claude/);
    expect(answer.usage.output_tokens).toBeGreaterThan(0);

    // The other: judging text and returning a decision the app can branch on.
    const verdict = JSON.parse(
      await ask('{"system":"You triage logs. Answer with exactly one word: WAKE or IGNORE.","prompt":"2026-01-01 FATAL database corruption, refusing writes","max_tokens":10}')
    );
    expect(verdict.text.trim()).toBe("WAKE");

    // The API key never reaches the container. This is the whole reason the
    // endpoint exists, so it is asserted rather than assumed.
    const env = await run("env | grep -ci anthropic || true");
    expect(env.trim()).toBe("0");
    expect(JSON.stringify(answer)).not.toContain("sk-ant");

    // A malformed ask is the app's mistake and says so, rather than a 500.
    const bad = await run(
      `curl -sS -o /dev/null -w '%{http_code}' --unix-socket /run/hostit/hostit.sock http://x/api/container/assistant -d '{}'`
    );
    expect(bad.trim()).toBe("400");
  } finally {
    await request.delete(`/api/apps/${app}`);
  }
});

// It is the APP's endpoint, resolved by the socket's peer credentials, so it is
// not on the public web at all.
test("the ask endpoint is not reachable over the public web", async ({ request }) => {
  for (const path of ["/api/container/assistant", "/v1/assistant"]) {
    const res = await request.post(path, { data: { prompt: "hi" } });
    expect(res.status(), path).toBe(404);
  }
});
