import { authHeaders } from './token';

export interface QueryCallbacks {
  onToken: (text: string) => void;
  onSources: (paths: string[]) => void;
  onDone: () => void;
  onError: (error: string) => void;
}

export function streamQuery(question: string, topK: number, callbacks: QueryCallbacks): AbortController {
  const controller = new AbortController();
  let doneEmitted = false;

  fetch('/api/query', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ question, top_k: topK }),
    signal: controller.signal,
  }).then(async (response) => {
    if (!response.ok) {
      const text = await response.text();
      callbacks.onError(text || `HTTP ${response.status}`);
      return;
    }

    const reader = response.body?.getReader();
    if (!reader) {
      callbacks.onError('No response body');
      return;
    }

    const decoder = new TextDecoder();
    let buffer = '';
    let eventType = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() || '';

      for (const line of lines) {
        if (line.startsWith('event: ')) {
          eventType = line.slice(7).trim();
        } else if (line.startsWith('data: ')) {
          const data = line.slice(6);
          try {
            const parsed = JSON.parse(data);
            switch (eventType) {
              case 'token':
                callbacks.onToken(parsed.text);
                break;
              case 'sources':
                callbacks.onSources(parsed.paths || []);
                break;
              case 'error':
                callbacks.onError(parsed.error);
                break;
              case 'done':
                if (!doneEmitted) {
                  doneEmitted = true;
                  callbacks.onDone();
                }
                break;
            }
          } catch {
            // ignore malformed JSON
          }
          eventType = ''; // Reset after processing data
        } else if (line.trim() === '') {
          eventType = ''; // Reset on blank line (SSE event boundary)
        }
      }
    }

    // Ensure done is called if stream ended without explicit done event
    if (!doneEmitted) {
      callbacks.onDone();
    }
  }).catch((err) => {
    if (err.name !== 'AbortError') {
      callbacks.onError(err.message);
    }
  });

  return controller;
}
