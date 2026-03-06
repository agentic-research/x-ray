// CF Worker entry point: routes requests to BrowserSession Durable Objects.

export { BrowserSession } from './session';

interface Env {
	BROWSER: Fetcher;
	BROWSER_SESSION: DurableObjectNamespace;
	API_TOKEN: string;
}

export default {
	async fetch(request: Request, env: Env): Promise<Response> {
		// Auth check.
		if (env.API_TOKEN) {
			const auth = request.headers.get('Authorization');
			if (auth !== `Bearer ${env.API_TOKEN}`) {
				return json({ error: 'unauthorized' }, 401);
			}
		}

		const url = new URL(request.url);
		const path = url.pathname;

		// POST /session — create new browser session.
		if (path === '/session' && request.method === 'POST') {
			let body: { url?: string; cookies?: any[] } = {};
			try {
				body = await request.json() as { url?: string; cookies?: any[] };
			} catch (_) {
				// Empty or invalid body — use defaults.
			}
			const id = env.BROWSER_SESSION.newUniqueId();
			const stub = env.BROWSER_SESSION.get(id);
			const resp = await stub.fetch(new Request('http://do/create', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ url: body.url, cookies: body.cookies }),
			}));
			const result = await resp.json() as any;
			return json({ id: id.toString(), ...result });
		}

		// All other routes: /session/:id/...
		const match = path.match(/^\/session\/([^/]+)(\/.*)?$/);
		if (!match) {
			return json({ error: 'not found' }, 404);
		}

		const sessionId = match[1];
		const subpath = match[2] || '';

		// DELETE /session/:id
		if (request.method === 'DELETE' && !subpath) {
			const id = env.BROWSER_SESSION.idFromString(sessionId);
			const stub = env.BROWSER_SESSION.get(id);
			return stub.fetch(new Request('http://do/close', { method: 'POST' }));
		}

		// POST /session/:id/<action>
		if (request.method === 'POST' && subpath) {
			const id = env.BROWSER_SESSION.idFromString(sessionId);
			const stub = env.BROWSER_SESSION.get(id);
			return stub.fetch(new Request(`http://do${subpath}`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: request.body,
			}));
		}

		return json({ error: 'not found' }, 404);
	},
};

function json(data: any, status = 200): Response {
	return new Response(JSON.stringify(data), {
		status,
		headers: { 'Content-Type': 'application/json' },
	});
}
