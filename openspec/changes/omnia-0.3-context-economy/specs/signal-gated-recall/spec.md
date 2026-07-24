# Signal-Gated Recall Specification

## Purpose

Triggers an automatic recall/search from the prompt-submission hook layer when the user's new prompt shows a new-topic or uncertainty signal, so relevant memory surfaces proactively without firing on every turn, reusing the existing dedup-marker idiom, and degrading silently if the omnia CLI is unavailable.

## Requirements

### Requirement: Signal Detection Gates Recall Invocation

The prompt-submit hook MUST detect trigger signals — at minimum a new-topic signal and an uncertainty signal — and invoke recall only when a signal is detected.

#### Scenario: New-topic signal triggers recall
- GIVEN the user's new prompt introduces a topic with no related recent in-session context
- WHEN signal-gated-recall is enabled and the new-topic signal is detected
- THEN the hook invokes recall and injects relevant results

#### Scenario: Uncertainty signal triggers recall
- GIVEN the user's prompt contains an uncertainty expression matched by the configured heuristic
- WHEN signal-gated-recall is enabled
- THEN the hook invokes recall

### Requirement: Must Not Fire Every Turn

Turns without a detected trigger signal MUST NOT invoke recall.

#### Scenario: No-signal turn is a no-op
- GIVEN a prompt with no detected new-topic or uncertainty signal
- WHEN signal-gated-recall is enabled
- THEN no recall is invoked for that turn and no additional latency is introduced

### Requirement: Session Dedup via Existing Marker Idiom

The hook MUST reuse the existing dedup-marker idiom (as used by post-tool-error-recall.sh) so the same observation(s) are not injected redundantly more than once per session.

#### Scenario: Second trigger does not re-inject
- GIVEN a signal already caused an observation to be injected once in the session
- WHEN a subsequent trigger in the same session would surface the same observation
- THEN the dedup marker prevents re-injecting it

### Requirement: Silent Degradation on CLI Unavailability

When the omnia CLI is unavailable or errors, the hook MUST degrade silently: no crash, no error surfaced to the user or agent turn, and no blocking of prompt submission.

#### Scenario: CLI missing does not block the turn
- GIVEN the omnia CLI binary is missing or returns an error
- WHEN a trigger signal fires
- THEN the hook fails silently and the user's prompt submission proceeds unaffected

### Requirement: Injected Content Still Passes Budget/Diversity Gates

Content injected as a result of a fired signal MUST still be subject to injection-budget and injection-diversity when those features are enabled — signal-gated-recall is a new trigger for the existing search path, not a bypass of it.

#### Scenario: Fired signal still respects budget
- GIVEN signal-gated-recall fires and injection-budget is also enabled
- WHEN results are returned
- THEN they are trimmed by injection-budget the same way as any other triggered search

### Requirement: Disabled by Default, No-Op When Off

The feature MUST be controlled by a dedicated config flag, default OFF. When OFF, the prompt-submit hook's behavior MUST be identical to its pre-v0.3 behavior (save-nudge only, no auto-search).

#### Scenario: Flag off preserves current behavior
- GIVEN the signal-gated-recall flag is OFF
- WHEN any prompt is submitted
- THEN the hook behaves identically to pre-v0.3 (nudge-to-save only, no auto-search triggered)
