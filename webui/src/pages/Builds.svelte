<script lang="ts">
  import { api } from '../api/client';
  import type { BuildRow } from '../api/types';
  import BuildPill from '../components/BuildPill.svelte';
  import { startPoll } from '../lib/poll';
  import { fmtDuration, fmtAgo } from '../lib/fmt';

  const PAGE_SIZE = 50;
  let builds = $state<BuildRow[]>([]);
  let total = $state(0);
  let page = $state(0);
  let error = $state<string | null>(null);
  let expanded = $state<number | null>(null);

  // filter: '' | 'success' | 'failure'
  let filter = $state('');

  $effect(() => {
    const p = page;
    return startPoll(async () => {
      try {
        const r = await api.builds(PAGE_SIZE, p * PAGE_SIZE);
        builds = r.builds;
        total = r.total;
        error = null;
      } catch (e) {
        error = String(e);
      }
    }, 5000);
  });

  const filtered = $derived.by(() => {
    if (filter === 'success') return builds.filter(b => b.status <= 2 || b.status === 13);
    if (filter === 'failure') return builds.filter(b => b.status > 2 && b.status !== 13);
    return builds;
  });

  const lastPage = $derived(Math.max(0, Math.ceil(total / PAGE_SIZE) - 1));
</script>

<h2>Builds <span class="total">({total} total)</span></h2>

<div class="filters">
  {#each [['', 'all'], ['success', 'success'], ['failure', 'failure']] as [val, label] (val)}
    <button class:active={filter === val} onclick={() => filter = val}>{label}</button>
  {/each}
</div>

{#if error}
  <div role="alert" class="err">API error: {error}</div>
{:else}
  <table>
    <thead>
      <tr>
        <th>Package</th>
        <th>System</th>
        <th>Status</th>
        <th>Duration</th>
        <th>When</th>
        <th>Builder</th>
      </tr>
    </thead>
    <tbody>
      {#each filtered as b (b.id)}
        <tr
          role="button"
          tabindex="0"
          onclick={() => expanded = expanded === b.id ? null : b.id}
          onkeydown={e => e.key === 'Enter' && (expanded = expanded === b.id ? null : b.id)}
        >
          <td>
            <strong>{b.pname || '—'}</strong>
            {#if b.version}<span class="ver">{b.version}</span>{/if}
          </td>
          <td>{b.system || '—'}</td>
          <td><BuildPill status={b.status} text={b.statusText} /></td>
          <td>{fmtDuration(b.durationSecs)}</td>
          <td>{fmtAgo(b.startTime)}</td>
          <td><code>{b.builderId || '—'}</code></td>
        </tr>
        {#if expanded === b.id}
          <tr class="detail-row">
            <td colspan="6">
              <div class="detail">
                <div><span class="label">Drv:</span> <code>{b.drvPath}</code></div>
                {#if b.errorMsg}<div class="err-msg"><span class="label">Error:</span> {b.errorMsg}</div>{/if}
              </div>
            </td>
          </tr>
        {/if}
      {:else}
        <tr><td colspan="6" class="empty">no builds</td></tr>
      {/each}
    </tbody>
  </table>

  <div class="pager">
    <button disabled={page === 0} onclick={() => page--}>prev</button>
    <span>page {page + 1} / {lastPage + 1}</span>
    <button disabled={page >= lastPage} onclick={() => page++}>next</button>
  </div>
{/if}

<style>
  h2 { margin: 0 0 1rem; font-size: 1.25rem; }
  .total { font-size: 0.875rem; font-weight: 400; color: #6b7280; }
  .filters { display: flex; gap: 0.5rem; margin-bottom: 1rem; }
  .filters button {
    border: 1px solid #d1d5db;
    background: #fff;
    padding: 0.25rem 0.75rem;
    border-radius: 9999px;
    cursor: pointer;
    font-size: 0.875rem;
  }
  .filters button.active { background: #2563eb; border-color: #2563eb; color: #fff; }
  .err { background: #fee2e2; color: #991b1b; padding: 0.75rem 1rem; border-radius: 6px; margin-bottom: 1rem; }
  table { width: 100%; border-collapse: collapse; background: #fff; border: 1px solid #e5e7eb; border-radius: 8px; overflow: hidden; }
  th, td { text-align: left; padding: 0.5rem 0.75rem; border-bottom: 1px solid #e5e7eb; }
  th { font-weight: 600; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; color: #6b7280; background: #f9fafb; }
  tbody tr[role="button"] { cursor: pointer; }
  tbody tr[role="button"]:hover { background: #f9fafb; }
  .ver { color: #6b7280; font-size: 0.8rem; margin-left: 0.25rem; }
  code { font-family: ui-monospace, monospace; font-size: 0.8rem; }
  .detail-row td { padding: 0; }
  .detail {
    background: #f8fafc;
    border-top: 1px solid #e5e7eb;
    padding: 0.75rem 1rem;
    font-size: 0.8rem;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .detail code { word-break: break-all; }
  .label { color: #6b7280; font-weight: 500; }
  .err-msg { color: #991b1b; }
  .empty { text-align: center; color: #9ca3af; font-style: italic; }
  .pager { display: flex; justify-content: center; align-items: center; gap: 1rem; margin-top: 1rem; }
  .pager button {
    border: 1px solid #d1d5db; background: #fff;
    padding: 0.25rem 0.75rem; border-radius: 4px; cursor: pointer;
  }
  .pager button:disabled { opacity: 0.4; cursor: default; }
</style>
