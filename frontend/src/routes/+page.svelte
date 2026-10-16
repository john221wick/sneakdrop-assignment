<script>
	import { onMount } from 'svelte';
	import Sneaker from '#lib/Sneaker.svelte';
	import StockShelf from '#lib/StockShelf.svelte';
	import Barcode from '#lib/Barcode.svelte';

	const USER_KEY = 'sneakdrop.user';
	const PURCHASE_LIMIT = 2;
	const PRICE = '$180';
	const OFFLINE = "Can't reach the store server. Start it with: cd backend && go run .";

	let userId = $state('');
	let drop = $state(null);
	let receivedAt = $state(0);
	let offline = $state(false);
	let now = $state(Date.now());

	// The request in flight, if any: 'buy', 'join', 'pay' or 'reset'.
	let action = $state(null);
	// Set after Pay until the payment company's confirmation shows up.
	let paying = $state(null);
	// A confirmed order, shown as a receipt until dismissed.
	let order = $state(null);
	let notice = $state(null);
	let confirmingReset = $state(false);

	function randomUser() {
		if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
			return `shopper-${crypto.randomUUID().slice(0, 8)}`;
		}
		return `shopper-${Math.random().toString(36).slice(2, 10)}`;
	}

	function readStoredUser() {
		try {
			const saved = localStorage.getItem(USER_KEY);
			if (saved) return saved;
		} catch {
			/* storage unavailable, use an in-memory id */
		}
		const fresh = randomUser();
		storeUser(fresh);
		return fresh;
	}

	function storeUser(id) {
		try {
			localStorage.setItem(USER_KEY, id);
		} catch {
			/* ignore */
		}
	}

	async function readJson(res) {
		try {
			return await res.json();
		} catch {
			return null;
		}
	}

	async function post(path, body) {
		try {
			const res = await fetch(path, {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: JSON.stringify(body)
			});
			return { ok: res.ok, data: await readJson(res) };
		} catch {
			return { ok: false, data: { error: 'backend_unreachable' } };
		}
	}

	let noticeTimer;

	// Errors stay until the next action; good news clears itself.
	function say(text, tone = 'info') {
		notice = { text, tone };
		clearTimeout(noticeTimer);
		if (tone === 'info') noticeTimer = setTimeout(() => (notice = null), 8000);
	}

	function duration(ms) {
		if (ms >= 60_000) {
			const minutes = Math.round(ms / 60_000);
			return `${minutes} minute${minutes === 1 ? '' : 's'}`;
		}
		return `${Math.round(ms / 1000)} seconds`;
	}

	let refreshSeq = 0;

	async function refresh() {
		if (!userId) return;
		const requestedUser = userId;
		const seq = ++refreshSeq;
		try {
			const res = await fetch(`/api/state?user=${encodeURIComponent(requestedUser)}`);
			const data = await readJson(res);
			if (seq !== refreshSeq || requestedUser !== userId) return;
			offline = !res.ok;
			if (res.ok && data) update(data);
		} catch {
			/* network blip: keep the last known state, retry on the next cycle */
			if (seq === refreshSeq) offline = true;
		}
	}

	// Compares the new state with the last one to tell the shopper what changed.
	function update(next) {
		const prev = drop;
		if (paying && !next.hold) {
			if (next.purchases > paying.purchases) {
				order = { number: `AB1-${String(paying.holdId).padStart(6, '0')}`, owned: next.purchases };
				notice = null;
			} else {
				say(
					'Your reservation ran out before the payment came through. Any money taken will be refunded.',
					'error'
				);
			}
			paying = null;
		} else if (prev?.hold && !next.hold && next.purchases === prev.purchases) {
			say('Your reservation ran out, so the pair went back.', 'error');
		} else if (prev && prev.queue_position != null && next.hold) {
			say(`A pair came free and it's yours. You have ${duration(next.hold_ttl_ms)} to pay.`);
		}
		drop = next;
		receivedAt = Date.now();
	}

	function holdError(code) {
		switch (code) {
			case 'sold_out':
				return 'Someone just reserved the last pair. Join the waiting line to get the next one that comes free.';
			case 'already_holding':
				return 'You already have a pair reserved.';
			case 'limit_reached':
				return "You've bought 2 pairs, the most one person can buy in this drop.";
			case 'backend_unreachable':
				return OFFLINE;
			default:
				return "Couldn't reserve a pair. Try again.";
		}
	}

	function payError(code) {
		switch (code) {
			case 'hold_not_active':
			case 'hold_not_found':
				return 'Your reservation has ended, so the payment was not started.';
			case 'backend_unreachable':
				return OFFLINE;
			default:
				return "Couldn't start the payment. Try again.";
		}
	}

	function joinError(code) {
		switch (code) {
			case 'available':
				return 'Pairs are back on sale. Buy one now.';
			case 'already_holding':
				return 'You already have a pair reserved.';
			case 'limit_reached':
				return "You've bought 2 pairs, the most one person can buy in this drop.";
			case 'backend_unreachable':
				return OFFLINE;
			default:
				return "Couldn't join the waiting line. Try again.";
		}
	}

	async function buy() {
		action = 'buy';
		notice = null;
		const res = await post('/api/hold', { user: userId });
		if (!res.ok) say(holdError(res.data?.error), 'error');
		await refresh();
		action = null;
	}

	async function pay() {
		if (!drop?.hold) return;
		action = 'pay';
		notice = null;
		const holdId = drop.hold.id;
		const res = await post('/api/pay', { user: userId, hold_id: holdId });
		if (res.ok) {
			paying = { holdId, purchases: drop.purchases };
		} else {
			say(payError(res.data?.error), 'error');
		}
		action = null;
		await refresh();
	}

	async function joinLine() {
		action = 'join';
		notice = null;
		const res = await post('/api/queue', { user: userId });
		if (!res.ok) say(joinError(res.data?.error), 'error');
		await refresh();
		action = null;
	}

	async function reset() {
		confirmingReset = false;
		action = 'reset';
		const res = await post('/admin/reset', {});
		if (res.ok) {
			drop = null;
			paying = null;
			order = null;
			say('The drop was reset. All 20 pairs are back on sale.');
		} else if (res.data?.error === 'forbidden') {
			say('Reset is turned off on the server (ALLOW_RESET=0).', 'error');
		} else {
			say(
				res.data?.error === 'backend_unreachable' ? OFFLINE : "Couldn't reset the drop.",
				'error'
			);
		}
		await refresh();
		action = null;
	}

	function switchUser(next) {
		userId = next;
		storeUser(next);
		drop = null;
		paying = null;
		order = null;
		notice = null;
		refresh();
	}

	function saveUser() {
		switchUser(userId.trim() || randomUser());
	}

	onMount(() => {
		userId = readStoredUser();

		let stopped = false;
		let poll = null;
		async function pollLoop() {
			await refresh();
			if (!stopped) poll = setTimeout(pollLoop, 1000);
		}
		pollLoop();

		const tick = setInterval(() => (now = Date.now()), 250);
		return () => {
			stopped = true;
			clearTimeout(poll);
			clearInterval(tick);
		};
	});

	const ttl = $derived(drop?.hold_ttl_ms ?? 300_000);
	// Count down from the server's own remaining time, so a wrong local clock does not matter.
	const remaining = $derived(
		drop?.hold ? Math.max(0, drop.hold.expires_in_ms - (now - receivedAt)) : 0
	);
	const countdown = $derived.by(() => {
		const seconds = Math.ceil(remaining / 1000);
		return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
	});
	const busy = $derived(action !== null);

	const phase = $derived.by(() => {
		if (order) return 'confirmed';
		if (!drop) return offline ? 'offline' : 'loading';
		if (paying || drop.hold) return 'checkout';
		if (drop.queue_position != null) return 'waiting';
		if (drop.purchases >= PURCHASE_LIMIT) return 'limit';
		if (drop.available === 0) return 'soldout';
		return 'available';
	});
