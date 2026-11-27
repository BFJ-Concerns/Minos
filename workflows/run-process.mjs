import { execFile } from "node:child_process";
import { existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export function runCommand(run, command, args) {
  return new Promise((resolve, reject) => {
    execFile(command, args, { cwd: run.workspace, env: run.env, maxBuffer: Infinity }, (error, stdout, stderr) => {
      if (error) {
        reject(new Error(`${args[0] ?? command}: ${stderr.trim() || error.message}`));
      } else {
        resolve(stdout);
      }
    });
  });
}

// A warm successor keeps the previous attempts and gives each new dispatch
// its own input, result, timings and worker records.
export function prepareStage(run, name, input) {
  let attempt = name;
  for (let number = 2; existsSync(join(run.runDir, `${attempt}-args.json`)); number++) {
    attempt = `${name}@${number}`;
  }
  writeFileSync(join(run.runDir, `${attempt}-args.json`), input);
  return attempt;
}
