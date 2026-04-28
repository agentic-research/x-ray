# WebArena Evaluation Subsets

Task file: `testdata/webarena_tasks.json` — 204 tasks (187 shopping + 17 cross-site).
Full WA-Verified dataset: 812 tasks across 6 sites.

## Site Coverage

| Site | Port | Tasks in file | Container |
|------|------|--------------|-----------|
| shopping | :7770 | 190 | `webarena_verified_shopping` |
| shopping_admin | :7780 | 5 | `webarena_verified_shopping_admin` |
| reddit | :9999 | 5 | `webarena_verified_reddit` |
| gitlab | :8023 | 7 | `webarena_verified_gitlab` |
| map | :3000 | 5 | `webarena_verified_map` |
| wikipedia | :8888 | 3 | `webarena_verified_wikipedia` |

## Recommended Subsets

### Cross-Site Proof (20 tasks, all 6 sites, ~40 min)
Proves the full infrastructure works. 3 tasks per single-site, 1 per cross-site combo.

```bash
scripts/webarena_run.sh 0,1,2,7,8,9,21,22,23,27,28,29,44,45,46,97,552,556,671,759
```

Containers needed: ALL (shopping, shopping_admin, reddit, gitlab, map, wikipedia).

### Shopping Representative (48 tasks, ~1.5 hr)
One task per intent template. Best coverage-to-time ratio for shopping.

```bash
scripts/webarena_run.sh 21,47,96,117,118,124,141,146,158,163,188,225,226,231,238,260,269,274,279,283,284,298,313,319,324,329,334,351,358,368,376,384,386,387,431,436,465,506,509,511,516,521,528,571,585,653,689,794
```

### Quick Smoke Test (15 tasks, ~25 min)
Shopping only, fast iteration.

```bash
scripts/webarena_run.sh 21,22,23,24,25,26,47,48,158,159,160,161,362,431,432
```

### Full Shopping (187 tasks, ~5 hr)

```bash
scripts/webarena_run.sh shopping
```
