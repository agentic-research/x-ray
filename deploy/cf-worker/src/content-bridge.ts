// content-bridge.ts — Injects x-ray's content.js logic into CF Puppeteer pages.
// Functions are injected via page.evaluate() and called directly (no chrome.runtime).

import type { Page } from '@cloudflare/puppeteer';

/**
 * Inject the content.js registry logic into the page.
 * Call this once after navigation and after each page load.
 */
export async function injectContentScript(page: Page): Promise<void> {
	await page.evaluate(() => {
		// Guard against double-injection.
		if ((window as any).__xrayInjected) return;
		(window as any).__xrayInjected = true;

		(window as any).__xrayIdCounter = 0;
		(window as any).__xrayRegistry = new Map();
		(window as any).__xrayReverse = new Map();

		(window as any).__xrayIsVisible = function(el: Element): boolean {
			if (el.getAttribute('aria-hidden') === 'true') return false;
			if (el.closest('[aria-hidden="true"]')) return false;
			const style = getComputedStyle(el);
			if (style.display === 'none' || style.visibility === 'hidden') return false;
			const rect = el.getBoundingClientRect();
			if (rect.width === 0 && rect.height === 0) return false;
			return true;
		};

		(window as any).__xrayBuildRegistry = function(): void {
			const isVisible = (window as any).__xrayIsVisible;
			const registry = (window as any).__xrayRegistry as Map<string, Element>;
			const reverse = (window as any).__xrayReverse as Map<Element, string>;

			const interactiveAncestors = new Map<Node, number>();
			const interactiveNodes = document.querySelectorAll(
				'a, button, input, select, textarea, ' +
				'[role="button"], [role="link"], [role="tab"], [contenteditable="true"]'
			);
			interactiveNodes.forEach(node => {
				if (!reverse.has(node) && isVisible(node)) {
					const id = `mache-${(window as any).__xrayIdCounter++}`;
					registry.set(id, node);
					reverse.set(node, id);
					node.setAttribute('data-mache-id', id);
				}
				if (!isVisible(node)) return;
				let parent = node.parentElement;
				while (parent) {
					interactiveAncestors.set(parent, (interactiveAncestors.get(parent) || 0) + 1);
					parent = parent.parentElement;
				}
			});

			// Phase 1.5: cursor-interactive divs.
			const KNOWN_INTERACTIVE_TAGS = new Set([
				'a', 'button', 'input', 'select', 'textarea', 'audio', 'video', 'details', 'summary'
			]);
			let cursorCount = 0;
			const allEls = document.querySelectorAll('*');
			for (const el of allEls) {
				if (cursorCount >= 50) break;
				if (reverse.has(el)) continue;
				const tag = el.tagName.toLowerCase();
				if (KNOWN_INTERACTIVE_TAGS.has(tag)) continue;
				const style = getComputedStyle(el);
				const hasCursorPointer = style.cursor === 'pointer';
				const hasOnClick = el.hasAttribute('onclick');
				const tabIdx = el.getAttribute('tabindex');
				const hasFocusable = tabIdx !== null && tabIdx !== '-1';
				if (!hasCursorPointer && !hasOnClick && !hasFocusable) continue;
				if (hasCursorPointer && !hasOnClick && !hasFocusable) {
					const parent = el.parentElement;
					if (parent && getComputedStyle(parent).cursor === 'pointer') continue;
				}
				if (!isVisible(el)) continue;
				const rect = el.getBoundingClientRect();
				if (rect.width === 0 && rect.height === 0) continue;
				const text = (el.textContent || '').trim();
				if (!text && !el.children.length) continue;
				const id = `mache-${(window as any).__xrayIdCounter++}`;
				registry.set(id, el);
				reverse.set(el, id);
				el.setAttribute('data-mache-id', id);
				cursorCount++;
				let ancestor = el.parentElement;
				while (ancestor) {
					interactiveAncestors.set(ancestor, (interactiveAncestors.get(ancestor) || 0) + 1);
					ancestor = ancestor.parentElement;
				}
			}

			// Phase 2: structural containers.
			const containers = document.querySelectorAll(
				'main, section, article, nav, header, footer, aside, form, ul, ol, dl, table, tbody, ' +
				'[role="navigation"], [role="main"], [role="list"], [role="group"], [role="region"]'
			);
			containers.forEach(node => {
				if (!reverse.has(node) && (interactiveAncestors.get(node) || 0) >= 2) {
					const id = `mache-${(window as any).__xrayIdCounter++}`;
					registry.set(id, node);
					reverse.set(node, id);
					node.setAttribute('data-mache-id', id);
				}
			});

			// Phase 3: body + semantic divs.
			const SEMANTIC_KEYWORDS = /content|main|sidebar|footer|header|wrapper|container|layout|page|app/i;
			const bodyAndDivs = document.querySelectorAll('body, div');
			bodyAndDivs.forEach(node => {
				if (reverse.has(node)) return;
				const tag = node.tagName.toLowerCase();
				if (tag !== 'body') {
					const hasRole = node.hasAttribute('role');
					const hasSemantic = SEMANTIC_KEYWORDS.test(node.id || '') || SEMANTIC_KEYWORDS.test(node.className || '');
					const hasChildren = (interactiveAncestors.get(node) || 0) >= 3;
					if (!hasRole && !hasSemantic && !hasChildren) return;
				}
				if (!isVisible(node)) return;
				const id = `mache-${(window as any).__xrayIdCounter++}`;
				registry.set(id, node);
				reverse.set(node, id);
				node.setAttribute('data-mache-id', id);
			});

			// Prune stale.
			for (const [id, node] of registry) {
				if (!document.contains(node) || !isVisible(node)) {
					registry.delete(id);
					reverse.delete(node);
					try { node.removeAttribute('data-mache-id'); } catch (_) {}
				}
			}
		};

		(window as any).__xrayGetPath = function(element: Element, maxLevels = 3): string {
			const parts: string[] = [];
			let el: Element | null = element;
			for (let i = 0; i < maxLevels && el && el !== document.body; i++) {
				const tag = el.tagName.toLowerCase();
				const cls = el.classList.length > 0 ? '.' + el.classList[0] : '';
				parts.unshift(tag + cls);
				el = el.parentElement;
			}
			return parts.join(' > ');
		};

		(window as any).__xrayGetSemanticColor = function(node: Element): string {
			const tag = node.tagName.toLowerCase();
			if (tag === 'a' || node.getAttribute('role') === 'link') return 'BLUE';
			if (tag === 'button' || node.getAttribute('role') === 'button') return 'GREEN';
			if (['input', 'textarea', 'select'].includes(tag)) return 'ORANGE';
			const style = getComputedStyle(node);
			if (style.cursor === 'pointer') return 'PURPLE';
			return 'GRAY';
		};

		// Build on first injection.
		(window as any).__xrayBuildRegistry();
	});
}

