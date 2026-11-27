// The per-repository settings that change what a review prompt says, and
// the passages that change with each. The service exports a setting's value
// to the run in an environment variable. An input builder reads them all
// with promptSettingsFromEnvironment and hands them to reviewPrompts, which
// fills each of a setting's slots from the file named for the value:
// workflows/review-briefs/<slot>/<value>.md. A setting that changes two
// prompts in different ways takes a slot for each passage.
// workflows/review-briefs/README.md lists the slots for a prompt's editor.

// Each setting under the name configuration gives it in a repository's
// `review` table: the variable that carries it, the values it takes, the
// value it has when the variable is unset or empty (its documented default,
// as when an input builder is run by hand), and the file slots that follow
// it.
export const PROMPT_SETTINGS = {
  "file-safeguard-requests": {
    variable: "MINOS_FILE_SAFEGUARD_REQUESTS",
    values: ["false", "true"],
    unset: "false",
    slots: ["safeguard-requests", "verifier-safeguard-requests"],
  },
  depth: {
    variable: "MINOS_REVIEW_DEPTH",
    values: ["light", "standard", "thorough"],
    unset: "standard",
    slots: ["exploration-depth", "specialist-depth", "general-depth"],
  },
};

// Every setting's value for this run. A variable holding anything but one
// of its setting's values is refused, never read as the default.
export function promptSettingsFromEnvironment(env = process.env) {
  const settings = {};
  for (const [name, { variable, values, unset }] of Object.entries(PROMPT_SETTINGS)) {
    const value = env[variable] === undefined || env[variable] === "" ? unset : env[variable];
    if (!values.includes(value)) throw new Error(`${variable} must be one of ${values.join(", ")}`);
    settings[name] = value;
  }
  return settings;
}

// The text each setting slot takes under `settings`, by slot name. `read`
// returns the text of a file in the prompt directory. Every value's file is
// read, so a passage no repository has chosen yet is found missing when any
// input is built, and not by the first repository to choose it.
export function settingSections(read, settings) {
  const sections = {};
  for (const [name, { values, slots }] of Object.entries(PROMPT_SETTINGS)) {
    const chosen = settings?.[name];
    if (!values.includes(chosen)) throw new Error(`prompt setting ${name} must be one of ${values.join(", ")}`);
    for (const slot of slots) {
      const passages = Object.fromEntries(values.map((value) => [value, read(`${slot}/${value}.md`)]));
      sections[slot] = passages[chosen];
    }
  }
  return sections;
}
