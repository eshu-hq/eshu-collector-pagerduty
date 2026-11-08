# Decisions

This folder holds architecture decision records (ADRs) for eshu-collector-pagerduty: short notes that capture one decision, why it was made, and what it costs us. They save future readers from re-litigating settled questions.

## Index

No architecture decision records are recorded in this repo yet. Decisions that live in code comments, the manifest, or Eshu core issues are linked from the pages that rely on them.

## Format

Name files `NNNN-short-slug.md`, numbered in order, and keep each one to this shape:

```markdown
# ADR-NNNN: Short title

Status: Proposed | Accepted | Superseded by ADR-MMMM
Date: YYYY-MM-DD

## Context

What forced a decision, in plain terms.

## Decision

What we chose.

## Consequences

What gets easier, what gets harder, and what we must now keep true.
```

Add a row to the index above in the same change. An ADR is only added for a decision that is actually recorded — do not back-fill ADRs for choices nobody wrote down.
