// ax-mapper.ts — Maps Puppeteer's raw CDP AX tree to x-ray's enriched summary format.

import type { Page } from '@cloudflare/puppeteer';

interface AXNode {
	nodeId: string;
	backendDOMNodeId?: number;
	role?: { value: string };
	name?: { value: string };
	properties?: Array<{ name: string; value: { value: any } }>;
}

interface MacheAXInfo {
	role: string;
	name: string;
	properties: string[];
}

/**
 * Get the full AX tree via raw CDP, join to mache IDs, and return the
 * enriched summary (same format as cdp.EnrichSummaryWithAX in Go).
 */
export async function getEnrichedAXTree(page: Page): Promise<string> {
	const client = (page as any)._client;
	if (!client) return '';

	// Get the summary first so we can enrich it.
	const summary = await page.evaluate(() => {
		(window as any).__xrayBuildRegistry();
		// Re-use the summary generation.
		const registry = (window as any).__xrayRegistry as Map<string, Element>;
		const reverse = (window as any).__xrayReverse as Map<Element, string>;
		const getPath = (window as any).__xrayGetPath;
		const getColor = (window as any).__xrayGetSemanticColor;

		let result = "Interactive Elements:\n";
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
			const zIndex = cs.zIndex;
			const opacity = parseFloat(cs.opacity);
			const area = rect.width * rect.height;
			const textDensity = area > 0 ? Math.min(1.0, text.length / (area / 1000)) : 0;
			const interactive = node.tabIndex >= 0 || ['a', 'button', 'input', 'select', 'textarea'].includes(tag);

			let parentID = 'none';
			let ancestor = node.parentElement;
			while (ancestor) {
				if (reverse.has(ancestor)) { parentID = reverse.get(ancestor)!; break; }
				ancestor = ancestor.parentElement;
			}
			result += `ID: ${macheId} | Color: ${color} | Bounds: ${bounds} | Parent: ${parentID} | Tag: ${tag} | Text: "${text}" | Path: ${getPath(node)}` +
				` | FontSize: ${fontSize.toFixed(0)} | Display: ${display} | Interactive: ${interactive} | TextDensity: ${textDensity.toFixed(2)}` +
				` | ZIndex: ${zIndex} | Opacity: ${opacity.toFixed(2)}\n`;
			count++;
		}
		return result;
	});

	// Try raw CDP AX tree for enrichment.
	try {
		const axResult = await client.send('Accessibility.getFullAXTree');
		const axNodes: AXNode[] = axResult.nodes || [];

		// Build backendDOMNodeId → AX info map.
		const backendToAX = new Map<number, MacheAXInfo>();
		for (const node of axNodes) {
			if (!node.backendDOMNodeId) continue;
			const role = node.role?.value || '';
			if (role === 'none' || role === 'GenericContainer') continue;
			const name = node.name?.value || '';
			const props: string[] = [];
			for (const p of node.properties || []) {
				if (['disabled', 'expanded', 'checked', 'selected'].includes(p.name)) {
					props.push(`${p.name}=${p.value.value}`);
				}
			}
			backendToAX.set(node.backendDOMNodeId, { role, name, properties: props });
		}

		// Get mache → backendNodeId mapping via DOM.
		const docResult = await client.send('DOM.getDocument', { depth: 0 });
		const rootNodeId = docResult.root.nodeId;
		const qaResult = await client.send('DOM.querySelectorAll', {
			nodeId: rootNodeId,
			selector: '[data-mache-id]',
		});
		const nodeIds: number[] = qaResult.nodeIds || [];

		const macheToBackend = new Map<string, number>();
		for (const nid of nodeIds) {
			try {
				const desc = await client.send('DOM.describeNode', { nodeId: nid });
				const backendId = desc.node?.backendNodeId;
				const attrs = desc.node?.attributes || [];
				let macheId = '';
				for (let i = 0; i < attrs.length; i += 2) {
					if (attrs[i] === 'data-mache-id') {
						macheId = attrs[i + 1];
						break;
					}
				}
				if (macheId && backendId) {
					macheToBackend.set(macheId, backendId);
				}
			} catch (_) {
				// Node may have been removed.
			}
		}

		// Join: mache → backend → AX.
		const macheAX = new Map<string, MacheAXInfo>();
		for (const [macheId, backendId] of macheToBackend) {
			const ax = backendToAX.get(backendId);
			if (ax) macheAX.set(macheId, ax);
		}

		// Enrich summary lines.
		if (macheAX.size === 0) return summary;

		const lines = summary.split('\n');
		const idRe = /^ID:\s*(mache-\d+)/;
		for (let i = 0; i < lines.length; i++) {
			const m = idRe.exec(lines[i]);
			if (!m) continue;
			const ax = macheAX.get(m[1]);
			if (!ax) continue;
			let suffix = ` | AXRole: ${ax.role}`;
			if (ax.name) suffix += ` | AXName: "${ax.name}"`;
			if (ax.properties.length > 0) suffix += ` | AXProps: ${ax.properties.join(',')}`;
			lines[i] += suffix;
		}
		return lines.join('\n');
	} catch (e) {
		// AX tree not available — return unenriched summary.
		console.log('AX tree enrichment failed:', e);
		return summary;
	}
}
