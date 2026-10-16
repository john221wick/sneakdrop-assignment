<script>
	import '../app.css';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import BlossomMark from '#lib/BlossomMark.svelte';

	let { children } = $props();
	let dark = $state(false);

	onMount(() => {
		dark = document.documentElement.classList.contains('dark');
	});

	function toggleTheme() {
		dark = !dark;
		document.documentElement.classList.toggle('dark', dark);
		document.documentElement.style.colorScheme = dark ? 'dark' : 'light';
		try {
			localStorage.setItem('theme', dark ? 'dark' : 'light');
		} catch {
			/* ignore */
		}
	}

	const current = (path) => (page.url.pathname === resolve(path) ? 'page' : undefined);
</script>

<header
	class="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-3 px-5 pt-5 pb-3 md:px-8"
>
	<a href={resolve('/')} class="flex items-center gap-2 text-lg font-extrabold tracking-tight">
		<span class="text-accent"><BlossomMark /></span>
		sneakdrop
	</a>
	<nav class="flex items-center gap-1 text-sm font-semibold" aria-label="Main">
		<a href={resolve('/')} class="nav-link" aria-current={current('/')}>The drop</a>
		<a href={resolve('/simulator')} class="nav-link" aria-current={current('/simulator')}
			>Simulator</a
		>
		<button
			type="button"
			class="nav-link"
			onclick={toggleTheme}
			aria-label={dark ? 'Use the light theme' : 'Use the dark theme'}
		>
			{dark ? 'Light' : 'Dark'}
		</button>
	</nav>
</header>

{@render children()}

<style>
	.nav-link {
		padding: 0.45rem 0.75rem;
		border-radius: 0.6rem;
		color: var(--muted);
		transition: color 0.2s ease;
	}

	.nav-link:hover {
		color: var(--fg);
	}

	.nav-link[aria-current='page'] {
		color: var(--fg);
		text-decoration: underline;
		text-decoration-color: var(--accent);
		text-decoration-thickness: 2px;
		text-underline-offset: 6px;
	}
</style>