/**
 * Build the registry and generate summary text.
 */
export async function requestSummary(page: Page): Promise<{ summary: string; url: string }> {
	return page.evaluate(() => {
		(window as any).__xrayBuildRegistry();

		const registry = (window as any).__xrayRegistry as Map<string, Element>;
		const reverse = (window as any).__xrayReverse as Map<Element, string>;
		const getPath = (window as any).__xrayGetPath;
		const getColor = (window as any).__xrayGetSemanticColor;

		let summary = "Interactive Elements:\n";
		let count = 0;
		const pageWidth = document.documentElement.scrollWidth || window.innerWidth;
		const pageHeight = document.documentElement.scrollHeight || window.innerHeight;

		for (const [macheId, node] of registry) {
			if (count >= 500) break;
			const tag = node.tagName.toLowerCase();
			const clone = node.cloneNode(true) as Element;
			clone.querySelectorAll('script, style').forEach(s => s.remove());
			const rawText = (clone.textContent || '').replace(/\s+/g, ' ').trim();
			let text = rawText.length > 1500
				? rawText.substring(0, 1500) + `... [${rawText.length} chars total]`
				: rawText;
			if (!text) {
				text = node.getAttribute('aria-label') || node.getAttribute('title') || '';
			}
			if (!text && tag === 'input') {
				text = (node as HTMLInputElement).placeholder || (node as HTMLInputElement).name || 'input';
			}
			if (!text && !node.children.length) continue;

			const rect = node.getBoundingClientRect();
			const x = (rect.left + window.scrollX) / pageWidth;
			const y = (rect.top + window.scrollY) / pageHeight;
			const w = rect.width / pageWidth;
			const h = rect.height / pageHeight;
			const bounds = `[${x.toFixed(3)}, ${y.toFixed(3)}, ${w.toFixed(3)}, ${h.toFixed(3)}]`;
			const color = getColor(node);
			const cs = getComputedStyle(node);
			const fontSize = parseFloat(cs.fontSize) || 0;
			const display = cs.display || 'block';
			const zIndex = cs.zIndex;
			const opacity = parseFloat(cs.opacity);
			const area = rect.width * rect.height;
			const textDensity = area > 0 ? Math.min(1.0, text.length / (area / 1000)) : 0;
			const interactive = node.tabIndex >= 0 || ['a', 'button', 'input', 'select', 'textarea'].includes(tag);

			let parentID = 'none';
			let ancestor = node.parentElement;
			while (ancestor) {
				if (reverse.has(ancestor)) {
					parentID = reverse.get(ancestor)!;
					break;
				}
				ancestor = ancestor.parentElement;
			}
			summary += `ID: ${macheId} | Color: ${color} | Bounds: ${bounds} | Parent: ${parentID} | Tag: ${tag} | Text: "${text}" | Path: ${getPath(node)}` +
				` | FontSize: ${fontSize.toFixed(0)} | Display: ${display} | Interactive: ${interactive} | TextDensity: ${textDensity.toFixed(2)}` +
				` | ZIndex: ${zIndex} | Opacity: ${opacity.toFixed(2)}\n`;
			count++;
		}

		return { summary, url: window.location.href };
	});
}

