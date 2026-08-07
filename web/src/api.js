// Thin fetch wrapper for the hostit API. All requests are same-origin and
// authenticated via the session cookie; errors surface as ApiError with the
// server's {"error": "..."} message when available.
export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

// isNetworkError reports a transient connectivity failure (fetch rejected, status
// 0) rather than a real HTTP error. Background polls swallow these instead of
// showing a sticky banner, since connectivity usually returns on its own.
export const isNetworkError = (err) => err instanceof ApiError && err.status === 0;

const request = async (method, path, body) => {
  let res;
  try {
    res = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new ApiError(0, "Network error, check your connection and try again");
  }
  let data = null;
  try {
    const text = await res.text();
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!res.ok) {
    const message = data && data.error ? data.error : `Request failed (HTTP ${res.status})`;
    throw new ApiError(res.status, message);
  }
  return data;
};

export const api = {
  get: (path) => request("GET", path),
  post: (path, body) => request("POST", path, body),
  patch: (path, body) => request("PATCH", path, body),
  del: (path) => request("DELETE", path),
};
