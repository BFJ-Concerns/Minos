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
  // and the bar judge are verification legs: review work at the resting level
  // on whichever family a finding's checker runs on — Codex for the panel's
  // Claude-produced findings, Claude for the Codex leg's — with the other
  // family as each finding's recorded degradation path. Pinned because an
  // unpinned call rides the ambient default model silently.
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
//     entry_point, // "combined" (default) | "verification-only" | "bar-only"
//     verification_result, disposition_manifest, manifest_sha256,
//                  // required by bar-only. The service owns policy and the
//                  // manifest; the bar judges and attests that exact digest.
//     prior_verification_result, candidate_reconsiderations,
//                  // optional remediation inputs. The prior exhaustive result
//                  // is carried forward; only the exact named suppressions are
//                  // reopened for fresh checking.
//     coverage_result,   // optional — the mechanical coverage-accounting result
//                        // ({ status, omissions, … }) or null. THE HARD GATE:
//                        // a status that is not "complete" blocks convergence
//                        // deterministically (coverage_gate below). Kept
//                        // distinct from the inspection record — this is the
//                        // labelling verdict, not the depth evidence.
//     inspection_record, // optional — the lead-owned inspection record the
//                        // accounting consumed (changed files, per-hunk
//                        // read/omitted accounts, named definition/call-site
//                        // references) or null. THE BAR'S DEPTH EVIDENCE: it
//                        // rides into the bar material so the judge can weigh
//                        // whether the recorded depth was adequate, not just
//                        // whether every item was labelled read.
//                        // The two must arrive together to converge: supplying
//                        // one without the other is a wiring error and blocks
//                        // the gate (a result with no record = depth unjudged).
//                        // Supplying neither leaves the gate inactive.
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
const rawFindings = input.findings || []
const coverage = input.coverage || []
const skipped = input.skipped || []
const failures = input.failures || []
const reviews = input.reviews || []
const suppressedByValidator = input.suppressed_by_validator || []
const quoteValidation = input.quote_validation || null
const verify = input.verify !== false
const barMode = input.bar_mode || 'off'
const entryPoint = input.entry_point || 'combined'
const dispositionManifest = input.disposition_manifest || null
const manifestSHA256 = input.manifest_sha256 || null
const priorVerificationResult = input.prior_verification_result || null
const candidateReconsiderations = input.candidate_reconsiderations || []
if (!['combined', 'verification-only', 'bar-only'].includes(entryPoint)) {
  throw new Error(`review-panel-verify: entry_point must be "combined", "verification-only", or "bar-only", got "${entryPoint}".`)
}
if (entryPoint === 'verification-only' && barMode !== 'off') {
  throw new Error('review-panel-verify: verification-only entry point requires bar_mode "off".')
}
if (entryPoint === 'bar-only') {
  if (!dispositionManifest || !/^[0-9a-f]{64}$/.test(String(manifestSHA256))) {
    throw new Error('review-panel-verify: bar-only entry point requires disposition_manifest and its lower-case SHA-256 digest.')
  }
  if (verify) {
    throw new Error('review-panel-verify: bar-only entry point must set verify=false; finding verification is a separate phase.')
  }
  if (barMode !== 'on') {
    throw new Error('review-panel-verify: bar-only entry point requires bar_mode "on".')
  }
}
if (priorVerificationResult && (priorVerificationResult.schema_version !== 1 || !Array.isArray(priorVerificationResult.candidates))) {
  throw new Error('review-panel-verify: prior_verification_result must be an exhaustive verification-only result.')
}
if (!Array.isArray(candidateReconsiderations)) {
  throw new Error('review-panel-verify: candidate_reconsiderations must be an array.')
}
if (candidateReconsiderations.length && !priorVerificationResult) {
  throw new Error('review-panel-verify: candidate reconsiderations require the prior exhaustive verification result.')
}
const reservedOrdinals = new Map()
for (const raw of rawFindings) {
  if (!Number.isInteger(raw.producer_ordinal) || raw.producer_ordinal < 0) continue
  const identity = String(raw.producer_identity || raw.producer || '')
  if (identity !== String(raw.producer || '')) {
    throw new Error('review-panel-verify: a carried candidate changed producer identity.')
  }
  const reserved = reservedOrdinals.get(identity) || new Set()
  if (reserved.has(raw.producer_ordinal)) {
    throw new Error(`review-panel-verify: duplicate carried candidate ${identity}:${raw.producer_ordinal}.`)
  }
  reserved.add(raw.producer_ordinal)
  reservedOrdinals.set(identity, reserved)
}
const producerOrdinals = new Map()
const findings = rawFindings.map((finding) => {
  const producerIdentity = String(finding.producer || '')
  let ordinal = finding.producer_identity === producerIdentity && Number.isInteger(finding.producer_ordinal)
    ? finding.producer_ordinal
    : producerOrdinals.get(producerIdentity) || 0
  const reserved = reservedOrdinals.get(producerIdentity) || new Set()
  while (reserved.has(ordinal) && !(finding.producer_identity === producerIdentity && finding.producer_ordinal === ordinal)) ordinal += 1
  reserved.add(ordinal)
  reservedOrdinals.set(producerIdentity, reserved)
  producerOrdinals.set(producerIdentity, ordinal + 1)
  if (finding.assurance && finding.assurance !== 'agent-judgement') {
    throw new Error(`review-panel-verify: unsupported finding assurance "${finding.assurance}".`)
  }
  return {
    ...finding,
    producer_identity: producerIdentity,
    producer_ordinal: ordinal,
    assurance: 'agent-judgement',
  }
})
// Two distinct coverage artefacts, never conflated. The accounting RESULT
// (account-coverage's output, or any equivalent mechanical accounting) is the
// gate; the inspection RECORD it consumed is the bar's depth evidence. Null for
// a caller that supplies neither.
const coverageResult = input.coverage_result || null
const inspectionRecord = input.inspection_record || null
// A present result whose mechanical status is anything but 'complete' — partial,
// or an accounting error — is short. "Partial coverage can never converge" is a
// determinate guarantee, so this is settled in code: a short result fires the
// bar trigger below AND blocks convergence outright (coverage_gate), whatever
// the panel declared or the judge concludes.
const coverageResultShort =
  !!coverageResult && coverageResult.status !== 'complete'
