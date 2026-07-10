export const meta = {
  name: 'review-panel-verify',
  description: 'Verify each finding with an independent checker, then judge the assembled review against the bar',
  phases: [
    { title: 'Check', detail: 'one independent checker per finding, cross-family where live' },
    { title: 'Bar', detail: 'one judge over the assembled review, when triggered or forced' },
  ],
}

export const defaults = {
  // Worker policy — declared once, inherited by every agent() call. Checkers
  // and the bar judge are verification legs: cross-family review work on the
  // Codex review tier at the resting level, with the Claude entries governing
  // only the recorded degradation path when a Codex call does not return.
  // Pinned because an unpinned call rides the ambient default model silently.
  // NOTE: the runtime's extractor reads this block only when it directly
  // follows meta — whitespace between them, nothing else.
  codex: { model: 'gpt-5.6-sol', effort: 'medium' },
  claude: { model: 'opus', effort: 'medium', fallbackModel: 'sonnet' },
}

// args is the verification stage's input, assembled by scripts/
// assemble_verify_input.py from the plan, the review workflow's result, and the
// quote validator's output:
//
//   {
//     plan,        // the discovery plan (repo root, base_ref, brief paths, method paths)
//     findings,    // quote-validated findings — the checkers' input
//     coverage,    // the dispatch-derived coverage account from the review workflow
//     skipped,     // briefs that did not run, with reasons
//     failures,    // reviewer shards that returned nothing
//     reviews,     // per-shard reviewer notes (the bar's material on a clean review)
//     sweep_advisories, mode, pr,          // ride through to the final report
//     suppressed_by_validator,             // what the quote gate removed — the bar sees these
//     quote_validation,                    // the quote gate's counts
//     verify,      // boolean — run the per-finding checkers (default true)
//     bar_mode,    // "off" | "on" | "auto" (default "off")
//   }
//
// The return value is the run's single, complete report: everything the skill
// needs to post and summarise, so nothing has to be re-joined across files.
//
// Like the review workflow, this script is sandboxed: everything it needs
// arrives through args, and the static method files (finding-check.md,
// bar-check.md, reviewer-method.md) are read by the agents themselves from the
// paths the plan carries.
let input
try {
  input = typeof args === 'string' ? JSON.parse(args) : args
} catch (err) {
  throw new Error('review-panel-verify: args was a string but not valid JSON; nothing ran.')
}
const plan = input && input.plan
if (!plan) {
  throw new Error(
    'review-panel-verify: args did not contain a "plan". Expected the discovery plan; got keys: ' +
      (input ? Object.keys(input).join(', ') || '(none)' : String(input)),
  )
}
if (!plan.checker_method_path || !plan.bar_method_path || !plan.template_path) {
  throw new Error(
    'review-panel-verify: plan is missing method-file paths — re-run plan_review.py ' +
      '(the planner and this workflow must be the same version).',
  )
}
const root = plan.repo_root
const findings = input.findings || []
const coverage = input.coverage || []
const skipped = input.skipped || []
const failures = input.failures || []
const reviews = input.reviews || []
const suppressedByValidator = input.suppressed_by_validator || []
const quoteValidation = input.quote_validation || null
const verify = input.verify !== false
const barMode = input.bar_mode || 'off'
if (!['off', 'on', 'auto'].includes(barMode)) {
  throw new Error(`review-panel-verify: bar_mode must be "off", "on", or "auto", got "${barMode}".`)
}

const briefByName = new Map(plan.briefs.map((b) => [b.name, b]))

