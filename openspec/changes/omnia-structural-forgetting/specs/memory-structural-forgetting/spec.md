# Memory Structural Forgetting Specification

## Purpose

Living-memory layer for Omnia 0.2: memories optionally anchor to code (file + symbol + git-blame line range + blame SHA + content hash). A reconcile pass detects when anchored code materially changed, relocates anchors that merely moved ("travel"), and marks unrelocatable, materially-changed anchors stale — never deleting the memory. Stale memories are downranked at retrieval and surfaced for user review. Staleness candidate generation extends memory-conflict-semantic's `FindCandidates`/`ScanProject` seam via a new `anchor` `CandidateSource`.

## Requirements

### Requirement: Explicit Anchor Capture at Save

`mem_save` MAY accept `code_anchors: [{file, symbol, line_start, line_end}]`. When present, the system MUST resolve each entry's blame range, blame SHA (via `git blame -L`, `rev-parse`), and content hash, and MUST persist it as an anchor record linked 1:N to the memory. Omitting `code_anchors` MUST leave `mem_save` behavior unchanged. Auto-inferred anchors are out of scope. The system MUST resolve exactly one `repo_root` per project; multi-repo projects are out of scope for v1.

#### Scenario: Agent supplies a valid anchor
- GIVEN a git repo with the referenced file and symbol
- WHEN `mem_save` is called with `code_anchors`
- THEN an anchor (file, symbol, line range, blame SHA, content hash) is stored linked to the memory

#### Scenario: Save without anchors is unaffected
- GIVEN a `mem_save` call with no `code_anchors`
- WHEN the save completes
- THEN no anchor is created and behavior is identical to before this feature

### Requirement: Graceful Degradation Without Git

If `git` is not on PATH or the working directory is not inside a git repository, the system MUST skip anchor capture and MUST NOT fail `mem_save`.

#### Scenario: No git binary or not a repo
- GIVEN `git` is missing, or the directory is not part of a git repo
- WHEN `mem_save` is called with `code_anchors`
- THEN the memory saves successfully, no anchor is created, and no error is surfaced

### Requirement: Reconcile Pass Detects Changed Anchors

`omnia forget-scan` MUST re-blame each active anchor and compare its current content hash to the stored hash. A hash mismatch MUST be classified via Anchor Travel first, then materiality: any non-whitespace change within the range is material by default (conservative).

#### Scenario: Unchanged anchor is skipped
- GIVEN an anchor whose current hash matches the stored hash
- WHEN `forget-scan` runs
- THEN the anchor stays `active` and no memory is downranked

### Requirement: Anchor Travel Before Staling

Before staling a changed anchor, the system MUST attempt to relocate it by symbol name and content hash. If the same body is found at a different range (same file or repo), the system MUST update `line_start`/`line_end` in place (status `traveled`) instead of staling.

#### Scenario: Refactor moves a function unchanged
- GIVEN an anchored function moved to a new line range with unchanged body
- WHEN `forget-scan` runs
- THEN the anchor's range is updated and it remains active/traveled, not stale

#### Scenario: Symbol no longer found
- GIVEN an anchor whose symbol can't be located anywhere in the repo
- WHEN `forget-scan` runs
- THEN relocation fails and the anchor proceeds to staleness evaluation

### Requirement: Staleness Marking Never Hard-Deletes

A materially-changed, unrelocatable anchor MUST be marked `stale` (the memory and the anchor row are never deleted) and the memory's `review_after` MUST be set to now. A system-provenance `supersedes` relation row MUST be written ONLY when a newer memory already covers the same subject.

#### Scenario: Stale with no newer memory
- GIVEN a stale-eligible anchor and no newer memory on the subject
- WHEN it is marked stale
- THEN status becomes `stale`, `review_after` is set to now, and no `supersedes` row is created

#### Scenario: Stale with a newer memory present
- GIVEN a stale-eligible anchor and an existing newer memory on the same subject
- WHEN it is marked stale
- THEN a system-provenance `supersedes` row links the newer memory, in addition to the stale marking

### Requirement: Retrieval Downranks Stale Memories

`mem_search` MUST apply a downranking penalty to memories with a `stale` anchor, applied after recall fusion, and MUST include a receipt line naming the changed anchor (file, line range, old-to-new SHA).

#### Scenario: Stale memory ranks lower
- GIVEN two equally-relevant memories, one with a stale anchor and one without
- WHEN `mem_search` runs
- THEN the staled memory ranks lower and its result includes an "anchor changed" receipt line

### Requirement: Review Surfacing for Stale Memories

Memories whose `review_after` was set by anchor staleness MUST appear in the existing review flow with an explicit "still true? keep/forget" prompt.

#### Scenario: User reviews a staled memory
- GIVEN a memory marked stale with `review_after` set to now
- WHEN the user runs the review flow
- THEN the memory appears with a code-changed keep/forget prompt

### Requirement: Anchor Candidates Ride the Existing Seam

Staleness candidate generation MUST be implemented as a new `CandidateSource` (`anchor`) within the existing `FindCandidates`/`ScanProject` seam, reusing its worker pool and system-provenance write path — not a separate scanner. `ScanProject{Source: anchor}` MUST report counts of anchors checked, traveled, and staled.

#### Scenario: ScanProject with anchor source reports counters
- GIVEN a project with active anchors, some changed and some unchanged
- WHEN `ScanProject` runs with `Source: anchor`
- THEN the result reports checked/traveled/staled counts consistent with the outcomes