</script>

<svelte:head>
	<title>Air Blossom One | sneakdrop</title>
	<meta
		name="description"
		content="Twenty pairs of the Air Blossom One. Reserve a pair, then pay within five minutes."
	/>
</svelte:head>

<div class="demo-bar">
	<div
		class="mx-auto flex max-w-6xl flex-wrap items-center gap-x-4 gap-y-2 px-5 py-2.5 text-sm md:px-8"
	>
		<span class="font-semibold">Demo store</span>
		<label class="flex max-w-full min-w-0 items-center gap-2 text-muted" for="user">
			<span class="whitespace-nowrap">Shopping as</span>
			<input
				id="user"
				class="user-field"
				style="width: {Math.min(Math.max(userId.length, 8) + 2, 40)}ch"
				type="text"
				autocomplete="off"
				spellcheck="false"
				bind:value={userId}
				onchange={saveUser}
			/>
		</label>
		<button
			class="font-semibold text-accent hover:underline"
			type="button"
			disabled={busy}
			onclick={() => switchUser(randomUser())}
		>
			New shopper
		</button>

		<div class="ml-auto flex items-center gap-2">
			{#if confirmingReset}
				<span class="text-muted">Clear every reservation, order and the line?</span>
				<button class="btn btn-sm btn-ink" type="button" onclick={reset}>Reset</button>
				<button
					class="btn btn-sm btn-ghost"
					type="button"
					onclick={() => (confirmingReset = false)}
				>
					Cancel
				</button>
			{:else}
				<button
					class="btn btn-sm btn-ghost"
					type="button"
					disabled={busy}
					aria-busy={action === 'reset'}
					onclick={() => (confirmingReset = true)}
				>
					{#if action === 'reset'}
						<span class="spinner" aria-hidden="true"></span> Resetting the drop…
					{:else}
						Reset drop
					{/if}
				</button>
			{/if}
		</div>
	</div>
</div>

<main
	class="mx-auto grid max-w-6xl gap-10 px-5 pt-8 pb-20 md:px-8 lg:grid-cols-[1.1fr_1fr] lg:gap-14 lg:pt-12"
>
	<section class="stage" aria-label="Air Blossom One, side view">
		<div class="stage-disc"></div>
		<div class="stage-shoe"><Sneaker /></div>
		<p class="stage-note">Only 20 pairs made</p>
	</section>

	<section class="flex flex-col gap-8" aria-labelledby="product-name">
		<div>
			<h1 id="product-name" class="display text-[clamp(3.75rem,9vw,6.5rem)]">Air Blossom One</h1>
			<div class="mt-4 flex items-end justify-between gap-4">
				<div>
					<p class="font-semibold">Sakura/Bone-Gum</p>
					<p class="text-sm text-muted">Style AB1-020, white leather with a gum sole</p>
				</div>
				<p class="text-3xl font-bold tabular-nums">{PRICE}</p>
			</div>
		</div>

		<StockShelf
			total={drop?.total ?? 20}
			available={drop?.available ?? 0}
			sold={drop?.sold ?? 0}
			loading={!drop}
		/>

		<div class="panel" aria-live="polite">
			{#if notice}
				<p class="notice" class:error={notice.tone === 'error'}>{notice.text}</p>
			{/if}

			{#if phase === 'loading'}
				<div class="flex items-center gap-3 text-muted">
					<span class="spinner" aria-hidden="true"></span> Checking stock…
				</div>
				<div class="skeleton mt-5 h-14 w-full"></div>
			{:else if phase === 'offline'}
				<p class="font-semibold">The store is offline</p>
				<p class="mt-1 text-sm text-muted">{OFFLINE}</p>
			{:else if phase === 'available'}
				<button
					class="btn btn-solid btn-lg w-full"
					type="button"
					disabled={busy}
					aria-busy={action === 'buy'}
					onclick={buy}
				>
					{#if action === 'buy'}
						<span class="spinner" aria-hidden="true"></span> Reserving your pair…
					{:else}
						Buy now
					{/if}
				</button>
				<p class="mt-3 text-sm text-muted">
					We hold your pair for {duration(ttl)} while you pay.
				</p>
			{:else if phase === 'checkout'}
				<div class="flex items-baseline justify-between gap-4">
					<h2 class="text-xl font-semibold">Your pair is reserved</h2>
					<p
						class="text-2xl font-bold tabular-nums"
						class:text-accent={remaining < 60_000}
						aria-label="{countdown} left to pay"
					>
						{countdown}
					</p>
				</div>
				<div class="timer mt-3" aria-hidden="true">
					<span style="width: {Math.min(100, (remaining / ttl) * 100)}%"></span>
				</div>
				<p class="mt-2 text-sm text-muted">
					{#if remaining === 0}
						Time's up. Checking whether your payment made it…
					{:else}
						Pay before the timer runs out, or the pair goes to the next person.
					{/if}
				</p>

				<dl class="summary mt-6">
					<div>
						<dt>Air Blossom One, Sakura/Bone-Gum</dt>
						<dd>{PRICE}</dd>
					</div>
					<div>
						<dt>Shipping</dt>
						<dd>Free</dd>
					</div>
					<div class="total">
						<dt>Total</dt>
						<dd>{PRICE}</dd>
					</div>
				</dl>

				<div class="card-row mt-5">
					<span class="card-chip" aria-hidden="true"></span>
					<div>
						<p class="font-semibold">Test card ending 4242</p>
						<p class="text-xs text-muted">Demo payment. No real money moves.</p>
					</div>
				</div>

				<button
					class="btn btn-solid btn-lg mt-5 w-full"
					type="button"
					disabled={busy || paying !== null || remaining === 0}
					aria-busy={action === 'pay' || paying !== null}
					onclick={pay}
				>
					{#if paying}
						<span class="spinner" aria-hidden="true"></span> Confirming payment…
					{:else if action === 'pay'}
						<span class="spinner" aria-hidden="true"></span> Sending payment…
					{:else}
						Pay {PRICE}
					{/if}
				</button>
				{#if paying}
					<p class="mt-3 text-sm text-muted">
						The payment company is confirming your payment. This usually takes a few seconds.
					</p>
				{/if}
			{:else if phase === 'confirmed'}
				<p class="flex items-center gap-2 font-semibold text-accent">
					<svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true">
						<path
							d="M5 12.5l4.5 4.5L19 7.5"
							fill="none"
							stroke="currentColor"
							stroke-width="3"
							stroke-linecap="round"
							stroke-linejoin="round"
						/>
					</svg>
					Payment confirmed
				</p>
				<h2 class="display mt-3 text-5xl">The pair is yours</h2>
				<dl class="summary mt-6">
					<div>
						<dt>Order</dt>
						<dd class="tabular-nums">{order.number}</dd>
					</div>
					<div>
						<dt>Paid with test card ending 4242</dt>
						<dd>{PRICE}</dd>
					</div>
					<div>
						<dt>Pairs you own from this drop</dt>
						<dd>{order.owned} of {PURCHASE_LIMIT}</dd>
					</div>
				</dl>
				<div class="mt-5 text-fg"><Barcode value={order.number} /></div>
				<button class="btn btn-ghost mt-6 w-full" type="button" onclick={() => (order = null)}>
					Done
				</button>
			{:else if phase === 'waiting'}
				<h2 class="text-xl font-semibold">You're number {drop.queue_position} in line</h2>
				<p class="mt-2 text-sm text-muted">
					If someone doesn't pay in time, their pair goes to the next person in line, who gets {duration(
						ttl
					)} to pay. Keep this page open.
				</p>
				<p class="mt-5 flex items-center gap-3 text-sm font-semibold">
					<span class="spinner text-accent" aria-hidden="true"></span>
					Waiting for a pair to come free. {drop.queue_length}
					{drop.queue_length === 1 ? 'person' : 'people'} in line.
				</p>
			{:else if phase === 'soldout'}
				<h2 class="text-xl font-semibold">Sold out for now</h2>
				<p class="mt-2 text-sm text-muted">
					Every pair is reserved or sold. Reserved pairs that aren't paid for in time go to the
					waiting line, first come, first served.
				</p>
				<button
					class="btn btn-solid btn-lg mt-5 w-full"
					type="button"
					disabled={busy}
					aria-busy={action === 'join'}
					onclick={joinLine}
				>
					{#if action === 'join'}
						<span class="spinner" aria-hidden="true"></span> Joining the line…
					{:else}
						Join the waiting line
					{/if}
				</button>
			{:else if phase === 'limit'}
				<h2 class="text-xl font-semibold">You've bought your 2 pairs</h2>
				<p class="mt-2 text-sm text-muted">That's the most one person can buy in this drop.</p>
			{/if}

			{#if drop?.refunds_due}
				<p class="mt-4 text-sm text-muted">
					{drop.refunds_due} late payment{drop.refunds_due === 1 ? '' : 's'} will be refunded to you.
				</p>
			{/if}
		</div>

		<ul class="rules text-sm">
			<li><span>Reserve</span>one pair at a time</li>
			<li><span>Pay within</span>{duration(ttl)} of reserving</li>
			<li><span>Limit</span>{PURCHASE_LIMIT} pairs per person</li>
			<li>
				<span>You own</span>{drop ? `${drop.purchases} of ${PURCHASE_LIMIT}` : '…'}
			</li>
		</ul>
	</section>
</main>

<style>
	.demo-bar {
		border-block: 1px dashed var(--line);
		background: var(--surface);
	}

	.user-field {
		min-width: 0;
		max-width: 100%;
		border: 0;
		border-bottom: 1px solid var(--line);
		background: transparent;
		padding: 0.1rem 0;
		color: var(--fg);
		font-weight: 600;
	}

	.user-field:focus {
		outline: none;
		border-bottom-color: var(--accent);
	}

	.stage {
		position: relative;
		display: grid;
		place-items: center;
		min-height: 20rem;
		aspect-ratio: 5 / 4;
		overflow: hidden;
		border-radius: 1.75rem;
		background: var(--surface);
	}

	/* On wide screens the stage is square and stays in view beside the checkout. */
	@media (min-width: 1024px) {
		.stage {
			position: sticky;
			top: 1.5rem;
			align-self: start;
			aspect-ratio: 1;
		}
	}

	.stage-disc {
		position: absolute;
		width: 74%;
		aspect-ratio: 1;
		border-radius: 50%;
		background: var(--blossom);
	}

	.stage-shoe {
		position: relative;
		width: 86%;
		margin-top: 6%;
	}

	.stage-note {
		position: absolute;
		bottom: 1.1rem;
		left: 1.4rem;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--muted);
	}

	.panel {
		border: 1px solid var(--line);
		border-radius: 1.25rem;
		background: var(--raised);
		padding: 1.5rem;
	}

	.notice {
		margin-bottom: 1.25rem;
		border-radius: 0.75rem;
		background: var(--surface);
		padding: 0.75rem 1rem;
		font-size: 0.875rem;
	}

	.notice.error {
		background: var(--accent-soft);
		color: var(--accent);
	}

	.timer {
		height: 4px;
		overflow: hidden;
		border-radius: 999px;
		background: var(--surface);
	}

	.timer span {
		display: block;
		height: 100%;
		background: var(--accent);
		transition: width 0.25s linear;
	}

	.summary div {
		display: flex;
		justify-content: space-between;
		gap: 1rem;
		border-top: 1px solid var(--line);
		padding: 0.6rem 0;
		font-size: 0.9375rem;
	}

	.summary dt {
		color: var(--muted);
	}

	.summary .total {
		font-weight: 700;
	}

	.summary .total dt {
		color: var(--fg);
	}

	.card-row {
		display: flex;
		align-items: center;
		gap: 0.9rem;
		border-radius: 0.9rem;
		background: var(--surface);
		padding: 0.8rem 1rem;
	}

	.card-chip {
		width: 2.6rem;
		height: 1.75rem;
		flex: none;
		border-radius: 0.35rem;
		background: linear-gradient(135deg, var(--fg), var(--accent));
	}

	.rules {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 1rem 1.5rem;
	}

	.rules li {
		border-top: 1px solid var(--line);
		padding-top: 0.6rem;
	}

	.rules span {
		display: block;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--muted);
	}
</style>