// The checker's structured verdict: one sub-verdict per dimension, with the
// vocabulary constrained exhaustively — a checker cannot invent a synonym
// verdict, and the downgrade table below maps each value deterministically.
const CHECK_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  required: ['evidence', 'applicability', 'genuine', 'priority', 'attribution'],
  properties: {
    evidence: {
      type: 'object',
      additionalProperties: false,
      required: ['verdict', 'note'],
      properties: {
        verdict: { type: 'string', enum: ['pass', 'fail'] },
        note: { type: 'string' },
      },
    },
    applicability: {
      type: 'object',
      additionalProperties: false,
      required: ['verdict', 'note'],
      properties: {
        verdict: { type: 'string', enum: ['pass', 'fail'] },
        note: { type: 'string' },
      },
    },
    genuine: {
      type: 'object',
      additionalProperties: false,
      required: ['verdict', 'note'],
      properties: {
        verdict: { type: 'string', enum: ['pass', 'fail'] },
        note: { type: 'string' },
      },
    },
    priority: {
      type: 'object',
      additionalProperties: false,
      required: ['verdict', 'proposed', 'note'],
      properties: {
        verdict: { type: 'string', enum: ['agree', 'reclassify'] },
        proposed: { type: 'string', enum: ['P0', 'P1', 'P2', 'P3'] },
        note: { type: 'string' },
      },
    },
    attribution: {
      type: 'object',
      additionalProperties: false,
      required: ['verdict', 'note'],
      properties: {
        verdict: { type: 'string', enum: ['pass', 'fail', 'not-applicable'] },
        note: { type: 'string' },
      },
    },
  },
}

const BAR_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  required: ['verdict', 'reasons'],
  properties: {
    verdict: { type: 'string', enum: ['pass', 'fail'] },
    reasons: { type: 'array', items: { type: 'string' } },
    notes: { type: 'string' },
  },
}

function checkerPrompt(finding) {
  const brief = briefByName.get(finding.brief)
  // An aspect's path is absolute (it ships with the skill); a repo brief's is
  // relative to the reviewed repository.
  const briefLine = brief
    ? `The brief it was judged against is \`${brief.path.startsWith('/') ? brief.path : `${root}/${brief.path}`}\` — read it.`
    : `Its brief ("${finding.brief}") is not in the plan; judge the finding on its own terms.`
  const attributionLine =
    plan.mode === 'full' || !plan.base_ref
      ? 'This run has no base branch, so the attribution dimension is not applicable — return "not-applicable" for it.'
      : `The branch is compared against the base \`${plan.base_ref}\`; judge the attribution dimension with \`git diff ${plan.base_ref}...HEAD -- <file>\`.`
  return [
    `You are an independent checker for a single code-review finding. You did not produce it, and your job is to find out whether it is wrong before accepting it.`,
    `The repository root is \`${root}\`. ${briefLine}`,
    attributionLine,
    `## The finding\n\n${JSON.stringify(finding, null, 2)}`,
    `## How to check and report\n\nRead and follow \`${plan.checker_method_path}\` — the checking method and what each dimension means. Return the structured verdict it describes.`,
  ].join('\n\n')
}

// Dispatch one checker per finding: Codex first, so a differently-trained
// family judges Claude-produced findings. A checker that errors or returns an
// invalid verdict resolves to null; those findings are retried on Claude —
// still never their producer (a fresh clean-context session), so the exclusion
// is structural — and the achieved pairing is recorded per finding rather than
// the run refusing. A finding no checker confirms is suppressed: verification
// is a gate, not an annotation.
phase('Check')
let checked = []
if (verify && findings.length) {
  log(`Checking ${findings.length} finding(s) — Codex first, Claude on degradation.`)
  const codexResults = await parallel(
    findings.map((finding) => () =>
      agent(checkerPrompt(finding), {
        engine: 'codex',
        label: `check:${finding.brief}:${finding.file ?? '?'}`,
        phase: 'Check',
        schema: CHECK_SCHEMA,
      }),
    ),
  )
  const retryIdx = []
  codexResults.forEach((r, i) => {
    if (!r) retryIdx.push(i)
  })
  let claudeResults = []
  if (retryIdx.length) {
    log(`${retryIdx.length} checker(s) did not return on Codex — retrying on Claude (recorded as degraded pairing).`)
    claudeResults = await parallel(
      retryIdx.map((i) => () =>
        agent(checkerPrompt(findings[i]), {
          engine: 'claude',
          label: `check:${findings[i].brief}:${findings[i].file ?? '?'} (degraded)`,
          phase: 'Check',
          schema: CHECK_SCHEMA,
        }),
      ),
    )
  }
  checked = findings.map((finding, i) => {
    if (codexResults[i]) return { finding, verdicts: codexResults[i], checked_by: 'codex' }
    const j = retryIdx.indexOf(i)
    if (j !== -1 && claudeResults[j]) return { finding, verdicts: claudeResults[j], checked_by: 'claude' }
    return { finding, verdicts: null, checked_by: null }
  })
} else {
  checked = findings.map((finding) => ({ finding, verdicts: null, checked_by: 'skipped' }))
}

