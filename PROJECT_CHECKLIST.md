# X-Ray — Gemini Live Agent Challenge Submission Checklist

## Devpost Steps (2/5 done)

- [x] Project overview
- [x] Project details
- [ ] Additional info (in progress)
- [ ] Submit
- [ ] Final review

## Code & Repo

- [x] Public repo: https://github.com/agentic-research/x-ray
- [x] README with reproducible testing instructions (`task test`)
- [x] Architecture docs with mermaid diagrams (`docs/ARCHITECTURE.md`)
- [x] Test suite — 42 tests, no Gemini key needed
- [x] CI via GitHub Actions (`.github/workflows/ci.yml`)
- [x] Clean git history (no co-author attribution)
- [x] Pushed to remote

## Devpost: Additional Info Fields

| Field | Status | Value |
|-------|--------|-------|
| Upload a File | **TODO** | Screen recording of voice flow? Or zip of extension? |
| Submitter Type | done | Individual |
| Country | done | United States |
| Category | done | UI Navigator |
| Start date | done | 02-22-26 |
| Code repo URL | done | https://github.com/agentic-research/x-ray |
| Reproducible testing? | done | Yes |
| GCP deployment proof | done | https://github.com/agentic-research/x-ray/tree/main/deploy |
| Architecture diagram location | **TODO** | Check "Code repo" — mermaid in docs/ARCHITECTURE.md. Consider rendering a PNG for image carousel too. |
| Blog/content (bonus 0.6) | **IN PROGRESS** | Written, unpublished. Needs: publish + add hackathon disclaimer + URL |
| Automated deployment (bonus 0.2) | done | https://github.com/agentic-research/x-ray/blob/main/deploy/deploy.sh |
| GDG profile (bonus 0.2) | done | https://gdg.community.dev/u/mgxmee/#/about |

## Remaining Action Items

### Must Do (before submit)
- [ ] **Update Devpost description to mention Accessibility Tree Enrichment** — the ARCHITECTURE.md and code include CDP AX tree capture, but Devpost project details don't reference it yet. Add a paragraph about how the extension captures the browser's computed accessibility tree via `chrome.debugger` + CDP, enriching the DOM summary with semantic roles and computed names (not just raw `aria-*` scraping). This is a differentiator — judges should see it.
- [ ] Publish blog post — add line: "Created for the Gemini Live Agent Challenge hackathon" + #GeminiLiveAgentChallenge
- [ ] Paste blog URL into Devpost bonus field
- [ ] Upload file — screen recording of voice demo, or a zip
- [ ] Select architecture diagram checkbox(es) — at minimum "Code repo"
- [ ] Fill remaining Devpost fields and hit Submit

### Nice to Have
- [ ] Render mermaid architecture diagram to PNG for image carousel upload
- [ ] Record short GCP console screen recording (Cloud Run service running) as stronger deployment proof
- [ ] Test the AX tree enrichment end-to-end on a live page (new CDP code hasn't been browser-tested yet)
- [ ] Social media post with #GeminiLiveAgentChallenge linking to blog

## Bonus Points Tracker

| Bonus | Max | Status |
|-------|-----|--------|
| Blog/content | 0.6 | Written, needs publish + URL |
| Automated deployment | 0.2 | Done (deploy.sh + main.tf) |
| GDG profile | 0.2 | Done |
| **Total potential** | **1.0** | |

## Tech Debt (Post-Submission)

- AX tree enrichment (B workstream) is code-complete but untested in browser
- `captureAndSend` is now async — verify no regressions in snapshot flow
- Voice `setMic(true)` has a `setTimeout` race in one-click flow (the background.js diff)
- Integration tests use inline testdata — could load from testdata/ files instead