/**
 * Execute an action (click/type/enter/focus) on a mache element.
 */
export async function executeAction(
	page: Page,
	macheID: string,
	action: string,
	payload: string
): Promise<void> {
	await page.evaluate(({ macheID, action, payload }) => {
		const registry = (window as any).__xrayRegistry as Map<string, Element>;
		let el = registry.get(macheID);
		if (!el) {
			console.error('X-Ray CF: element not found:', macheID);
			return;
		}

		if (action === 'type') {
			(el as HTMLInputElement).focus();
			const nativeInputSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;
			const nativeTextAreaSetter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value')!.set!;
			if (el.tagName === 'TEXTAREA') {
				nativeTextAreaSetter.call(el, payload);
			} else {
				nativeInputSetter.call(el, payload);
			}
			el.dispatchEvent(new Event('input', { bubbles: true }));
			el.dispatchEvent(new Event('change', { bubbles: true }));
		} else if (action === 'enter') {
			el.dispatchEvent(new KeyboardEvent('keydown', { bubbles: true, cancelable: true, keyCode: 13, key: 'Enter' }));
			el.dispatchEvent(new KeyboardEvent('keyup', { bubbles: true, cancelable: true, keyCode: 13, key: 'Enter' }));
		} else if (action === 'click') {
			const containers = ['article', 'section', 'main', 'aside', 'nav', 'header', 'footer', 'div', 'li', 'ul', 'ol'];
			if (containers.includes(el.tagName.toLowerCase())) {
				const clickable = el.querySelector('a, button, [role="button"]');
				if (clickable) el = clickable;
			}
			(el as HTMLElement).click();
		} else if (action === 'focus') {
			(el as HTMLElement).focus();
		}
	}, { macheID, action, payload });
}

/**
 * Scroll the page and return updated state.
 */