// The downgrade table, applied in code so no verdict is reinterpreted:
//   evidence, applicability, or genuine fail → suppress (kind: rejected)
//   priority reclassify                      → set the honest priority, never suppress
//   attribution fail                         → mark indeterminate and route to the PR
//   no checker verdict on any engine         → the check itself failed: the finding
//                                              is withheld from posting (the gate
//                                              holds) and recorded as check-failed,
//                                              distinct from a rejection
const survivors = []
const suppressed = []
const reclassified = []
const attributionIndeterminate = []
for (const { finding, verdicts, checked_by } of checked) {
  if (checked_by === 'skipped') {
    survivors.push(finding)
    continue
  }
  if (!verdicts) {
    suppressed.push({
      finding,
      kind: 'check-failed',
      reason: 'no checker returned a verdict on either engine — the finding is withheld, not rejected',
      checked_by: null,
    })
    continue
  }
  const rejections = [
    ['evidence', 'evidence'],
    ['applicability', 'not applicable under the brief'],
    ['genuine', 'not a genuine issue'],
  ].filter(([dim]) => verdicts[dim].verdict === 'fail')
  if (rejections.length) {
    const [dim, label] = rejections[0]
    suppressed.push({ finding, kind: 'rejected', reason: `${label}: ${verdicts[dim].note}`, checked_by })
    continue
  }
  const out = { ...finding, checked_by }
  if (verdicts.priority.verdict === 'reclassify' && verdicts.priority.proposed !== finding.priority) {
    reclassified.push({
      file: finding.file,
      title: finding.title,
      from: finding.priority,
      to: verdicts.priority.proposed,
      note: verdicts.priority.note,
    })
    out.priority = verdicts.priority.proposed
  }
  if (verdicts.attribution.verdict === 'fail') {
    // The introduced-vs-pre-existing call is wrong or unsubstantiated. Route
    // conservatively to the PR (a missed change-introduced defect filed as a
    // background issue is the worse error) and mark it so the report and the
    // posted context can say the attribution is indeterminate.
    out.attribution = 'indeterminate'
    if (out.preexisting === true) out.preexisting = false
    attributionIndeterminate.push({ file: finding.file, title: finding.title, note: verdicts.attribution.note })
  }
  survivors.push(out)
}

const byEngine = { codex: 0, claude: 0 }
for (const { checked_by } of checked) {
  if (checked_by === 'codex' || checked_by === 'claude') byEngine[checked_by] += 1
}
const checkFailed = suppressed.filter((s) => s.kind === 'check-failed').length
const verification = {
  performed: verify && findings.length > 0,
  checked: verify ? findings.length : 0,
  rejected: suppressed.length - checkFailed,
  check_failed: checkFailed,
  reclassified: reclassified.length,
  by_engine: byEngine,
  degraded: byEngine.claude > 0 || checkFailed > 0,
}

