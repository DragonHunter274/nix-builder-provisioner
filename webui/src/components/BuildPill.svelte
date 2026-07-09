<script lang="ts">
  let { status, text }: { status: number; text: string } = $props();

  function bg(s: number): string {
    if (s <= 2 || s === 13) return '#d1fae5';
    if (s === 8) return '#fef3c7';
    return '#fee2e2';
  }
  function fg(s: number): string {
    if (s <= 2 || s === 13) return '#065f46';
    if (s === 8) return '#92400e';
    return '#991b1b';
  }
</script>

<span class="pill" style:background={bg(status)} style:color={fg(status)}>{text}</span>

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
