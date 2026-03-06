// session.ts — Durable Object that holds a Puppeteer browser + page.
//
// Strategy: Keep browser connection alive in-memory across requests.
// If the DO is evicted, reconnect using stored CF session ID.

import puppeteer from '@cloudflare/puppeteer';
import type { Browser, Page } from '@cloudflare/puppeteer';
import { injectContentScript, requestSummary, executeAction, scrollPage } from './content-bridge';
import { getEnrichedAXTree } from './ax-mapper';

interface Env {
	BROWSER: Fetcher;
}

const KEEP_ALIVE_MS = 60_000;

export class BrowserSession {
	private state: DurableObjectState;
	private env: Env;
	private browser: Browser | null = null;
	private page: Page | null = null;

	constructor(state: DurableObjectState, env: Env) {
		this.state = state;
		this.env = env;
	}

	async fetch(request: Request): Promise<Response> {
		const url = new URL(request.url);
		const path = url.pathname;

		try {
			switch (path) {
				case '/create':
					return this.handleCreate(request);
				case '/navigate':
					return this.handleNavigate(request);
				case '/summary':
					return this.handleSummary();
				case '/screenshot':
					return this.handleScreenshot(request);
				case '/ax-tree':
					return this.handleAXTree();
				case '/page-text':
					return this.handlePageText();
				case '/layout':
					return this.handleLayout();
				case '/action':
					return this.handleAction(request);
				case '/scroll':
					return this.handleScroll(request);
				case '/close':
					return this.handleClose();
				default:
					return json({ error: `unknown path: ${path}` }, 404);
			}
		} catch (e: any) {
			console.error(`Session error on ${path}:`, e);
			return json({ error: e.message || String(e) }, 500);
		}
	}

	private async handleCreate(request: Request): Promise<Response> {
		const body = await request.json() as {
			url?: string;
			cookies?: Array<{ name: string; value: string; domain: string; path: string }>;
		};

		// Close any existing browser.
		if (this.browser) {
			try { await this.browser.close(); } catch (_) {}
		}

		this.browser = await puppeteer.launch(this.env.BROWSER);

		// Use the default page (avoid creating extra about:blank).
		const pages = await this.browser.pages();
		this.page = pages[0] || await this.browser.newPage();

		// Save session ID for reconnect if DO is evicted.
		await this.state.storage.put('cfSessionId', this.browser.sessionId());

		await this.page.setViewport({ width: 1280, height: 720 });

		// Inject cookies before navigation.
		if (body.cookies && body.cookies.length > 0) {
			const puppeteerCookies = body.cookies.map(c => ({
				name: c.name,
				value: c.value,
				domain: c.domain,
				path: c.path || '/',
			}));
			await this.page.setCookie(...puppeteerCookies);
		}

		// Navigate.
		if (body.url) {
			await this.page.goto(body.url, { waitUntil: 'networkidle0', timeout: 30000 });

			// Click through ngrok free-tier interstitial if present.
			try {
				const visitBtn = await this.page.$('button');
				if (visitBtn) {
					const btnText = await this.page.evaluate(el => el?.textContent || '', visitBtn);
					if (btnText.includes('Visit Site')) {
						await visitBtn.click();
						await this.page.waitForNavigation({ waitUntil: 'networkidle0', timeout: 30000 });
					}
				}
			} catch (_) { /* not an ngrok interstitial */ }
		}

		// Inject content script.
		await injectContentScript(this.page);

		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);

