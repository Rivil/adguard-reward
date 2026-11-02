<script lang="ts">
  import { iconUrl } from './api'

  // AdGuard serves an icon for built-in services only; a custom service
  // (icon "") shows a letter avatar from its name instead (locked custom_icon).
  let { name, icon, size = 20 }: { name: string; icon: string; size?: number } = $props()

  /** The uppercased first grapheme of the trimmed name, or "?" when blank. */
  function letterOf(n: string): string {
    const t = n.trim()
    if (t === '') return '?'
    let first: string | undefined
    if (typeof Intl !== 'undefined' && 'Segmenter' in Intl) {
      const seg = new Intl.Segmenter(undefined, { granularity: 'grapheme' })
      first = seg.segment(t)[Symbol.iterator]().next().value?.segment
    }
    return (first ?? Array.from(t)[0]).toUpperCase()
  }

  let letter = $derived(letterOf(name))
</script>

{#if icon}
  <img alt="" src={iconUrl(icon)} width={size} height={size} />
{:else}
  <span
    class="letter-avatar"
    aria-hidden="true"
    data-letter-avatar
    style:width="{size}px"
    style:height="{size}px"
    style:font-size="{Math.round(size * 0.6)}px">{letter}</span
  >
{/if}

<style>
  .letter-avatar {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    border-radius: 50%;
    background: var(--accent-bg);
    color: var(--accent);
    font-weight: 600;
    line-height: 1;
    overflow: hidden;
    user-select: none;
  }
</style>
