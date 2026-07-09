<script lang="ts">
  let { status }: { status: string } = $props();

  const META: Record<string, { bg: string; fg: string }> = {
    in_use:       { bg: '#fef3c7', fg: '#92400e' },
    pooled:       { bg: '#d1fae5', fg: '#065f46' },
    provisioning: { bg: '#dbeafe', fg: '#1e40af' },
    ready:        { bg: '#e0e7ff', fg: '#3730a3' },
    destroying:   { bg: '#fee2e2', fg: '#991b1b' },
  };
  const m = $derived(META[status] ?? { bg: '#f3f4f6', fg: '#6b7280' });
</script>

<span class="pill" style:background={m.bg} style:color={m.fg}>{status.replace('_', ' ')}</span>

<style>
  .pill {
    display: inline-block;
    padding: 0.1rem 0.5rem;
    border-radius: 9999px;
    font-size: 0.75rem;
    font-weight: 500;
    white-space: nowrap;
  }
</style>