		return json({ status: 'created', url: this.page.url() });
	}

	/**
	 * Ensure we have a live browser + page. If the DO was evicted and
	 * recreated, reconnect using the stored CF session ID.
	 */
	private async ensureBrowser(): Promise<Page> {
		// Happy path: in-memory connection still alive.
		if (this.browser && this.browser.isConnected() && this.page) {
			return this.page;
		}

		// DO was evicted — try to reconnect.
		const cfSessionId = await this.state.storage.get<string>('cfSessionId');
		if (!cfSessionId) {
			throw new Error('No active browser session. Call /create first.');
		}

		try {
			this.browser = await puppeteer.connect(this.env.BROWSER, cfSessionId);
			const pages = await this.browser.pages();
			this.page = pages.find(p => p.url() !== 'about:blank') || pages[0] || await this.browser.newPage();

			// Re-inject content script if needed.
			try {
				const injected = await this.page.evaluate(() => !!(window as any).__xrayInjected);
				if (!injected) await injectContentScript(this.page);
			} catch (_) {
				await injectContentScript(this.page);
			}

			console.log(`Reconnected to browser session: ${cfSessionId}, page: ${this.page.url()}`);
			return this.page;
		} catch (e) {
			await this.state.storage.delete('cfSessionId');
			this.browser = null;
			this.page = null;
			throw new Error(`Browser session expired. Call /create to start a new one.`);
		}
	}

	private async handleNavigate(request: Request): Promise<Response> {
		const page = await this.ensureBrowser();
		const body = await request.json() as { url: string };
		await page.goto(body.url, { waitUntil: 'networkidle0', timeout: 30000 });
		await injectContentScript(page);
		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);
		return json({ status: 'navigated', url: page.url() });
	}

	private async handleSummary(): Promise<Response> {
		const page = await this.ensureBrowser();
		const result = await requestSummary(page);
		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);
		return json(result);
	}

	private async handleScreenshot(request: Request): Promise<Response> {
		const page = await this.ensureBrowser();
		const screenshot = await page.screenshot({ type: 'png', fullPage: true });
		const base64 = Buffer.from(screenshot).toString('base64');
		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);
		return json({ base64 });
	}

	private async handleAXTree(): Promise<Response> {
		const page = await this.ensureBrowser();
		const enriched = await getEnrichedAXTree(page);
		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);
		return json({ enriched });
	}

	private async handlePageText(): Promise<Response> {
		const page = await this.ensureBrowser();
		const text = await page.evaluate(() => {
			const body = document.body;
			return body ? body.innerText.substring(0, 50000) : '';
		});
		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);
		return json({ text });
	}

	private async handleLayout(): Promise<Response> {
		const page = await this.ensureBrowser();
		const metrics = await page.evaluate(() => ({
			width: document.documentElement.scrollWidth || window.innerWidth,
			height: Math.min(document.documentElement.scrollHeight || window.innerHeight, 16384),
		}));
		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);
		return json(metrics);
	}

	private async handleAction(request: Request): Promise<Response> {
		const page = await this.ensureBrowser();
		const body = await request.json() as {
			macheID: string;
			action: string;
			payload: string;
		};
		// Arm a MutationObserver before the action so we can detect DOM settling.
		await page.evaluate(() => {
			(window as any).__xraySettled = false;
			let timer: any = null;
			const obs = new MutationObserver(() => {
				clearTimeout(timer);
				timer = setTimeout(() => { obs.disconnect(); (window as any).__xraySettled = true; }, 500);
			});
			obs.observe(document.body, { childList: true, subtree: true, characterData: true });
			// If nothing mutates within 500ms, settle anyway.
			timer = setTimeout(() => { obs.disconnect(); (window as any).__xraySettled = true; }, 500);
		});

		await executeAction(page, body.macheID, body.action, body.payload);

		// Wait for DOM mutations to settle (500ms of silence) or 5s max.
		try {
			await page.waitForFunction(() => (window as any).__xraySettled, { timeout: 5000 });
		} catch (_) { /* timeout — proceed anyway */ }

		try { await injectContentScript(page); } catch (_) {}
		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);
		return json({ status: 'ok' });
	}

	private async handleScroll(request: Request): Promise<Response> {
		const page = await this.ensureBrowser();
		const body = await request.json() as { direction: string };
		const result = await scrollPage(page, body.direction);
		await this.state.storage.setAlarm(Date.now() + KEEP_ALIVE_MS);
		return json(result);
	}

	private async handleClose(): Promise<Response> {
		if (this.browser) {
			try { await this.browser.close(); } catch (_) {}
			this.browser = null;
			this.page = null;
		} else {
			// DO was evicted — try to close via stored session.
			const cfSessionId = await this.state.storage.get<string>('cfSessionId');
			if (cfSessionId) {
				try {
					const browser = await puppeteer.connect(this.env.BROWSER, cfSessionId);
					await browser.close();
				} catch (_) {}
			}
		}
		await this.state.storage.delete('cfSessionId');
		return json({ status: 'closed' });
	}

	async alarm(): Promise<void> {
		console.log('Session alarm: closing inactive browser');
		if (this.browser) {
			try { await this.browser.close(); } catch (_) {}
			this.browser = null;
			this.page = null;
		} else {
			const cfSessionId = await this.state.storage.get<string>('cfSessionId');
			if (cfSessionId) {
				try {
					const browser = await puppeteer.connect(this.env.BROWSER, cfSessionId);
					await browser.close();
				} catch (_) {}
			}
		}
		await this.state.storage.delete('cfSessionId');
	}
}

function json(data: any, status = 200): Response {
	return new Response(JSON.stringify(data), {
		status,
		headers: { 'Content-Type': 'application/json' },
	});
}
