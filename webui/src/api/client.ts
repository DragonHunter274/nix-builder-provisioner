import type { PoolStatus, BuildList, StoreSummary } from './types';

async function get<T>(path: string): Promise<T> {
  const r = await fetch(path);
  if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
  return r.json() as Promise<T>;
}

export const api = {
  status: () => get<PoolStatus>('/api/status'),
  builds: (limit = 50, offset = 0) =>
    get<BuildList>(`/api/builds?limit=${limit}&offset=${offset}`),
  stats: () => get<StoreSummary>('/api/stats'),
};
