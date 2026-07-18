// In-memory auth token for the web UI. When the server is started with a token
// (required for non-loopback binds), it gates /api/* and /ws. The token arrives
// via ?token=... on first load; we keep it in memory only — never localStorage,
// per the frontend security constraints — and strip it from the visible URL so
// it is not bookmarked or shown.

let token = '';

(function init() {
  try {
    const params = new URLSearchParams(location.search);
    const t = params.get('token');
    if (t) {
      token = t;
      // Remove ?token= from the address bar without reloading.
      params.delete('token');
      const qs = params.toString();
      const clean = location.pathname + (qs ? `?${qs}` : '') + location.hash;
      history.replaceState(null, '', clean);
    }
  } catch {
    // location/history unavailable (non-browser context) — no token.
  }
})();

export function getToken(): string {
  return token;
}

// authHeaders returns the Authorization header when a token is set, for use with
// fetch(). Empty object otherwise (loopback zero-config).
export function authHeaders(): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

// withToken appends ?token= to a URL for consumers that cannot set headers: the
// <img> element and the WebSocket handshake.
export function withToken(url: string): string {
  if (!token) return url;
  const sep = url.includes('?') ? '&' : '?';
  return `${url}${sep}token=${encodeURIComponent(token)}`;
}
