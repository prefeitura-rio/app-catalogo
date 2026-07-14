# Search Relevance Evaluation

This project includes a local evaluation harness for comparing search behavior against explicit relevance judgments. It sends the public POST JSON query contract used by the API, matches returned documents by `(source, source_id)`, and writes a machine-readable report suitable for local comparison or CI artifacts.

The harness measures a retrieval configuration; it does not choose ranking weights, create judgments, or infer relevance from clicks. Keep the dataset, endpoint version, catalog snapshot, and run configuration stable when comparing runs.

## Run locally

Start an approved local or staging API, prepare a JSONL dataset, and run:

```sh
go run ./cmd/search-eval -dataset ./testdata/search-eval.jsonl -endpoint http://localhost:8080/api/public/search -quality-policy ./quality-policy.json -output ./search-eval-report.json
```

Run `go run ./cmd/search-eval -help` for the executable defaults and resource
bounds; the report records the effective configuration used by each run.

The harness sends no authorization header and never follows redirects. Plain HTTP is accepted only for literal loopback addresses or `localhost`; remote and staging endpoints must use HTTPS.

The flags are part of the evaluation record:

| Flag | Meaning |
|---|---|
| `-dataset` | Input JSONL path. |
| `-endpoint` | Search endpoint queried by the harness. |
| `-output` | Destination for the JSON report. |
| `-timeout` | Per-request deadline. |
| `-concurrency` | Maximum number of in-flight requests. |
| `-candidate-limit` | Retrieval depth requested from the endpoint. |
| `-cutoffs` | Comma-separated ranking cutoffs used by relevance metrics. |
| `-latency-quantiles` | Comma-separated quantiles included in the latency summary. |
| `-continue-on-error` | Whether HTTP transport and response-contract failures may produce a successful process exit with `complete: false`; dataset validation is always fatal before the network. |
| `-run-timestamp` | Fixed RFC 3339 timestamp written to the report for reproducible serialization. It does not change the endpoint clock. |
| `-max-response-bytes` | Maximum accepted HTTP response size. |
| `-max-dataset-line-bytes` | Maximum accepted JSONL record size. |
| `-quality-policy` | Optional path to the complete, versioned JSON acceptance policy. Supplying it makes every policy failure fatal. |

Every effective execution bound is serialized in `run_configuration`. When a quality policy is supplied, its schema version, artifact-declared identity and version, and SHA-256 of the exact bytes read are serialized in `quality_policy`. A comparison is not valid when catalog state, eligibility time, endpoint code, dataset, or policy changed without being recorded.

## Executable quality policy

The policy contract is defined by [the JSON Schema](search-quality-policy.schema.json). [The accompanying example](search-quality-policy.example.non-production.json) is deliberately marked non-production and contains illustrative values only; it is not an approved promotion policy. Derive production thresholds from an explicitly reviewed baseline and holdout decision, then preserve that exact artifact as an immutable input.

The loader is fail-closed. It reads within a fixed byte bound, requires UTF-8 and one JSON object, rejects duplicate keys at any nesting level, rejects unknown fields and trailing content, and accepts only the declared schema version. The artifact must contain stable `id` and `version` values, a non-`unversioned` catalog revision, at least one allowed runtime pipeline, a requirement for a complete report, a requirement for zero degraded responses, and a bounded list of uniquely identified assertions.

Assertions target either `overall` or one named `slice`. The supported metrics and selectors are:

| Metric | Required selector | Valid comparison |
|---|---|---|
| `recall`, `mrr`, `ndcg`, `judged_rate` | `cutoff` present in the report | `gte` or `lte` |
| `zero_result_rate`, `failure_rate` | No selector | `gte` or `lte` |
| `latency_ms` | `quantile` present in the report | `gte` or `lte` |

An absent cutoff, quantile, or slice fails its assertion instead of silently substituting zero. A non-finite observed metric also fails and is omitted from the serialized `actual` field so the gate artifact remains valid JSON. Assertion results are ordered by assertion ID, requirement results have a fixed order, and each result carries a stable code suitable for automation.

The report contains only the policy reference and the bounded `quality_gate` result. It never copies the complete policy, query text, returned documents, or response bodies. A failed gate always produces a non-zero process status after writing the report, including when `-continue-on-error=true`; continuation applies only to endpoint collection failures and cannot waive promotion criteria.

