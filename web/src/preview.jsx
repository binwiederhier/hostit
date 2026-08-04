import { createRoot } from "react-dom/client";
import App from "./App";
import "./styles.css";

const json = (body, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

const app = {
  name: "blog",
  url: "https://blog.apps.example.com",
  port: 10042,
  disk_mb: 12,
  over_quota: false,
  owner_email: "hr@example.com",
  created_at: "2026-07-30T10:00:00Z",
  ssh: { user: "blog", host: "apps.example.com", command: "ssh blog@apps.example.com" },
};

window.fetch = (path, opts) => {
  const method = (opts && opts.method) || "GET";
  if (path === "/v1/account") {
    return json({
      email: "hr@example.com",
      name: "HR",
      role: "user",
      status: "active",
      limits: { app_limit: 5, memory_mb: 512, disk_mb: 512 },
      usage: { apps: 1, disk_mb: 12 },
    });
  }
  if (path === "/v1/apps") {
    return json([app]);
  }
  if (path === "/v1/apps/blog") {
    return json(app);
  }
  if (path === "/v1/apps/nope") {
    return json({ error: "app not found" }, 404);
  }
  if (path === "/v1/account/tokens" && method === "POST") {
    return json({
      id: "t1",
      prefix: "ho_abc123",
      label: "agent: blog",
      app_name: "blog",
      token: "ho_abc123def456ghi789jkl012mno345pqr678stu",
    });
  }
  return json({ error: "not mocked: " + method + " " + path }, 404);
};

createRoot(document.getElementById("root")).render(<App />);
