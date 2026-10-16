<script>
	// Side view of the Air Blossom One, toe to the right. Colors are fixed: the
	// shoe is the same white leather, sakura and gum in either theme.
	const INK = '#2b1631';
	const LEATHER = '#ffffff';
	const SAKURA = '#f4b3c6';
	const MAGENTA = '#c8104f';
	const BONE = '#efe6da';
	const GUM = '#c68a4b';
	const LINING = '#6b3f5e';

	// Lace holes along the eyestay curve.
	const holes = [
		[284, 107],
		[312, 119],
		[342, 132],
		[374, 144],
		[407, 157],
		[440, 169]
	];
	const tread = Array.from({ length: 26 }, (_, i) => 104 + i * 18);
</script>

<svg viewBox="0 0 640 360" class="sneaker" aria-hidden="true">
	<defs>
		<filter id="sneaker-shadow" x="-20%" y="-300%" width="140%" height="700%">
			<feGaussianBlur stdDeviation="9" />
		</filter>
	</defs>

	<ellipse
		class="shadow"
		cx="330"
		cy="326"
		rx="262"
		ry="13"
		fill={INK}
		opacity="0.28"
		filter="url(#sneaker-shadow)"
	/>

	<g class="shoe" stroke={INK} stroke-width="3" stroke-linejoin="round" stroke-linecap="round">
		<!-- outsole -->
		<path
			fill={GUM}
			d="M52 262 L604 262 C618 262 624 274 616 286 C608 296 590 298 570 298 L96 298 C70 298 50 292 44 282 C40 272 44 262 52 262 Z"
		/>
		<path
			fill="none"
			stroke-width="2"
			opacity="0.5"
			d={tread.map((x) => `M${x} 289v7`).join(' ')}
		/>

		<!-- lining seen through the ankle opening -->
		<path
			fill={LINING}
			d="M84 100 C120 86 176 88 222 92 C212 108 204 122 198 134 C188 120 174 110 156 104 C136 99 112 97 84 100 Z"
		/>

		<!-- upper -->
		<path
			fill={LEATHER}
			d="M58 250 C44 200 44 150 60 116 C66 102 80 94 96 96 C120 98 140 100 156 104 C176 110 190 122 198 134 C206 122 214 100 226 82 C232 72 248 66 260 70 C270 74 274 84 270 96 C300 112 330 128 370 146 C430 172 500 190 556 204 C590 212 614 228 612 250 Z"
		/>

		<!-- collar padding -->
		<path
			fill="none"
			stroke={SAKURA}
			stroke-width="8"
			d="M72 114 C98 104 132 105 158 112 C174 117 186 125 194 133"
		/>

		<!-- heel counter -->
		<path
			fill={SAKURA}
			d="M58 250 C44 200 44 150 60 116 C76 126 96 156 108 196 C114 218 116 236 116 250 Z"
		/>

		<!-- toe cap -->
		<path
			fill={SAKURA}
			d="M482 186 C520 194 560 202 588 216 C606 226 613 238 612 250 L470 250 C462 230 466 204 482 186 Z"
		/>

		<!-- the blossom petal on the side panel -->
		<path fill={MAGENTA} d="M146 226 C210 160 320 146 430 200 C340 180 238 188 146 226 Z" />

		<!-- eyestay, laces and lace holes -->
		<path fill="none" stroke-width="26" d="M268 100 C320 124 400 156 474 180" />
		<path fill="none" stroke={SAKURA} stroke-width="20" d="M268 100 C320 124 400 156 474 180" />
		{#each holes as [x, y] (x)}
			<path fill="none" stroke-width="8" d="M{x - 3} {y + 1} L{x + 9} {y - 16}" />
			<path fill="none" stroke={LEATHER} stroke-width="4" d="M{x - 3} {y + 1} L{x + 9} {y - 16}" />
			<circle cx={x} cy={y} r="3.6" fill={LEATHER} stroke-width="2" />
		{/each}

		<!-- heel pull tab and tongue label -->
		<path fill={MAGENTA} d="M64 108 L48 92 C45 88 49 82 54 85 L78 100 Z" />
		<rect
			x="238"
			y="72"
			width="22"
			height="13"
			rx="2"
			fill={MAGENTA}
			stroke-width="2"
			transform="rotate(-38 249 78)"
		/>

		<!-- stitching -->
		<g fill="none" stroke-width="1.6" stroke-dasharray="4 5" opacity="0.55">
			<path d="M68 240 C58 200 58 160 68 130" />
			<path d="M486 198 C474 214 474 232 478 244" />
			<path d="M172 220 C232 176 318 166 400 194" />
		</g>

		<!-- midsole with the air window in the heel -->
		<path
			fill={BONE}
			d="M50 262 C44 248 46 236 56 226 L120 230 C200 234 300 238 400 240 C470 242 540 242 590 246 C606 248 614 254 612 262 Z"
		/>
		<rect x="78" y="238" width="96" height="16" rx="8" fill={SAKURA} stroke-width="2.5" />
		<path stroke-width="2" d="M110 238v16 M142 238v16" />
	</g>
</svg>

<style>
	.sneaker {
		width: 100%;
		height: auto;
		overflow: visible;
	}

	/* The one entrance on the page: the pair drops onto its stage. */
	.shoe {
		transform-box: fill-box;
		transform-origin: 50% 100%;
		transform: rotate(-7deg);
		animation: drop 0.9s var(--ease-editorial) both;
	}

	.shadow {
		transform-box: fill-box;
		transform-origin: 50% 50%;
		animation: land 0.9s var(--ease-editorial) both;
	}

	@keyframes drop {
		from {
			transform: translateY(-70px) rotate(-14deg);
			opacity: 0;
		}
		to {
			transform: translateY(0) rotate(-7deg);
			opacity: 1;
		}
	}

	@keyframes land {
		from {
			transform: scaleX(0.5);
			opacity: 0;
		}
		to {
			transform: scaleX(1);
			opacity: 0.28;
		}
	}
</style>