## Dataset contract

The dataset is UTF-8 JSONL. Every line is one independent query judgment record; blank lines are invalid. Keep records single-line so validation failures can identify the offending input line without parsing a larger document.

Example:

```json
{"query_id":"animal-vaccine-dog","query":"vacina para cachorro","types":["service"],"filters":{"bairro":"Centro"},"slices":["animal-health","short-query"],"qrels":[{"entity_id":"animal-vaccine","grade":3,"documents":[{"source":"salesforce","source_id":"current-vaccine"},{"source":"typesense","source_id":"legacy-vaccine"}]},{"entity_id":"health-center","grade":1,"documents":[{"source":"salesforce","source_id":"health-center"}]},{"entity_id":"unrelated-license","grade":0,"documents":[{"source":"salesforce","source_id":"license"}]}]}
```

The record schema is:

| Field | Required | Contract |
|---|---|---|
| `query_id` | Yes | Stable, unique, non-sensitive identifier for the judged intent. It is the only query identifier written to reports. |
| `query` | Yes | Query text sent to the search endpoint. Treat it as sensitive input. |
| `types` | No | Search verticals requested through the API `types` parameter. Omission preserves endpoint defaults. |
| `filters` | No | Object whose keys and values follow the public search filter contract. Omission means no additional filters. |
| `slices` | No | Stable, non-sensitive labels used for segmented metrics, such as intent family, vertical, language characteristic, or query shape. |
| `qrels` | Yes | Array of canonical entities with a grade and one or more source-document aliases. |

Each qrel entity has this schema:

| Field | Required | Contract |
|---|---|---|
| `entity_id` | Yes | Unique stable identifier for the canonical citizen-facing entity. |
| `grade` | Yes | Integer in the schema scale: `0` not relevant, `1` marginally relevant, `2` relevant, `3` highly relevant. |
| `documents` | Yes | Non-empty array of aliases, each containing a valid API `source` and its exact `source_id`. |

Document identity is the pair `(source, source_id)`, never the database UUID, title, URL, or rank. The same document pair may occur only once in the entire query record and cannot belong to two entities. An entity may list multiple aliases when migrations or upstream systems expose the same citizen-facing entity through different sources. The accepted sources are the values emitted by the catalog API: `salesforce`, `typesense`, `courses`, `jobs`, `mei`, and `app-go-api`.

Every query must contain a positively graded entity. Keep the grade rubric stable between compared runs.

Documents absent from `qrels` are unjudged, not editorially proven irrelevant. The metric calculation gives them no gain while leaving them in their returned rank, so incomplete judgment pools can still bias results. Add explicit zero-grade judgments when a document was reviewed and found irrelevant, and keep judgment-pool construction consistent between compared runs.

Filters are part of the intent. A document relevant without filters can be ineligible for a filtered query and must not receive credit merely because its text matches. The endpoint remains responsible for enforcing active, expiry, type, and filter eligibility.

## Metric semantics

For each configured cutoff, the report macro-averages metrics over successfully evaluated queries:

- `recall` is the fraction of positively graded entities represented by any alias in the returned list up to that cutoff.
- `mrr` is the reciprocal rank of the first positive qrel in the returned list, or zero when none appears by the cutoff.
- `ndcg` uses exponential graded gain and logarithmic rank discount, normalized by the ideal ordering of all positive qrels for that query.
- `judged_rate` is the fraction of rank positions up to the cutoff that contain a document matching any explicit qrel alias, including zero-grade entities. Missing results contribute no judged document.
- `judged_queries` records the denominator used by those averages.

The first returned alias for an entity receives its grade. Later aliases of that same entity remain in their original rank positions and count as judged, but receive no additional gain or recall credit. This makes duplicate source representations visible instead of improving the metric by silently deduplicating the ranking.

The HTTP endpoint exposes its final fused, optionally reranked list. Each retriever canonicalizes aliases before its candidate limit, and semantic retrieval uses a bounded ANN alias overfetch before that canonicalization. Consequently, `recall` describes the final list; it is neither exhaustive ANN recall nor candidate recall of internal pre-fusion or pre-rerank pools. Measuring those internal stages requires an in-process evaluator or a separately authorized diagnostic surface that exposes stable document identifiers without query content.

