// Retry helper with exponential backoff and jitter.
export async function retry(fn, { attempts = 3, baseMs = 100 } = {}) {
  let lastError;
  for (let i = 0; i < attempts; i++) {
    try {
      return await fn();
    } catch (err) {
      lastError = err;
      const jitter = Math.random() * baseMs;
      const delay = baseMs * 2 ** i + jitter;
      await new Promise((r) => setTimeout(r, delay));
    }
  }
  throw lastError;
}
