<script lang="ts">
  import { api } from '../api/client';
  import type { StoreSummary } from '../api/types';
  import { startPoll } from '../lib/poll';
  import { fmtDuration } from '../lib/fmt';

  let stats = $state<StoreSummary | null>(null);
  let error = $state<string | null>(null);

  $effect(() => startPoll(async () => {
    try {
      stats = await api.stats();
      error = null;
    } catch (e) {
      error = String(e);
    }
  }, 10000));

  const successRate = $derived(
    stats && stats.totalBuilds > 0
      ? Math.round((stats.successfulBuilds / stats.totalBuilds) * 100)
      : 0
  );
</script>

<h2>Statistics</h2>

{#if error}
  <div role="alert" class="err">API error: {error}</div>
{:else if !stats}
  <div class="loading">loading…</div>
{:else}
  <div class="cards">
    <div class="card">
      <div class="card-value">{stats.totalBuilds.toLocaleString()}</div>
      <div class="card-label">total builds</div>
    </div>
    <div class="card accent-green">
      <div class="card-value">{stats.successfulBuilds.toLocaleString()}</div>
      <div class="card-label">successful</div>
    </div>
    <div class="card accent-red">
      <div class="card-value">{stats.failedBuilds.toLocaleString()}</div>
      <div class="card-label">failed</div>
    </div>
    <div class="card">
      <div class="card-value">{successRate}%</div>
      <div class="card-label">success rate</div>
    </div>
  </div>

  <div class="timing">
    <h3>Build timing</h3>
    <dl>
      <dt>Average duration</dt>
      <dd>{fmtDuration(Math.round(stats.avgDurationSecs))}</dd>
      <dt>P90 duration</dt>
      <dd>{fmtDuration(Math.round(stats.p90DurationSecs))}</dd>
    </dl>
  </div>
{/if}

<style>
  h2 { margin: 0 0 1.5rem; font-size: 1.25rem; }
  h3 { margin: 0 0 0.75rem; font-size: 1rem; }
  .err { background: #fee2e2; color: #991b1b; padding: 0.75rem 1rem; border-radius: 6px; }
  .loading { color: #6b7280; }
  .cards { display: flex; gap: 1rem; margin-bottom: 2rem; flex-wrap: wrap; }
  .card {
    background: #fff; border: 1px solid #e5e7eb;
    border-radius: 8px; padding: 1rem 1.5rem;
    min-width: 8rem; text-align: center;
  }
  .card-value { font-size: 2rem; font-weight: 700; color: #111827; }
  .card-label { font-size: 0.75rem; color: #6b7280; margin-top: 0.25rem; }
  .accent-green .card-value { color: #065f46; }
  .accent-red .card-value { color: #991b1b; }
  .timing {
    background: #fff; border: 1px solid #e5e7eb; border-radius: 8px; padding: 1.25rem 1.5rem;
    max-width: 24rem;
  }
  dl { margin: 0; display: grid; grid-template-columns: 1fr 1fr; gap: 0.5rem 1rem; }
  dt { color: #6b7280; }
  dd { margin: 0; font-weight: 600; }
</style>