Ranked pages are projections of one cached final-order snapshot whose key excludes pagination. Evaluation requests must still ask for a complete bounded first page: later pages test transport stability, not an independent ranking run. The ranker descriptor records the semantic overfetch factor and the complete non-secret HyDE generation contract, so changing either prevents unlike runs from being averaged under one descriptor hash.

The evaluator requires a complete first page: the item count must equal the smaller of the declared `total` and requested candidate limit. Every item must carry a unique `entity-v1:<sha256>` `canonical_id` and a unique valid `(source, source_id)` pair. A truncated page, missing or malformed canonical identity, or duplicate canonical entity is a response-contract failure rather than a smaller valid result set.

`zero_result_rate` uses all successful queries. Failed queries are counted separately and excluded from both quality and latency aggregates.

## Report contract

The report is deterministic for the same validated dataset, injected run timestamp, endpoint observations, and flag values. Object keys, failure records, ranker versions, cutoffs, quantiles, and slice names are emitted in stable order.

The top-level fields are:

| Field | Meaning |
|---|---|
| `schema_version` | Version of the serialized report contract. |
| `run_timestamp` | UTC timestamp supplied by the injected clock or CLI override. |
| `complete` | Whether the run contains no transport, response-contract, or global ranker-version failures. |
| `endpoint` | Canonical public endpoint without request query parameters. |
| `dataset_hash` | SHA-256 of the exact JSONL bytes read by the harness. |
| `run_configuration` | POST transport and the effective timeout, concurrency, candidate depth, metric cutoffs, latency quantiles, continuation behavior, and input byte bounds. |
| `quality_policy` | Optional schema version, immutable identifier and version, and SHA-256 of the exact acceptance-policy artifact. |
| `quality_gate` | Optional bounded, deterministic requirement and assertion results. Present whenever `-quality-policy` is supplied. |
| `ranker_versions` | Sorted versions observed in successful responses. Multiple entries cause the global `mixed_ranker_version` failure. |
| `ranker_descriptor_hashes` | Sorted SHA-256 digests of the complete ranker descriptors. Multiple entries make the run incomplete. |
| `catalog_revisions` | Sorted content-and-eligibility snapshot identifiers observed in successful responses. A revision includes the next database-observed `valid_from` or `valid_until` boundary; crossing that boundary during a run produces multiple entries and makes the run incomplete. `unversioned` is explicit evidence that snapshot reproducibility has not yet been wired. |
| `effective_pipelines` | Sorted stages that actually produced returned orders; cache transport is not a ranking stage. |
| `degraded_responses` | Count of successful responses that used a runtime fallback. Any non-zero count makes the run incomplete. |
| `failures` | Bounded HTTP categories keyed by `query_id`, plus global contract failures; underlying errors and response bodies are excluded. |
| `overall` | Quality, zero-result, failure, and latency aggregates across valid queries. |
| `by_slice` | The same aggregates segmented by every declared slice. |

Metric maps use the configured cutoff and quantile labels. The report intentionally omits per-query rankings and `search_id`: contract validation happens during collection, while the durable artifact retains only the minimum information needed for comparison.

## Train and holdout discipline

Maintain separate train and holdout JSONL files. Use the train set for model selection, query expansion rules, fusion weights, candidate depths, and error analysis. Keep the holdout sealed until a candidate configuration and its decision criteria have been fixed.

Prefer a forward temporal split: training judgments come from an earlier collection window and holdout judgments from a later window. This exposes catalog drift, vocabulary drift, and new intents that a random row split can hide. Evaluate against a controlled catalog snapshot and endpoint clock when expiry or availability can change eligibility; `-run-timestamp` labels the report but does not freeze server time.

Group paraphrases, spelling variants, repeated sessions, and queries targeting the same canonical intent before splitting. Every member of an intent group belongs to the same partition. Otherwise, memorized synonyms or near-duplicate judgments leak from train into holdout.

Do not inspect holdout query-level failures while tuning. Once holdout results influence a ranking change, that set has become training evidence; freeze the decision record and create a new future holdout for the next cycle. Report aggregate and slice results for both partitions so a global improvement cannot conceal harm to a vertical or intent family.

## Click data is not a qrel substitute

Clicks are affected by rank position, presentation, device, prior exposure, document popularity, abandonment, and whether the citizen completed the task elsewhere. A non-click is not evidence that a result was irrelevant, and a click on the first visible result is not an unbiased preference judgment.

