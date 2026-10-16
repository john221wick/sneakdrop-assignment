<script>
	let { size = 22 } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
	<g fill="currentColor">
		{#each [0, 72, 144, 216, 288] as angle (angle)}
			<ellipse cx="12" cy="6.4" rx="3.7" ry="5.4" transform="rotate({angle} 12 12)" />
		{/each}
	</g>
	<circle cx="12" cy="12" r="2.4" fill="var(--bg)" />
</svg>
