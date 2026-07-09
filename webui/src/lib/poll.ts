export function startPoll(fn: () => Promise<void>, intervalMs = 3000): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout>;

  async function tick() {
    if (stopped) return;
    try { await fn(); } catch { /* errors surfaced by fn */ }
    if (!stopped) timer = setTimeout(tick, intervalMs);
  }

  tick();
  return () => { stopped = true; clearTimeout(timer); };
}