Use click and reformulation data to discover candidate intents, build judgment pools, and identify slices needing review. Relevance grades should come from an explicit rubric and qualified review. If click-derived preferences are used for learning or online comparison, preserve assignment and propensity information and analyze them separately from editorial qrels.

## Privacy and report handling

Raw queries are allowed only in the controlled input dataset and the request sent to the approved endpoint. They may contain names, addresses, health concerns, identifiers, or other personal information.

The generated report must never include raw query text, response titles, descriptions, snippets, metadata, or raw response bodies. Queries are sent only in the bounded POST body and never in the request URL. Use `query_id`, aggregate metrics, non-sensitive slices, status categories, and latency summaries. Error records must identify a query by `query_id` and a bounded error category; they must not echo request or response content.

Do not place personal information in `query_id` or `slices`. Keep datasets out of logs and unapproved artifact stores, sanitize sampled production queries before judgment, apply the project's retention policy, and restrict access to the smallest necessary group. Verify endpoint logging separately because a privacy-safe harness cannot prevent the target service from logging its request.

## Failure behavior

The complete dataset is validated before the first network request. Invalid JSONL, duplicate identifiers, invalid entity aliases, unsupported filters, invalid grades, missing positive judgments, and oversized records always stop the command with a non-zero status, regardless of `-continue-on-error`. No HTTP request is issued and no partial relevance report is produced.

Without `-continue-on-error`, an HTTP timeout, transport failure, non-success status, oversized response, malformed response JSON, invalid `(source, source_id)`, or another response-contract violation makes the completed run exit with a non-zero status. HTTP evaluations are collected through the bounded worker pool before the failure status is returned, allowing the report to retain bounded categories without exposing request content.

With `-continue-on-error=true`, those HTTP failures are recorded by `query_id`, the affected query is excluded from relevance and latency aggregates, and the process may exit successfully with `complete: false`. Dataset failures are never recoverable.

An executable quality policy is never recoverable through `-continue-on-error`. The harness writes the complete aggregate report and bounded gate result, then exits non-zero when any runtime requirement, metric threshold, referenced cutoff, latency quantile, or slice is not satisfied.

Mixed `ranker_version`, ranker descriptor, or catalog revision values across otherwise successful responses add global contract failures. A response marked `degraded` also makes the run incomplete so a provider or reranker outage cannot be evaluated as the nominal ranker. The run exits non-zero by default; explicit continuation may retain it as a diagnostic report, but different versions, snapshots, or degraded executions must never be averaged as one comparable ranker.

Consumers must inspect `complete` rather than treating a successful process exit as a complete ranking evaluation. An incomplete run must not be treated as a ranking pass or compared as if the failed queries had zero relevance.

Failures that prevent a meaningful report remain fatal regardless of the continuation flag: unreadable input, invalid global configuration or flags, inability to write the output, and any invalid dataset. They stop the run with a non-zero exit status.

The harness must never convert endpoint failures, missing results caused by decoding errors, or truncated responses into valid empty result sets. Resource bounds from the CLI flags are enforced before unbounded input or response data is retained in memory.

## Rollout after offline evaluation

Offline improvement is necessary but does not prove that live citizens benefit. Roll out a selected candidate in stages with the baseline preserved behind an immediate rollback flag.

In shadow mode, execute the candidate for the same eligible request but continue serving the baseline. Record only privacy-safe aggregates and stable document identifiers needed to compare eligibility, candidate coverage, rank movement, latency, and failures. Shadow traffic must not create citizen-visible side effects, mutate personalization state, or populate production caches under the baseline namespace.

After shadow correctness and operational behavior are acceptable, use randomized interleaving when a click-based online comparison is appropriate. Balanced or team-draft interleaving should preserve the same eligibility filters, deduplicate identical documents, record assignment, and avoid systematically favoring either ranker. Analyze interleaving preferences alongside latency, zero-result, reformulation, completion, and vertical-slice guardrails; click wins alone remain subject to presentation bias.

Only then expose the candidate directly through a reversible canary. Keep ranking, embedding, fusion, and cache versions in the evaluation and runtime metadata so an observed change can be attributed and rolled back without rebuilding unrelated parts of the catalog.
