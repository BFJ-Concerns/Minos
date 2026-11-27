// Writes the walkthrough of a head the run has just finished reviewing,
// where the repository enables one, and puts it into the summary comment.
//
// It follows the verdict in the summary, the in-flight marker withdrawal
// attempt and the run's completion record. A refused status or marker is
// reported and the walkthrough still proceeds. The pull request's run
// stays live while the stages read.
//
// A head gets a new walkthrough only when it was reviewed and judged, in
// full or as an increment. A head that needed no review, one whose run
// stopped short and one handed to a successor get none: their summary
// comment goes on showing the walkthrough kept from the last reviewed
// head, which every summary write carries (forge-writes.mjs). A head that
// moved before the walkthrough was asked for, or while it was being made,
// gets none either, and the run of the new head starts from the kept one.
//
// Nothing here can change how the run ended. A failed stage, a diagram
// that fails rendering checks, an oversized walkthrough or an unconfirmed
// write is reported as a presentation anomaly. A flow the diagrams stage
// leaves undrawn is omitted. If the diagrams stage fails, the walkthrough
// can still be published without diagrams.
//
// Takes `inspection` and `review`. Settles nothing.

import { readFileSync } from "node:fs";

import { changedPaths } from "../changed-paths.mjs";
import { drawnDiagrams, stage as walkthroughDiagrams } from "../judgements/walkthrough-diagrams.mjs";
import { stage as walkthroughStage } from "../judgements/walkthrough.mjs";
import { pullRequestFromOrientation } from "../pull-request-description.mjs";
import { descriptionFromOrientation } from "../repository-description.mjs";
import { routingFromEnvironment } from "../role-routing.mjs";
import { spendsHead } from "../run-endings.mjs";
import { RECORD_KIND, fitted, keepWalkthrough, keptWalkthrough, walkthroughSettings } from "../walkthrough.mjs";
import { currentSnapshot, movedHead, writeSummary } from "./forge-writes.mjs";
import { summaryFacts } from "./summary-facts.mjs";

export async function writeWalkthrough(run, settled, ending, problems = []) {
  if (!spendsHead(ending) || settled.review === undefined) return;
  try {
    const settings = walkthroughSettings(run.env);
    if (!settings.enabled) return;
    await currentSnapshot(run);

    const orientation = JSON.parse(readFileSync(run.orientation, "utf8"));
    const pullRequest = pullRequestFromOrientation(orientation);
    const repositoryDescription = descriptionFromOrientation(orientation);
    const described = {
      target: run.target,
      head: run.head,
      ...(repositoryDescription === undefined ? {} : { repositoryDescription }),
    };
    const kept = keptWalkthrough(run.env);
    const written = await run.judge(walkthroughStage, {
      ...described,
      files: changedPaths(run.workspace, run.target, run.head),
      ...(pullRequest === undefined ? {} : { pullRequest }),
      earlier: kept === null ? null : { head: kept.head, overview: kept.overview, groups: kept.groups },
      diagrams: settings.diagrams,
    });

    // The diagrams are an extra within the extra: the stage that draws
    // them failing leaves a walkthrough with none.
    let diagrams = [];
    const flows = written.flows ?? [];
    if (flows.length > 0) {
      try {
        diagrams = drawnDiagrams(await run.judge(walkthroughDiagrams, { ...described, flows }), flows);
      } catch (error) {
        problems.push(`The walkthrough's diagrams were not drawn. ${error.message}`);
      }
    }

    // The models named are those that answered, which a fallback may have
    // made others than the ones routed.
    const routing = routingFromEnvironment(run.env);
    const answeredBy = (stage) => run.judgementModels?.get(stage.name) ?? routing[stage.role].model;
    const { record, omitted, problem } = fitted({
      kind: RECORD_KIND,
      head: run.head,
      target: run.target,
      reviewBase: settled.inspection.reviewBase,
      overview: written.overview,
      groups: written.groups,
      diagrams,
      models: { walkthrough: answeredBy(walkthroughStage), diagrams: answeredBy(walkthroughDiagrams) },
    });
    problems.push(...omitted);
    if (problem) throw new Error(problem);

    // The same state for the same head, with the walkthrough beside what
    // `conclude` wrote. Only a walkthrough the forge holds is kept.
    try {
      await writeSummary(run, ending.outcome, { ...summaryFacts(settled), walkthrough: record });
    } catch (error) {
      if (movedHead(error) || !Object.hasOwn(error, "result") || error.result?.outcome === "rejected") throw error;
      problems.push(`The walkthrough could not be confirmed. ${error.message}`);
      return;
    }
    try {
      keepWalkthrough(run.env, record);
    } catch (error) {
      problems.push(`The walkthrough was published and could not be kept for the next head. ${error.message}`);
    }
  } catch (error) {
    if (movedHead(error)) return;
    problems.push(`The walkthrough was not written. ${error.message}`);
  }
}
