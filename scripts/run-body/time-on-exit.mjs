import { spawnSync } from 'node:child_process';
import { appendFileSync } from 'node:fs';
import { constants } from 'node:os';

const [eventLog, name, ...command] = process.argv.slice(2);
if (command.length === 0) {
  console.error('usage: time-on-exit EVENT_LOG NAME COMMAND [ARG...]');
  process.exit(2);
}

const startedAt = new Date();
const started = process.hrtime.bigint();
// Preserve shell command lookup, builtins, and the 126/127 execution statuses.
const result = spawnSync('/bin/sh', ['-c', '"$@"', 'time-on-exit', ...command], {
  stdio: 'inherit',
});
const durationMs = Number((process.hrtime.bigint() - started) / 1_000_000n);
const endedAt = new Date();

if (result.error) {
  console.error(`time-on-exit: could not execute ${command[0]}: ${result.error.message}`);
}
const status = result.status ?? (result.signal ? 128 + constants.signals[result.signal] : 126);

const event = {
  kind: 'minos-timing-event-v1',
  name,
  started_at: startedAt.toISOString().replace(/\.\d{3}Z$/, 'Z'),
  ended_at: endedAt.toISOString().replace(/\.\d{3}Z$/, 'Z'),
  duration_ms: durationMs,
  exit_status: status,
};
try {
  // A short line appended in one write keeps concurrent events separate.
  appendFileSync(eventLog, JSON.stringify(event) + '\n');
} catch {
  console.error(`time-on-exit: could not append the timing event for ${name} to ${eventLog}`);
}
process.exitCode = status;
