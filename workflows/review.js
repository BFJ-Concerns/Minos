export const meta = {
  name: "minos-review",
  description: "Independent review with cross-family verification"
};

export const defaults = {
  codex: { model: "gpt-5.6-sol", effort: "medium" },
  claude: { model: "opus", effort: "medium" }
};

const finding = {
  type: "object",
  additionalProperties: false,
  required: ["title", "priority", "path", "line", "explanation"],
  properties: {
    title: { type: "string" },
    priority: { type: "string", enum: ["critical", "high", "medium", "low"] },
    path: { type: "string" },
    line: { type: "integer", minimum: 1 },
    explanation: { type: "string" }
  }
};

const reviewSchema = {
  type: "object",
  additionalProperties: false,
  required: ["findings"],
  properties: { findings: { type: "array", items: finding } }
};

const verdictSchema = {
  type: "object",
  additionalProperties: false,
  required: ["confirmed", "reason"],
  properties: {
    confirmed: { type: "boolean" },
    reason: { type: "string" }
  }
};

const base = args.target;
const head = args.head;
const common = `Review the complete git diff ${base}...${head} in the current repository. Read relevant files in full. Report only specific defects introduced by this change that are worth fixing. Return {findings:[{title,priority,path,line,explanation}]}.`;
const briefs = [
  ["codex", `${common}\nFocus on correctness, edge cases, and error handling.`],
  ["claude", `${common}\nFocus on security and unsafe trust assumptions.`],
  ["codex", `${common}\nFocus on tests and behaviour that the change fails to cover.`],
  ["claude", `${common}\nFocus on unnecessary complexity and simpler equivalent designs.`]
];

const reviews = await parallel(briefs.map(([engine, prompt], index) => () =>
  agent(prompt, { engine, schema: reviewSchema, label: `review-${index + 1}` })
));

const candidates = [];
for (let i = 0; i < reviews.length; i++) {
	if (!reviews[i]) continue;
  for (const item of reviews[i].findings) {
    candidates.push({ source: briefs[i][0], finding: item });
  }
}

const checked = await parallel(candidates.map((candidate, index) => () =>
  agent(
    `Independently try to disprove this proposed finding against the repository and diff ${base}...${head}. Confirm it only if the cited code demonstrates a real defect introduced by the change. Return {confirmed:boolean,reason:string}. Finding: ${JSON.stringify(candidate.finding)}`,
    { engine: candidate.source === "codex" ? "claude" : "codex", schema: verdictSchema, label: `verify-${index + 1}` }
  )
));

return {
  reviewed: { target: base, head },
  findings: candidates.map((candidate, index) => ({
    ...candidate.finding,
    confirmed: checked[index]?.confirmed === true,
    verification: checked[index]?.reason || "Verifier did not return a result"
  }))
};
