import { BACKEND_URL } from '$app/env/private';

// The drop itself runs in the Go server in ../backend. This app only serves
// the page, so API, webhook and admin requests are passed straight through.
const BACKEND_PREFIXES = ['/api/', '/webhooks/', '/admin/'];
const FORWARDED_HEADERS = ['content-type', 'x-signature'];

function backendUrl() {
	return BACKEND_URL || 'http://127.0.0.1:8080';
}

export async function handle({ event, resolve }) {
	const { pathname, search } = event.url;
	if (!BACKEND_PREFIXES.some((prefix) => pathname.startsWith(prefix))) {
		return resolve(event);
	}

	const headers = new Headers();
	for (const name of FORWARDED_HEADERS) {
		const value = event.request.headers.get(name);
		if (value) headers.set(name, value);
	}
	const method = event.request.method;
	const body =
		method === 'GET' || method === 'HEAD' ? undefined : await event.request.arrayBuffer();

	let response;
	try {
		response = await fetch(new URL(pathname + search, backendUrl()), { method, headers, body });
	} catch {
		return Response.json({ error: 'backend_unreachable' }, { status: 502 });
	}
	return new Response(response.body, {
		status: response.status,
		headers: { 'content-type': response.headers.get('content-type') ?? 'application/json' }
	});
}