// The two artefacts must travel together to converge: the accounting result
// gives the completeness verdict the gate trusts, and the inspection record
// gives the bar the evidence to judge depth. Supplying exactly one is a wiring
// error — a result with no record leaves depth unjudged, a record with no result
// has no completeness verdict — so asymmetric supply blocks convergence
// deterministically, exactly like a short result. Supplying neither is the
// general caller with no coverage seam: the gate stays inactive, not passed.
const coverageArtefactSupplied = !!coverageResult || !!inspectionRecord
const coverageArtefactMissing = coverageArtefactSupplied && !(coverageResult && inspectionRecord)
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
  required: ['verdict', 'reasons', 'implicated_briefs', 'candidate_reconsiderations'],
  properties: {
    verdict: { type: 'string', enum: ['pass', 'fail'] },
    reasons: { type: 'array', items: { type: 'string' } },
    // Which briefs the failure reasons concern, each with its own subset of
    // the reasons — the remediation round re-dispatches exactly these briefs
    // and hands each only its own complaints (plan_remediation.py consumes
    // the list). Required on every verdict so an omission cannot masquerade
    // as a systemic failure: an empty array is the explicit statement that
    // the verdict is a pass, or that a fail traces to no brief's output.
    implicated_briefs: {
      type: 'array',
      items: {
        type: 'object',
        additionalProperties: false,
        required: ['brief', 'reasons'],
        properties: {
          brief: { type: 'string' },
          reasons: { type: 'array', items: { type: 'string' } },
        },
      },
    },
    candidate_reconsiderations: {
      type: 'array',
      items: {
        type: 'object',
        additionalProperties: false,
        required: ['candidate_id', 'reasons'],
        properties: {
          candidate_id: { type: 'string', pattern: '^C-[0-9a-f]{64}$' },
          reasons: { type: 'array', minItems: 1, items: { type: 'string' } },
        },
      },
    },
    notes: { type: 'string' },
  },
}

function validateBarCandidateReconsiderations(verdict) {
  const requested = verdict.candidate_reconsiderations || []
  if (verdict.verdict === 'pass' && requested.length) return false
  if (!requested.length) return true
  if (entryPoint !== 'bar-only' || !dispositionManifest || !Array.isArray(dispositionManifest.candidates)) return false
  const verdictReasons = new Set(verdict.reasons || [])
  const manifestByID = new Map(dispositionManifest.candidates.map((entry) => [entry?.candidate?.candidate_id, entry]))
  const verificationByProducer = new Map(
    verificationResult.candidates.map((entry) => [`${entry?.producer?.id}\u0000${entry?.producer?.ordinal}`, entry]),
  )
  const seen = new Set()
  for (const request of requested) {
    if (seen.has(request.candidate_id)) return false
    seen.add(request.candidate_id)
    const manifestEntry = manifestByID.get(request.candidate_id)
    if (!manifestEntry || manifestEntry.outcome !== 'suppressed') return false
    const producer = manifestEntry.candidate?.producer
    const verificationEntry = verificationByProducer.get(`${producer?.id}\u0000${producer?.ordinal}`)
    if (!verificationEntry || verificationEntry.outcome !== 'suppressed') return false
    if (!request.reasons?.length || request.reasons.some((reason) => !String(reason).trim() || !verdictReasons.has(reason))) return false
  }
  return true
}