export async function scrollPage(
	page: Page,
	direction: string
): Promise<{
	summary: string;
	at_bottom: boolean;
	at_top: boolean;
	scroll_moved: boolean;
	scroll_y: number;
	scroll_height: number;
	viewport_height: number;
}> {
	return page.evaluate(async (dir) => {
		const preScrollY = window.scrollY;

		if (dir === 'up') {
			window.scrollBy({ top: -window.innerHeight, behavior: 'instant' });
		} else {
			const maxScroll = document.documentElement.scrollHeight - window.scrollY - window.innerHeight;
			const target = Math.min(1.5 * window.innerHeight, Math.max(maxScroll - 10, window.innerHeight));
			window.scrollBy({ top: target, behavior: 'instant' });
		}

		// Wait for content to settle.
		await new Promise(resolve => setTimeout(resolve, 500));

		(window as any).__xrayBuildRegistry();

		// Re-generate summary inline (same logic as requestSummary).
		const registry = (window as any).__xrayRegistry as Map<string, Element>;
		const reverse = (window as any).__xrayReverse as Map<Element, string>;
		const getPath = (window as any).__xrayGetPath;
		const getColor = (window as any).__xrayGetSemanticColor;

		let summary = "Interactive Elements:\n";
		let count = 0;
		const pageWidth = document.documentElement.scrollWidth || window.innerWidth;
		const pageHeight = document.documentElement.scrollHeight || window.innerHeight;

		for (const [macheId, node] of registry) {
			if (count >= 500) break;
			const tag = node.tagName.toLowerCase();
			const clone = node.cloneNode(true) as Element;
			clone.querySelectorAll('script, style').forEach(s => s.remove());
			const rawText = (clone.textContent || '').replace(/\s+/g, ' ').trim();
			let text = rawText.length > 1500 ? rawText.substring(0, 1500) + '...' : rawText;
			if (!text) text = node.getAttribute('aria-label') || node.getAttribute('title') || '';
			if (!text && tag === 'input') text = (node as HTMLInputElement).placeholder || 'input';
			if (!text && !node.children.length) continue;

			const rect = node.getBoundingClientRect();
			const x = (rect.left + window.scrollX) / pageWidth;
			const y = (rect.top + window.scrollY) / pageHeight;
			const w = rect.width / pageWidth;
			const h = rect.height / pageHeight;
			const bounds = `[${x.toFixed(3)}, ${y.toFixed(3)}, ${w.toFixed(3)}, ${h.toFixed(3)}]`;
			const color = getColor(node);
			const cs = getComputedStyle(node);
			const fontSize = parseFloat(cs.fontSize) || 0;
			const display = cs.display || 'block';
			const interactive = node.tabIndex >= 0 || ['a', 'button', 'input', 'select', 'textarea'].includes(tag);

			let parentID = 'none';
			let ancestor = node.parentElement;
			while (ancestor) {
				if (reverse.has(ancestor)) { parentID = reverse.get(ancestor)!; break; }
				ancestor = ancestor.parentElement;
			}
			summary += `ID: ${macheId} | Color: ${color} | Bounds: ${bounds} | Parent: ${parentID} | Tag: ${tag} | Text: "${text}" | Path: ${getPath(node)}` +
				` | FontSize: ${fontSize.toFixed(0)} | Display: ${display} | Interactive: ${interactive}\n`;
			count++;
		}

		const afterScrollY = window.scrollY;
		const scrollHeight = document.documentElement.scrollHeight;
		const viewportHeight = window.innerHeight;
		const atBottom = afterScrollY + viewportHeight >= scrollHeight - 5;
		const atTop = afterScrollY <= 5;
		const scrollMoved = Math.abs(afterScrollY - preScrollY) > 5;

		return {
			summary,
			at_bottom: atBottom,
			at_top: atTop,
			scroll_moved: scrollMoved,
			scroll_y: afterScrollY,
			scroll_height: scrollHeight,
			viewport_height: viewportHeight,
		};
	}, direction);
}
