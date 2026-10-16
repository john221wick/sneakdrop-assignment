<script>
	let buyers = $state(1000);
	let windowMs = $state(1000);
	let running = $state(false);
	let report = $state(null);
	let error = $state(null);

	async function run() {
		running = true;
		error = null;
		try {
			const res = await fetch('/api/simulate', {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: JSON.stringify({ buyers: Number(buyers), window_ms: Number(windowMs) })
			});
			const data = await res.json().catch(() => null);
			if (res.ok && data) {
				report = data;
			} else if (data?.error === 'backend_unreachable') {
				error = 'Cannot reach the drop server. Start it with: cd backend && go run .';
			} else {
				error = data?.error ?? 'The simulation failed.';
			}
		} catch {
			error = 'Cannot reach the drop server.';
		}
		running = false;
	}

	const held = $derived(report?.outcomes.held ?? 0);
	const turnedAway = $derived(
		(report?.outcomes.sold_out ?? 0) + (report?.outcomes.already_holding ?? 0)
	);
	const errors = $derived(report ? report.clicks - held - turnedAway : 0);

	function label(check) {
		if (check.info) return 'Note';
		return check.ok ? 'Pass' : 'Fail';
	}

	function answer(message) {
		return message.status === 200 ? message.outcome : `${message.status} ${message.outcome}`;
	}
</script>

