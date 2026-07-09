<script lang="ts">
  import { api } from '../api/client';
  import type { PoolStatus } from '../api/types';
  import StatusPill from '../components/StatusPill.svelte';
  import { startPoll } from '../lib/poll';
  import { fmtDuration, truncPath } from '../lib/fmt';

  let status = $state<PoolStatus | null>(null);
  let error = $state<string | null>(null);

  $effect(() => startPoll(async () => {
    try {
      status = await api.status();
      error = null;
    } catch (e) {
      error = String(e);
    }
  }));

  const counts = $derived.by(() => {
    if (!status) return { active: 0, pooled: 0, provisioning: 0, destroying: 0, total: 0, provisioningInFlight: 0 };
    const c = { active: 0, pooled: 0, provisioning: 0, destroying: 0 };
    for (const b of status.builders) {
      if (b.status === 'in_use') c.active++;
      else if (b.status === 'pooled') c.pooled++;
      else if (b.status === 'provisioning') c.provisioning++;
      else if (b.status === 'destroying') c.destroying++;
    }
    const totalProvisioning = Object.values(status.provisioningCount).reduce((a, b) => a + b, 0);
    return { ...c, total: status.builders.length, provisioningInFlight: totalProvisioning };
  });
</script>

<h2>Overview</h2>

{#if error}
  <div role="alert" class="err">API unreachable: {error}</div>
{:else if !status}
  <div class="loading">loading…</div>
{:else}
  <div class="cards">
    <div class="card">
      <div class="card-value">{counts.total}</div>
      <div class="card-label">total builders</div>
    </div>
    <div class="card accent-yellow">
      <div class="card-value">{counts.active}</div>
      <div class="card-label">active builds</div>
    </div>
    <div class="card accent-green">
      <div class="card-value">{counts.pooled}</div>
      <div class="card-label">pooled</div>
    </div>
    <div class="card accent-blue">
      <div class="card-value">{counts.provisioningInFlight}</div>
      <div class="card-label">provisioning</div>
    </div>
    <div class="card">
      <div class="card-value">{status.pendingRequests}</div>
      <div class="card-label">pending requests</div>
    </div>
  </div>

  {#if status.builders.length === 0}
    <p class="empty">No builders active.</p>
  {:else}
    <table>
      <thead>
        <tr>
          <th>ID</th>
          <th>Arch</th>
          <th>Status</th>
          <th>Derivation</th>
          <th>IP</th>
          <th>Age</th>
        </tr>
      </thead>
      <tbody>
        {#each status.builders as b (b.id)}
          <tr>
            <td><code>{b.id}</code></td>
            <td>{b.arch}</td>
            <td><StatusPill status={b.status} /></td>
            <td><code class="drv" title={b.drvPath}>{truncPath(b.drvPath ?? '')}</code></td>
            <td>{b.ip}</td>
            <td>{fmtDuration(b.ageSecs)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
{/if}

<style>
  h2 { margin: 0 0 1.5rem; font-size: 1.25rem; }
  .err {
    background: #fee2e2;
    color: #991b1b;
    padding: 0.75rem 1rem;
    border-radius: 6px;
    margin-bottom: 1rem;
  }
  .loading { color: #6b7280; }
  .cards {
    display: flex;
    gap: 1rem;
    margin-bottom: 2rem;
    flex-wrap: wrap;
  }
  .card {
    background: #fff;
    border: 1px solid #e5e7eb;
    border-radius: 8px;
    padding: 1rem 1.5rem;
    min-width: 8rem;
    text-align: center;
  }
  .card-value { font-size: 2rem; font-weight: 700; color: #111827; }
  .card-label { font-size: 0.75rem; color: #6b7280; margin-top: 0.25rem; }
  .accent-yellow .card-value { color: #92400e; }
  .accent-green .card-value { color: #065f46; }
  .accent-blue .card-value { color: #1e40af; }
  table { width: 100%; border-collapse: collapse; background: #fff; border: 1px solid #e5e7eb; border-radius: 8px; overflow: hidden; }
  th, td { text-align: left; padding: 0.5rem 0.75rem; border-bottom: 1px solid #e5e7eb; }
  th { font-weight: 600; font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; color: #6b7280; background: #f9fafb; }
  tbody tr:last-child td { border-bottom: none; }
  code { font-family: ui-monospace, monospace; font-size: 0.8rem; }
  .drv { color: #6b7280; }
  .empty { color: #6b7280; font-style: italic; }
</style>