// The bar trigger, computed from the assembled state — never from a reviewer's
// say-so: the review would otherwise converge clean (zero blocking
// change-introduced findings), or some criterion was skipped, failed, or only
// partially covered. The trigger is always computed and reported; bar_mode
// decides whether the judge actually runs.
phase('Bar')
const blocking = survivors.filter(
  (f) => f.preexisting !== true && (f.priority === 'P0' || f.priority === 'P1'),
)
const triggerReasons = []
if (blocking.length === 0) triggerReasons.push('zero blocking change-introduced findings')
const notRun = coverage.filter((c) => c.status === 'not-run')
const partial = coverage.filter((c) => c.status === 'partial' || c.status === 'none')
if (notRun.length) triggerReasons.push(`${notRun.length} criterion/criteria not run`)
if (partial.length) triggerReasons.push(`${partial.length} criterion/criteria only partially covered`)
if (failures.length) triggerReasons.push(`${failures.length} reviewer(s) returned nothing`)
const triggerFired = triggerReasons.length > 0

const bar = {
  mode: barMode,
  trigger_fired: triggerFired,
  trigger_reasons: triggerReasons,
  ran: false,
  outcome: null, // 'pass' | 'fail' | 'check-failed' when ran
  reasons: [],
  checked_by: null,
}

if (barMode === 'on' || (barMode === 'auto' && triggerFired)) {
  bar.ran = true
  const material = {
    findings: survivors,
    coverage,
    skipped,
    failures,
    reviewer_notes: reviews,
    verification,
    // What the gates removed is part of the review's story: a clean-looking
    // review whose findings were all suppressed as fabricated must read very
    // differently to the judge than one whose reviewers found nothing.
    suppressed_by_quote_validator: suppressedByValidator,
    quote_validation: quoteValidation,
    suppressed_by_checkers: suppressed,
  }
  const barPrompt = [
    `You are the independent judge of an assembled code review — a second opinion on the review itself, not a re-review of the code.`,
    `The repository root is \`${root}\`.` +
      (plan.base_ref ? ` The reviewed branch is compared against \`${plan.base_ref}\`.` : ''),
    `## The bar\n\nThe discipline the reviewers were charged with is \`${plan.template_path}\` — read it. That text is the bar you judge against; do not weaken it or substitute your own.`,
    `## The assembled review\n\n${JSON.stringify(material, null, 2)}`,
    `## How to judge and report\n\nRead and follow \`${plan.bar_method_path}\`. Return the structured verdict it describes.`,
  ].join('\n\n')

  // Cross-family judge preferred for the same reason as the checkers; a judge
  // that does not return is retried on Claude, and if that also fails the
  // outcome is 'check-failed' — a recorded degraded outcome, distinct from
  // pass and fail, never silently dropped.
  let verdict = await agent(barPrompt, {
    engine: 'codex',
    label: 'bar-check',
    phase: 'Bar',
    schema: BAR_SCHEMA,
  }).catch(() => null)
  bar.checked_by = verdict ? 'codex' : null
  if (!verdict) {
    verdict = await agent(barPrompt, {
      engine: 'claude',
      label: 'bar-check (degraded)',
      phase: 'Bar',
      schema: BAR_SCHEMA,
    }).catch(() => null)
    if (verdict) bar.checked_by = 'claude'
  }
  if (verdict) {
    bar.outcome = verdict.verdict
    bar.reasons = verdict.reasons
    if (verdict.notes) bar.notes = verdict.notes
  } else {
    bar.outcome = 'check-failed'
    bar.reasons = ['the bar judge returned no valid verdict on either engine']
  }
}

// The single, complete report for the run: posting and the chat summary read
// this one object, so nothing has to be re-joined across stage files.
return {
  findings: survivors,
  suppressed_by_checkers: suppressed,
  suppressed_by_validator: suppressedByValidator,
  quote_validation: quoteValidation,
  reclassified,
  attribution_indeterminate: attributionIndeterminate,
  verification,
  bar,
  coverage,
  skipped,
  failures,
  reviews,
  sweep_advisories: input.sweep_advisories || [],
  mode: input.mode ?? plan.mode,
  pr: input.pr ?? plan.pr,
}
