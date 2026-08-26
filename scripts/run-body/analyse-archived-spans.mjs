#!/usr/bin/env node
// Project retained Ensemble transcripts onto the timing record.
import fs from 'node:fs';
import path from 'node:path';

if (process.argv.length !== 4) {
  console.error('usage: analyse-archived-spans TIMING_RECORD RUN_DIRECTORY');
  process.exit(2);
}

const [recordPath, runDirectory] = process.argv.slice(2);
let record;
try { record = JSON.parse(fs.readFileSync(recordPath, 'utf8')); } catch { process.exit(1); }

function lines(file) {
  try { return fs.readFileSync(file, 'utf8').split('\n').filter(Boolean).map(JSON.parse); } catch { return null; }
}
function ms(value) {
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}
function commandCategory(command) {
  const text = String(command || '').toLowerCase();
  if (/(^|[\s;&|])(go|cargo|npm|pnpm|yarn|bun|just|make)\s+[^\n]*\btest\b|\b(pytest|jest|vitest|mocha)\b/.test(text)) return 'test_ms';
  if (/\b(build|compile|check|verify)\b/.test(text)) return 'build_ms';
  return 'reasoning_ms';
}
function empty(format) { return { status: 'parsed', format, build_ms: 0, test_ms: 0, reasoning_ms: 0 }; }
function unparseable(format, reason) { return { status: 'unparseable', format, reason }; }
function addIntervals(entries, categoryAt, format) {
  const result = empty(format);
  let previous = null;
  for (const entry of entries) {
    const now = ms(entry.at);
    // Claude session files contain metadata and UI events without timestamps.
    // They carry no interval boundary, so retain the surrounding timed events
    // rather than refusing an otherwise readable archived session.
    if (now === null) continue;
    if (previous !== null && now >= previous.at) result[previous.category] += now - previous.at;
    previous = { at: now, category: categoryAt(entry) };
  }
  return previous === null ? unparseable(format, 'no timestamped events') : result;
}
function codex(file) {
  const entries = lines(file);
  if (!entries) return unparseable('codex-app-server-events', 'invalid JSONL');
  let active = new Map();
  return addIntervals(entries.map(entry => ({ entry, at: entry.recordedAt })), ({ entry }) => {
    const item = entry.params && entry.params.item;
    const id = item && (item.id || item.itemId);
    if (entry.method === 'item/started' && item && (item.type === 'commandExecution' || item.itemType === 'commandExecution')) {
      active.set(id || '__command__', commandCategory(item.command || item.commandLine || item.input));
    }
    const category = active.get(id || '__command__') || 'reasoning_ms';
    if (entry.method === 'item/completed') {
      active.delete(id || '__command__');
      return 'reasoning_ms';
    }
    return category;
  }, 'codex-app-server-events');
}
function claude(file) {
  const entries = lines(file);
  if (!entries) return unparseable('claude-session-jsonl', 'invalid JSONL');
  let category = 'reasoning_ms';
  return addIntervals(entries.map(entry => ({ entry, at: entry.timestamp })), ({ entry }) => {
    const blocks = entry.message && entry.message.content;
    if (entry.type === 'assistant' && Array.isArray(blocks)) {
      const bash = blocks.find(block => block && block.type === 'tool_use' && block.name === 'Bash');
      if (bash) category = commandCategory(bash.input && bash.input.command);
    }
    if (entry.type === 'user') category = 'reasoning_ms';
    return category;
  }, 'claude-session-jsonl');
}
function agentFiles(directory, unreadableDirectories = []) {
  let entries;
  try { entries = fs.readdirSync(directory, { withFileTypes: true }); }
  catch {
    unreadableDirectories.push(directory);
    return { files: [], unreadableDirectories };
  }
  const output = [];
  for (const entry of entries) {
    const candidate = path.join(directory, entry.name);
    if (entry.isDirectory()) output.push(...agentFiles(candidate, unreadableDirectories).files);
    else if (entry.name === 'agent.json') output.push(candidate);
  }
  return { files: output, unreadableDirectories };
}
function analyseTranscripts(transcripts, recordRoot) {
  const archived = transcripts.filter(item => item && item.source && item.path);
  if (archived.length === 0) return unparseable(null, 'no archived transcript');
  const source = archived[0].source;
  if (!['codex-app-server-events', 'claude-session-jsonl'].includes(source)) {
    return unparseable(source, 'unsupported transcript format');
  }
  if (archived.some(item => item.source !== source)) {
    return unparseable(null, 'mixed transcript formats');
  }
  const total = empty(source);
  for (const transcript of archived) {
    const transcriptPath = path.join(recordRoot, transcript.path);
    const span = source === 'codex-app-server-events' ? codex(transcriptPath) : claude(transcriptPath);
    // A parsed total must cover every recorded attempt.  Refusing a malformed
    // attempt is safer than presenting only the successful-looking remainder.
    if (span.status !== 'parsed') return span;
    total.build_ms += span.build_ms;
    total.test_ms += span.test_ms;
    total.reasoning_ms += span.reasoning_ms;
  }
  return total;
}
function ensembleRecordRoot(record) {
  for (const prefix of ['ensemble-records/', 'home/.local/share/ensemble/runs/']) {
    if (!record.startsWith(prefix)) continue;
    const relative = record.slice(prefix.length);
    if (!relative || relative.split('/').includes('..')) return null;
    return path.join(runDirectory, prefix, relative);
  }
  return null;
}
for (const worker of record.workers || []) {
  const recordRoot = typeof worker.record === 'string' ? ensembleRecordRoot(worker.record) : null;
  if (!recordRoot) {
    worker.span_breakdown = unparseable(null, 'unresolved ensemble record');
    continue;
  }
  const archivedAgents = agentFiles(recordRoot);
  if (archivedAgents.unreadableDirectories.length > 0) {
    worker.span_breakdown = unparseable(null, 'unreadable ensemble record');
    continue;
  }
  if (archivedAgents.files.length === 0) {
    worker.span_breakdown = unparseable(null, 'unresolved ensemble record');
    continue;
  }
  if (worker.id === null || worker.id === undefined) {
    worker.span_breakdown = unparseable(null, 'unresolved archived agent identity');
    continue;
  }
  let detail = null;
  let invalidAgentRecord = false;
  let duplicateIdentity = false;
  for (const agent of archivedAgents.files) {
    let candidate;
    try { candidate = JSON.parse(fs.readFileSync(agent, 'utf8')); }
    catch {
      invalidAgentRecord = true;
      continue;
    }
    if (candidate.id !== worker.id) continue;
    if (detail !== null) {
      duplicateIdentity = true;
      break;
    }
    detail = candidate;
  }
  if (duplicateIdentity) {
    worker.span_breakdown = unparseable(null, 'ambiguous archived agent identity');
    continue;
  }
  if (detail === null) {
    const reason = invalidAgentRecord
      ? 'unreadable or invalid agent record'
      : 'unresolved archived agent identity';
    worker.span_breakdown = unparseable(null, reason);
    continue;
  }
  worker.span_breakdown = analyseTranscripts(detail.transcripts || [], recordRoot);
}
process.stdout.write(JSON.stringify(record));
