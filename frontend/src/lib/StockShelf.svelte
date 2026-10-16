<script>
	// One box per pair, so the whole drop is visible at a glance.
	let { total = 20, available = 0, sold = 0, loading = false } = $props();

	const reserved = $derived(Math.max(0, total - available - sold));
	const boxes = $derived(
		Array.from({ length: total }, (_, i) => {
			if (i < available) return 'available';
			if (i < available + reserved) return 'reserved';
			return 'sold';
		})
	);
</script>

<div>
	<p class="text-lg font-semibold">
		{#if loading}
			<span class="skeleton inline-block h-5 w-40 align-middle"></span>
		{:else if available === 0}
			No pairs left on sale
		{:else}
			<span class="tabular-nums">{available}</span> of {total} pairs left
		{/if}
	</p>

	<ol class="mt-3 grid grid-cols-10 gap-1.5" aria-hidden="true">
		{#each boxes as state, i (i)}
			<li class="box {loading ? 'loading' : state}"></li>
		{/each}
	</ol>

	{#if !loading}
		<ul class="mt-3 flex flex-wrap gap-x-5 gap-y-1 text-sm text-muted">
			<li class="flex items-center gap-2">
				<span class="box available swatch"></span>{available} on sale
			</li>
			<li class="flex items-center gap-2">
				<span class="box reserved swatch"></span>{reserved} reserved
			</li>
			<li class="flex items-center gap-2"><span class="box sold swatch"></span>{sold} sold</li>
		</ul>
	{/if}
</div>

<style>
	/* A shoebox seen from the end: a body with a darker lid band. */
	.box {
		position: relative;
		display: block;
		height: 1.75rem;
		border-radius: 3px;
		transition: background-color 0.4s var(--ease-editorial);
	}

	.box::before {
		content: '';
		position: absolute;
		inset: 0 0 auto;
		height: 28%;
		border-radius: 3px 3px 0 0;
		background: rgb(0 0 0 / 0.16);
	}

	.available {
		background: var(--accent);
	}

	.reserved {
		background: repeating-linear-gradient(135deg, var(--accent) 0 3px, var(--accent-soft) 3px 7px);
		box-shadow: inset 0 0 0 1.5px var(--accent);
	}

	.sold {
		background: var(--line);
	}

	.sold::before {
		background: rgb(0 0 0 / 0.06);
	}

	.loading {
		background: var(--surface);
	}

	.swatch {
		width: 0.85rem;
		height: 0.85rem;
	}
</style>
