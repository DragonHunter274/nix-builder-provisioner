export function fmtDuration(secs: number): string {
  if (secs <= 0) return '—';
  if (secs < 60) return `${secs}s`;
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  if (m < 60) return s > 0 ? `${m}m ${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const rm = m % 60;
  return rm > 0 ? `${h}h ${rm}m` : `${h}h`;
}

export function fmtAgo(unixSecs: number): string {
  if (!unixSecs) return '—';
  const delta = Math.floor((Date.now() / 1000) - unixSecs);
  if (delta < 60) return `${delta}s ago`;
  if (delta < 3600) return `${Math.floor(delta / 60)}m ago`;
  if (delta < 86400) return `${Math.floor(delta / 3600)}h ago`;
  return `${Math.floor(delta / 86400)}d ago`;
}

export function fmtAgeSecs(secs: number): string {
  return fmtDuration(secs);
}

export function truncPath(p: string, maxLen = 50): string {
  if (!p || p.length <= maxLen) return p || '—';
  return '…' + p.slice(p.length - (maxLen - 1));
}