<section aria-labelledby="sim-title">
	<h1 id="sim-title" class="display text-[clamp(3.25rem,8vw,5.5rem)]">Rush the drop</h1>
	<p class="mt-5 max-w-2xl text-muted">
		Runs a separate drop of 20 pairs with short reservations and a payment company that repeats
		every message. Shows who got a pair and checks every rule. The real drop is not touched.
	</p>

	<div class="mt-8 flex flex-wrap items-end gap-6">
		<div class="w-28">
			<label class="field-label" for="sim-buyers">Buyers</label>
			<input
				id="sim-buyers"
				class="field tabular-nums"
				type="number"
				min="1"
				max="5000"
				bind:value={buyers}
			/>
		</div>
		<div class="w-40">
			<label class="field-label" for="sim-window">Clicks within (ms)</label>
			<input
				id="sim-window"
				class="field tabular-nums"
				type="number"
				min="0"
				max="5000"
				bind:value={windowMs}
			/>
		</div>
		<button
			class="btn btn-solid"
			type="button"
			disabled={running}
			aria-busy={running}
			onclick={run}
		>
			{#if running}
				<span class="spinner" aria-hidden="true"></span> Running the rush…
			{:else}
				Run simulation
			{/if}
		</button>
	</div>
	<p class="mt-3 text-xs text-muted">
		1000 ms spreads the clicks over one second, so the first clickers should win. 0 sends every
		click at the same instant.
	</p>

	{#if running}
		<p class="mt-6 text-sm text-muted">
			Buyers are rushing the drop, then payments, the waiting line and expiry play out. This takes a
			few seconds.
		</p>
	{/if}
	{#if error}
		<p class="mt-6 text-sm text-accent">{error}</p>
	{/if}

	{#if report}
		<p class="mt-10 text-sm font-medium" class:text-accent={!report.passed}>
			{report.passed ? 'All checks passed.' : 'Some checks failed.'}
		</p>
		<p class="mt-2 text-sm text-muted">
			{report.clicks} clicks from {report.buyers} buyers
			{report.window_ms ? `spread over ${report.window_ms} ms` : 'at the same instant'}. The rush
			took {report.rush_ms} ms. Holds last {report.hold_ttl_ms / 1000}s in the sandbox.
		</p>

		<div class="mt-8 grid gap-8 sm:grid-cols-3">
			<div>
				<div class="label">Held</div>
				<div class="display mt-2 text-6xl tabular-nums">
					{held}<span class="text-xl text-muted"> / {report.stock}</span>
				</div>
			</div>
			<div>
				<div class="label">Turned away</div>
				<div class="display mt-2 text-6xl tabular-nums">{turnedAway}</div>
			</div>
			<div>
				<div class="label">Errors</div>
				<div class="display mt-2 text-6xl tabular-nums">{errors}</div>
			</div>
		</div>

		<h2 class="mt-12 text-xl font-semibold">Checks</h2>
		<ul class="mt-4 space-y-3">
			{#each report.checks as check (check.name)}
				<li class="flex gap-4 text-sm">
					<span
						class="w-10 shrink-0 text-xs font-medium tracking-wider uppercase"
						class:text-muted={check.info}
						class:text-accent={!check.ok}>{label(check)}</span
					>
					<span>
						{check.name}
						{#if check.detail}
							<span class="block text-xs text-muted">{check.detail}</span>
						{/if}
					</span>
				</li>
			{/each}
		</ul>

		{#if report.winners?.length}
			<h2 class="mt-12 text-xl font-semibold">Who got a pair</h2>
			<p class="mt-2 text-xs text-muted">In the order the server granted them.</p>
			<div class="mt-4 overflow-x-auto">
				<table class="w-full text-left text-sm tabular-nums">
					<thead class="text-xs text-muted">
						<tr>
							<th class="py-2 pr-4 font-medium">#</th>
							<th class="py-2 pr-4 font-medium">Buyer</th>
							<th class="py-2 pr-4 font-medium">Clicked</th>
							<th class="py-2 pr-4 font-medium">Hold</th>
							<th class="py-2 font-medium">Then</th>
						</tr>
					</thead>
					<tbody>
						{#each report.winners as winner (winner.hold_id)}
							<tr class="border-t border-line">
								<td class="py-2 pr-4">{winner.order}</td>
								<td class="py-2 pr-4 whitespace-nowrap">{winner.user}</td>
								<td class="py-2 pr-4">#{winner.click}</td>
								<td class="py-2 pr-4">{winner.hold_id}</td>
								<td class="py-2" class:text-muted={winner.fate !== 'paid'}>{winner.fate}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}

		{#if report.line?.length}
			<h2 class="mt-12 text-xl font-semibold">Waiting line</h2>
			<ol class="mt-4 space-y-2 text-sm">
				{#each report.line as waiter (waiter.user)}
					<li class="flex gap-4">
						<span class="w-6 shrink-0 text-muted tabular-nums">{waiter.position}</span>
						<span class="w-24 shrink-0">{waiter.user}</span>
						<span class="text-muted">{waiter.fate}</span>
					</li>
				{/each}
			</ol>
		{/if}

		{#if report.messages?.length}
			<details class="mt-12">
				<summary class="cursor-pointer text-sm">
					<span class="text-xl font-semibold">Payment messages</span>
					<span class="ml-2 text-xs text-muted">{report.messages.length} received</span>
				</summary>
				<div class="mt-4 overflow-x-auto">
					<table class="w-full text-left text-xs tabular-nums">
						<thead class="text-muted">
							<tr>
								<th class="py-2 pr-4 font-medium">At</th>
								<th class="py-2 pr-4 font-medium">Kind</th>
								<th class="py-2 pr-4 font-medium">Hold</th>
								<th class="py-2 pr-4 font-medium">Event</th>
								<th class="py-2 font-medium">Shop's answer</th>
							</tr>
						</thead>
						<tbody>
							{#each report.messages as message, i (i)}
								<tr class="border-t border-line">
									<td class="py-1.5 pr-4">+{message.at_ms} ms</td>
									<td class="py-1.5 pr-4">{message.kind}</td>
									<td class="py-1.5 pr-4">{message.hold_id}</td>
									<td class="py-1.5 pr-4 whitespace-nowrap">{message.event_id.slice(0, 16)}</td>
									<td class="py-1.5" class:text-accent={message.status !== 200}>
										{answer(message)}
										{#if message.source === 'simulator'}
											<span class="text-muted">(sent by the simulator)</span>
										{/if}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</details>
		{/if}

		<p class="mt-12 text-sm text-muted">
			Final books: available {report.final.available} + held {report.final.held} + sold {report
				.final.sold} = {report.final.available + report.final.held + report.final.sold} of {report
				.final.total}. {report.final.waiting} still waiting, {report.final.refunds_due} refund owed.
		</p>
	{/if}
</section>
