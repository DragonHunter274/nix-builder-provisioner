<script lang="ts">
  import { Link, Route, Router } from 'svelte-routing';
  import Overview from './pages/Overview.svelte';
  import Builds from './pages/Builds.svelte';
  import Stats from './pages/Stats.svelte';
</script>

<Router>
  <div class="shell">
    <nav>
      <h1>nix-builder</h1>
      <ul>
        <li><Link to="/">Overview</Link></li>
        <li><Link to="/builds">Builds</Link></li>
        <li><Link to="/stats">Stats</Link></li>
      </ul>
    </nav>
    <main>
      <Route path="/"><Overview /></Route>
      <Route path="/builds"><Builds /></Route>
      <Route path="/stats"><Stats /></Route>
    </main>
  </div>
</Router>

<style>
  :global(*, *::before, *::after) { box-sizing: border-box; }
  :global(body) {
    margin: 0;
    font-family: ui-sans-serif, system-ui, sans-serif;
    font-size: 0.875rem;
    background: #f9fafb;
    color: #111827;
  }
  :global(a) { color: #2563eb; text-decoration: none; }
  :global(a:hover) { text-decoration: underline; }

  .shell {
    display: flex;
    min-height: 100vh;
  }
  nav {
    width: 13rem;
    background: #1e293b;
    color: #e2e8f0;
    padding: 1.5rem 1rem;
    flex-shrink: 0;
  }
  h1 {
    margin: 0 0 1.5rem;
    font-size: 1rem;
    font-weight: 700;
    color: #f8fafc;
    letter-spacing: 0.02em;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  :global(nav a) {
    display: block;
    padding: 0.4rem 0.75rem;
    border-radius: 6px;
    color: #94a3b8;
    font-size: 0.875rem;
    transition: background 0.1s, color 0.1s;
  }
  :global(nav a:hover), :global(nav a.active) {
    background: #334155;
    color: #f1f5f9;
    text-decoration: none;
  }
  main {
    flex: 1;
    padding: 2rem;
    overflow-y: auto;
  }
</style>
