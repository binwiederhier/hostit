// Thin fetch wrapper for the hostit API. All requests are same-origin and
// authenticated via the session cookie; errors surface as ApiError with the
// server's {"error": "..."} message when available.
export class ApiError extends Error {
  constructor(status, message, name) {
    super(message);
    this.name = name || "ApiError";
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

// getText/putRaw handle file bodies, which are not JSON: the file endpoints read
// and write raw bytes. Same-origin fetch carries the session cookie and the
// Sec-Fetch-Site header the CSRF gate needs.
const getText = async (path) => {
  let res;
  try {
    res = await fetch(path, { credentials: "same-origin" });
  } catch {
    throw new ApiError(0, "Network error, check your connection and try again");
  }
  const text = await res.text().catch(() => "");
  if (!res.ok) {
    let message = `Request failed (HTTP ${res.status})`;
    try {
      const data = JSON.parse(text);
      if (data && data.error) message = data.error;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, message);
  }
  return text;
};

const putRaw = async (path, body) => {
  let res;
  try {
    res = await fetch(path, {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/octet-stream" },
      body,
    });
  } catch {
    throw new ApiError(0, "Network error, check your connection and try again");
  }
  const text = await res.text().catch(() => "");
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }
  if (!res.ok) {
    throw new ApiError(res.status, data && data.error ? data.error : `Request failed (HTTP ${res.status})`);
  }
  return data;
};

// putRawProgress uploads a file with progress callbacks -- fetch cannot report
// upload progress, so this one uses XMLHttpRequest. onProgress gets a 0..1 fraction.
// An optional AbortSignal cancels the upload; the promise then rejects with an
// ApiError whose status is 0 and name is "AbortError".
const putRawProgress = (path, body, onProgress, signal) =>
  new Promise((resolve, reject) => {
    if (signal && signal.aborted) {
      reject(new ApiError(0, "Upload canceled", "AbortError"));
      return;
    }
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", path);
    xhr.withCredentials = true;
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    if (xhr.upload && onProgress) {
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) onProgress(e.loaded / e.total);
      };
    }
    if (signal) {
      signal.addEventListener("abort", () => xhr.abort(), { once: true });
    }
    xhr.onabort = () => reject(new ApiError(0, "Upload canceled", "AbortError"));
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(null);
        return;
      }
      let message = `Request failed (HTTP ${xhr.status})`;
      try {
        const data = JSON.parse(xhr.responseText);
        if (data && data.error) message = data.error;
      } catch {
        /* not JSON */
      }
      reject(new ApiError(xhr.status, message));
    };
    xhr.onerror = () => reject(new ApiError(0, "Network error, check your connection and try again"));
    xhr.send(body);
  });

export const api = {
  get: (path) => request("GET", path),
  post: (path, body) => request("POST", path, body),
  put: (path, body) => request("PUT", path, body),
  patch: (path, body) => request("PATCH", path, body),
  del: (path) => request("DELETE", path),
  getText,
  putRaw,
  putRawProgress,
  // getBlob reads a file as raw bytes (for previewing/moving binary files).
  getBlob: async (path) => {
    let res;
    try {
      res = await fetch(path, { credentials: "same-origin" });
    } catch {
      throw new ApiError(0, "Network error, check your connection and try again");
    }
    if (!res.ok) throw new ApiError(res.status, `Request failed (HTTP ${res.status})`);
    return res.blob();
  },
};
