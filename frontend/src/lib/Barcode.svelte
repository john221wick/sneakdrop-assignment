<script>
	// A decorative barcode drawn from the order number, like the one on a box label.
	let { value } = $props();

	const bars = $derived.by(() => {
		const out = [];
		let x = 0;
		for (const ch of value) {
			const code = ch.charCodeAt(0);
			for (let i = 0; i < 4; i++) {
				const width = ((code >> (i * 2)) & 3) + 1;
				if (i % 2 === 0) out.push({ x, width });
				x += width + 1;
			}
		}
		return { out, width: x };
	});
</script>

<svg
	viewBox="0 0 {bars.width} 40"
	preserveAspectRatio="none"
	class="h-9 w-56 max-w-full"
	role="img"
	aria-label="Barcode for order {value}"
>
	{#each bars.out as bar, i (i)}
		<rect x={bar.x} y="0" width={bar.width} height="40" fill="currentColor" />
	{/each}
</svg>