function checkerPrompt(finding) {
  const brief = briefByName.get(finding.brief)
  // An aspect's path is absolute (it ships with the skill); a repo brief's is
  // relative to the reviewed repository. The Codex leg has no brief file at
  // all — its reviewer's mandate is general correctness of the branch diff —
  // so its checker is told that rather than left to wonder about a missing brief.
  const codexLeg = plan.codex_review
  const briefLine = brief
    ? `The brief it was judged against is \`${brief.path.startsWith('/') ? brief.path : `${root}/${brief.path}`}\` — read it.`
    : codexLeg && finding.brief === codexLeg.name
      ? `It was produced by an external general reviewer of the branch diff (the Codex CLI's built-in review). There is no brief file: its mandate is general correctness, so judge applicability against that.`
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

// Dispatch one checker per finding, preferring the family opposite the
// finding's producer, so a differently-trained family judges every finding:
// the panel's reviewers run on Claude, so their findings go to Codex checkers;
// the Codex leg's findings (producer `…@codex-cli`) go to Claude checkers. A
// checker that errors or returns an invalid verdict resolves to null; those
// findings are retried on the other family — still never their producer (a
// fresh clean-context session), so the exclusion is structural — and the
// achieved pairing is recorded per finding rather than the run refusing. A
// finding no checker confirms is suppressed: verification is a gate, not an
// annotation.
phase('Check')
const otherEngine = (engine) => (engine === 'codex' ? 'claude' : 'codex')
const preferredEngine = (finding) =>
  String(finding.producer || '').endsWith('@codex-cli') ? 'claude' : 'codex'
// A finding that already carries a checker's verdict was confirmed in an
// earlier round of this run (a remediation re-verify merges round-one
// survivors back in). Its check stands — a fresh checker would re-spend a
// call to re-derive a recorded verdict — so it rides straight through,
// counted separately as carried_forward.
const isCarried = (f) => f.checked_by === 'codex' || f.checked_by === 'claude'
const carried = verify ? findings.filter(isCarried) : []
const toCheck = verify ? findings.filter((f) => !isCarried(f)) : findings
let checked = []
if (verify && toCheck.length) {
  log(
    `Checking ${toCheck.length} finding(s) — each on the family opposite its producer, the other on degradation.` +
      (carried.length ? ` ${carried.length} carried forward already checked.` : ''),
  )
  const preferred = toCheck.map(preferredEngine)
  const firstResults = await parallel(
    toCheck.map((finding, i) => () =>
      agent(checkerPrompt(finding), {
        engine: preferred[i],
        label: `check:${finding.brief}:${finding.file ?? '?'}`,
        phase: 'Check',
        schema: CHECK_SCHEMA,
      }),
    ),
  )
  const retryIdx = []
  firstResults.forEach((r, i) => {
    if (!r) retryIdx.push(i)
  })
  let retryResults = []
  if (retryIdx.length) {
    log(`${retryIdx.length} checker(s) did not return on their preferred family — retrying on the other (recorded as degraded pairing).`)
    retryResults = await parallel(
      retryIdx.map((i) => () =>
        agent(checkerPrompt(toCheck[i]), {
          engine: otherEngine(preferred[i]),
          label: `check:${toCheck[i].brief}:${toCheck[i].file ?? '?'} (degraded)`,
          phase: 'Check',
          schema: CHECK_SCHEMA,
        }),
      ),
    )
  }
  checked = toCheck.map((finding, i) => {
    if (firstResults[i]) {
      return { finding, verdicts: firstResults[i], checked_by: preferred[i], degraded_pairing: false }
    }
    const j = retryIdx.indexOf(i)
    if (j !== -1 && retryResults[j]) {
      return { finding, verdicts: retryResults[j], checked_by: otherEngine(preferred[i]), degraded_pairing: true }
    }
    return { finding, verdicts: null, checked_by: null }
  })
} else {
  checked = toCheck.map((finding) => ({ finding, verdicts: null, checked_by: 'skipped' }))
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
const priorityRanks = { P0: 0, P1: 1, P2: 2, P3: 3 }
const moreSeverePriority = (left, right) =>
  priorityRanks[left] <= priorityRanks[right] ? left : right
// Carried-forward findings survived an earlier round's checkers with any
// reclassification or attribution marking already applied — pass them through.
for (const finding of carried) {
  survivors.push({
    ...finding,
    proposed_priority: finding.proposed_priority || finding.priority,
    verifier_priority: finding.verifier_priority || finding.priority,
    verification_outcome: 'verified',
  })
}
for (const { finding, verdicts, checked_by } of checked) {
  if (checked_by === 'skipped') {
    survivors.push(finding)
    continue
  }
  if (!verdicts) {
    suppressed.push({
      finding,
      kind: 'check-failed',
      outcome: 'verification-unresolved',
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
    suppressed.push({ finding, kind: 'rejected', outcome: 'suppressed', reason: `${label}: ${verdicts[dim].note}`, checked_by })
    continue
  }
  const verifierPriority = verdicts.priority.proposed
  const verifiedPriority = moreSeverePriority(finding.priority, verifierPriority)
  const out = {
    ...finding,
    checked_by,
    proposed_priority: finding.priority,
    verifier_priority: verifierPriority,
    priority: verifiedPriority,
    verification_outcome: 'verified',
    priority_validation: {
      agreement: finding.priority === verifierPriority ? 'agreed' : 'disputed',
      rationale: verdicts.priority.note,
    },
  }
  if (verdicts.priority.verdict === 'reclassify' && verdicts.priority.proposed !== finding.priority) {
    reclassified.push({
      file: finding.file,
      title: finding.title,
      from: finding.priority,
      verifier_priority: verifierPriority,
      to: verifiedPriority,
      note: verdicts.priority.note,
    })
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
let degradedPairings = 0
for (const { checked_by, degraded_pairing } of checked) {
  if (checked_by === 'codex' || checked_by === 'claude') byEngine[checked_by] += 1
  if (degraded_pairing) degradedPairings += 1
}
const checkFailed = suppressed.filter((s) => s.kind === 'check-failed').length
const verification = {
  performed: verify && findings.length > 0,
  checked: verify ? toCheck.length : 0,
  // Findings whose verdict was recorded in an earlier round of this run and
  // rode through without a fresh checker (remediation re-verify only).
  carried_forward: carried.length,
  rejected: suppressed.length - checkFailed,
  check_failed: checkFailed,
  reclassified: reclassified.length,
  by_engine: byEngine,
  // Degraded means a finding was checked by its second-choice family, or not
  // checked at all — engine counts alone can't say this now that different
  // findings legitimately prefer different families.
  degraded_pairings: degradedPairings,
  degraded: degradedPairings > 0 || checkFailed > 0,
}

const survivorByCandidate = new Map(
  survivors.map((finding) => [`${finding.producer_identity}\u0000${finding.producer_ordinal}`, finding]),
)
const suppressedByCandidate = new Map(
  suppressed.map((entry) => [`${entry.finding.producer_identity}\u0000${entry.finding.producer_ordinal}`, entry]),
)
const currentVerificationResult = {
  schema_version: 1,
  criteria: plan.briefs.map((brief) => ({ name: brief.name, path: brief.path })),
  candidates: findings.map((candidate) => {
    const key = `${candidate.producer_identity}\u0000${candidate.producer_ordinal}`
    const survivor = survivorByCandidate.get(key)
    const suppression = suppressedByCandidate.get(key)
    const checkerFamily = survivor?.checked_by || suppression?.checked_by || null
    return {
      candidate,
      producer: {
        family: candidate.producer_identity.endsWith('@codex-cli') ? 'codex' : 'claude',
        id: candidate.producer_identity,
        ordinal: candidate.producer_ordinal,
      },
      assurance: 'agent-judgement',
      outcome: survivor ? 'verified' : suppression?.outcome || 'verification-unresolved',
      proposed_priority: candidate.priority,
      verifier_priority: survivor?.verifier_priority || null,
      verified_priority: survivor?.priority || null,
      verification_evidence: {
        checker_family: checkerFamily,
        checker_id: checkerFamily ? `check:${candidate.producer_identity}:${candidate.producer_ordinal}@${checkerFamily}` : null,
        degraded_pairing: checkerFamily ? checkerFamily !== preferredEngine(candidate) : true,
        rationale: suppression?.reason || survivor?.priority_validation?.rationale || null,
      },
      verified_finding: survivor || null,
    }
  }),
}
const currentByProducer = new Map(
  currentVerificationResult.candidates.map((entry) => [`${entry.producer.id}\u0000${entry.producer.ordinal}`, entry]),
)
const mergedCandidates = []
if (priorVerificationResult) {
  for (const entry of priorVerificationResult.candidates) {
    const key = `${entry?.producer?.id}\u0000${entry?.producer?.ordinal}`
    mergedCandidates.push(currentByProducer.get(key) || entry)
    currentByProducer.delete(key)
  }
}
mergedCandidates.push(...currentByProducer.values())
const assembledVerificationResult = priorVerificationResult ? {
  schema_version: 1,
  criteria: [...(priorVerificationResult.criteria || [])],
  candidates: mergedCandidates,
} : currentVerificationResult
if (priorVerificationResult) {
  const knownCriteria = new Set(assembledVerificationResult.criteria.map((criterion) => criterion.name))
  for (const criterion of currentVerificationResult.criteria) {
    if (!knownCriteria.has(criterion.name)) assembledVerificationResult.criteria.push(criterion)
  }
  const checkedKeys = new Set(toCheck.map((finding) => `${finding.producer_identity}\u0000${finding.producer_ordinal}`))
  for (const request of candidateReconsiderations) {
    const key = `${request.producer_identity}\u0000${request.producer_ordinal}`
    const prior = priorVerificationResult.candidates.find((entry) => `${entry?.producer?.id}\u0000${entry?.producer?.ordinal}` === key)
    if (!prior || prior.outcome !== 'suppressed' || !checkedKeys.has(key)) {
      throw new Error(`review-panel-verify: reconsidered candidate ${request.candidate_id || key} was not reopened from a prior suppression.`)
    }
  }
}
const verificationResult = entryPoint === 'bar-only'
  ? input.verification_result
  : assembledVerificationResult
if (entryPoint === 'bar-only' && (!verificationResult || verificationResult.schema_version !== 1 || !Array.isArray(verificationResult.candidates))) {
  throw new Error('review-panel-verify: bar-only entry point requires the verification_result produced by verification-only.')
}

// The bar trigger, computed from the assembled state — never from a reviewer's
// say-so: the review would otherwise converge clean (zero blocking
// change-introduced findings), or some criterion was skipped, failed, or only
// partially covered. The trigger is always computed and reported; bar_mode
// decides whether the judge actually runs.
phase('Bar')
const triggerReasons = []
if (entryPoint === 'bar-only') triggerReasons.push('bar-only attestation requested for a service-validated disposition manifest')
else if (survivors.length === 0) triggerReasons.push('zero independently verified findings')
const notRun = coverage.filter((c) => c.status === 'not-run')
const partial = coverage.filter((c) => c.status === 'partial' || c.status === 'none')
if (notRun.length) triggerReasons.push(`${notRun.length} criterion/criteria not run`)
if (partial.length) triggerReasons.push(`${partial.length} criterion/criteria only partially covered`)
if (failures.length) triggerReasons.push(`${failures.length} reviewer(s) returned nothing`)
// The mechanical accounting result is a trigger input in its own right — the
// panel account is reviewers' self-declaration, this is the diff-bound
// mechanical truth. A short result always convenes the judge (so the
// panel-vs-result contradiction is examined), independently of the hard gate
// below.
if (coverageResultShort) {
  const omitted = Array.isArray(coverageResult.omissions) ? coverageResult.omissions.length : 0
  triggerReasons.push(
    `the coverage accounting result is ${coverageResult.status || 'unaccounted'}` +
      (omitted ? ` (${omitted} omission(s))` : ''),
  )
}
// Asymmetric supply is surfaced as its own trigger reason so the block is
// legible, not silent — even though a clean review already convenes the judge.
if (coverageArtefactMissing) {
  triggerReasons.push(
    coverageResult
      ? 'the inspection record is missing — coverage depth cannot be judged'
      : 'the coverage accounting result is missing — completeness is unverified',
  )
}
const triggerFired = triggerReasons.length > 0

const bar = {
  mode: barMode,
  trigger_fired: triggerFired,
  trigger_reasons: triggerReasons,
  ran: false,
  outcome: null, // 'pass' | 'fail' | 'check-failed' when ran
  reasons: [],
  // On a fail, the brief names the judge holds responsible — what the
  // remediation round re-dispatches. Empty on a pass or a systemic fail.
  implicated_briefs: [],
  candidate_reconsiderations: [],
  checked_by: null,
}

if (barMode === 'on' || (barMode === 'auto' && triggerFired)) {
  bar.ran = true
  const material = {
    findings: survivors,
    verification_result: verificationResult,
    disposition_manifest: dispositionManifest,
    manifest_sha256: manifestSHA256,
    coverage,
    // Both coverage artefacts reach the judge, unconflated. The accounting
    // RESULT lets it see a panel-vs-result contradiction (declared full, found
    // partial); the inspection RECORD — the changed files with per-hunk
    // read/omitted accounts and the named definitions and call-sites the lead
    // recorded — is the evidence it weighs coverage *depth* from. Each is null
    // when the run supplied none.
    coverage_result: coverageResult,
    inspection_record: inspectionRecord,
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
  if (verdict && !validateBarCandidateReconsiderations(verdict)) verdict = null
  bar.checked_by = verdict ? 'codex' : null
  if (!verdict) {
    verdict = await agent(barPrompt, {
      engine: 'claude',
      label: 'bar-check (degraded)',
      phase: 'Bar',
      schema: BAR_SCHEMA,
    }).catch(() => null)
    if (verdict && !validateBarCandidateReconsiderations(verdict)) verdict = null
    if (verdict) bar.checked_by = 'claude'
  }
  if (verdict) {
    bar.outcome = verdict.verdict
    bar.reasons = verdict.reasons
    bar.implicated_briefs = verdict.implicated_briefs || []
    bar.candidate_reconsiderations = verdict.candidate_reconsiderations || []
    if (verdict.notes) bar.notes = verdict.notes
  } else {
    bar.outcome = 'check-failed'
    bar.reasons = ['the bar judge returned no valid verdict on either engine']
  }
}

// The mechanical convergence gate — a determinate, LLM-independent block that
// stands whatever the panel declared or the bar judged. It clears convergence
// only when BOTH artefacts are present AND the accounting result is 'complete':
// "partial coverage can never converge" (a short result), and depth must be
// judgeable (the inspection record must be in hand for the bar to weigh it), so
// a result without its record blocks just as a short result does. A null gate
// (neither artefact supplied) is inactive — "no gate", never a pass. The caller
// treats a blocking gate as inadequate coverage: the review may still publish
// its verified findings, but it cannot converge.
const coverageGate = {
  result_status: coverageResult ? (coverageResult.status ?? null) : null,
  inspection_record_present: !!inspectionRecord,
  blocks_convergence: coverageResultShort || coverageArtefactMissing,
}

const coverageAttestationReasons = coverageGate.blocks_convergence
  ? [
      coverageResultShort
        ? `mechanical coverage accounting is ${coverageGate.result_status || 'incomplete'}`
        : 'mechanical coverage evidence is incomplete',
    ]
  : []

const barAttestation = entryPoint === 'bar-only' && bar.ran ? {
  schema_version: 1,
  manifest_sha256: manifestSHA256,
  verdict: coverageGate.blocks_convergence ? 'unresolved' : bar.outcome === 'pass' ? 'pass' : bar.outcome === 'fail' ? 'fail' : 'unresolved',
  reasons: [...bar.reasons, ...coverageAttestationReasons],
  implicated_briefs: bar.implicated_briefs,
  candidate_reconsiderations: bar.candidate_reconsiderations,
  checker: {
    family: bar.checked_by || 'unresolved',
    id: bar.checked_by ? `bar-check@${bar.checked_by}` : 'bar-check-unresolved',
    degraded_pairing: bar.checked_by === 'claude' || bar.outcome === 'check-failed',
  },
} : null

// The single, complete report for the run: posting and the chat summary read
// this one object, so nothing has to be re-joined across stage files.
return {
  entry_point: entryPoint,
  verification_result: verificationResult,
  bar_attestation: barAttestation,
  disposition_manifest: dispositionManifest,
  findings: survivors,
  suppressed_by_checkers: suppressed,
  suppressed_by_validator: suppressedByValidator,
  quote_validation: quoteValidation,
  reclassified,
  attribution_indeterminate: attributionIndeterminate,
  verification,
  bar,
  coverage,
  coverage_result: coverageResult,
  inspection_record: inspectionRecord,
  coverage_gate: coverageGate,
  skipped,
  failures,
  reviews,
  sweep_advisories: input.sweep_advisories || [],
  mode: input.mode ?? plan.mode,
  pr: input.pr ?? plan.pr,
}
