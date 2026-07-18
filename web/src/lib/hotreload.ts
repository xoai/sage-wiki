import { withToken } from './token';

// Hot reload WebSocket client with auto-reconnect.
export function connectHotReload(onReload: () => void): () => void {
  let ws: WebSocket | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let stopped = false;

  function connect() {
    if (stopped) return;

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    // Browser WebSocket can't set headers, so carry the token as a query param.
    ws = new WebSocket(withToken(`${proto}//${location.host}/ws`));

    ws.onmessage = (e) => {
      if (e.data === 'reload') {
        onReload();
      }
    };

    ws.onclose = () => {
      ws = null;
      if (!stopped) {
        reconnectTimer = setTimeout(connect, 3000);
      }
    };

    ws.onerror = () => {
      ws?.close();
    };
  }

  connect();

  // Return cleanup function
  return () => {
    stopped = true;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    ws?.close();
  };
}
