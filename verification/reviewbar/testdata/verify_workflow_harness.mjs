#!/usr/bin/env node
// Integration harness for the coverage seam. Runs the real (service-consumed)
// verify_workflow.js over a prepared verify-input.json, with the model boundary
// stubbed, and prints { result, bar_prompt } to stdout: the workflow's returned
// report, plus the exact prompt the bar judge received (or null if the bar did
// not run). Capturing the prompt lets the Go test assert on the material the bar
// actually saw — the inspection record's own entries, not merely a non-empty
// field. This exercises the actual workflow code Minos runs, not a reimplementation.
//
//   node verify_workflow_harness.mjs <verify_workflow.js> <verify-input.json> <bar-verdict>
//
// <bar-verdict> is 'pass', 'fail', or 'none'. 'none' means no agent may run
// (the deterministic gate path needs no model); 'pass'/'fail' stub the single
// bar judge. Any agent call is asserted to be the bar judge — these seam cases
// carry zero findings, so no per-finding checker should ever be dispatched.

import { readFileSync } from 'node:fs'
import process from 'node:process'

const [, , workflowPath, inputPath, barVerdict] = process.argv
if (!workflowPath || !inputPath || !barVerdict) {
  console.error('usage: verify_workflow_harness.mjs <workflow.js> <input.json> <pass|fail|none>')
  process.exit(2)
}

// Same load technique as the skill's own test: strip the module export so the
// body can run inside an AsyncFunction with the ensemble hooks supplied here.
const source = readFileSync(workflowPath, 'utf8').replace(/^export const /gm, 'const ')
const args = JSON.parse(readFileSync(inputPath, 'utf8'))

const AsyncFn = Object.getPrototypeOf(async function () {}).constructor

const parallel = async (thunks) => {
  const out = []
  for (const thunk of thunks) {
    try {
      out.push(await thunk())
    } catch {
      out.push(null)
    }
  }
  return out
}

let capturedBarPrompt = null
const agent = async (prompt, opts) => {
  if (barVerdict === 'none') {
    throw new Error(`unexpected agent call (${opts && opts.label}); the deterministic path runs no model`)
  }
  if (!opts || opts.label !== 'bar-check') {
    throw new Error(`unexpected non-bar agent call: ${opts && opts.label}`)
  }
  capturedBarPrompt = prompt
  return { verdict: barVerdict, reasons: [], implicated_briefs: [] }
}

const body = new AsyncFn(
  'args', 'agent', 'parallel', 'pipeline', 'phase', 'log', 'budget', 'workflow', 'worktrees',
  source,
)

const result = await body(
  args,
  agent,
  parallel,
  null,
  () => {},
  () => {},
  null,
  null,
  [],
)

process.stdout.write(JSON.stringify({ result, bar_prompt: capturedBarPrompt }))
