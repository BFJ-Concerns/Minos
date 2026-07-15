package shell

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"bfj/minos/internal/destination"
	"bfj/minos/internal/findings"
	"bfj/minos/internal/forge"
	"bfj/minos/internal/ledger"
	"bfj/minos/internal/product"
)

type panelCriterion struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type panelFinding struct {
	Brief              string   `json:"brief"`
	ReviewTitle        *string  `json:"review_title,omitempty"`
	Scope              *string  `json:"scope,omitempty"`
	Extent             string   `json:"extent,omitempty"`
	File               string   `json:"file"`
	Line               *int     `json:"line,omitempty"`
	Side               string   `json:"side,omitempty"`
	Priority           string   `json:"priority"`
	Title              string   `json:"title"`
	Message            string   `json:"message"`
	CodeQuote          string   `json:"code_quote"`
	Suggestion         string   `json:"suggestion,omitempty"`
	Preexisting        bool     `json:"preexisting,omitempty"`
	Confidence         *float64 `json:"confidence,omitempty"`
	LineageID          string   `json:"lineage_id,omitempty"`
	Producer           string   `json:"producer,omitempty"`
	Attribution        string   `json:"attribution,omitempty"`
	ProducerIdentity   string   `json:"producer_identity"`
	ProducerOrdinal    int      `json:"producer_ordinal"`
	Assurance          string   `json:"assurance"`
	CheckedBy          string   `json:"checked_by,omitempty"`
	ProposedPriority   string   `json:"proposed_priority,omitempty"`
	VerifierPriority   string   `json:"verifier_priority,omitempty"`
	VerificationState  string   `json:"verification_outcome,omitempty"`
	PriorityValidation struct {
		Agreement string `json:"agreement"`
		Rationale string `json:"rationale"`
	} `json:"priority_validation,omitempty"`
}

type panelVerificationEvidence struct {
	CheckerFamily   *string `json:"checker_family"`
	CheckerID       *string `json:"checker_id"`
	DegradedPairing bool    `json:"degraded_pairing"`
	Rationale       *string `json:"rationale"`
}

type panelCandidate struct {
	Candidate            panelFinding              `json:"candidate"`
	Producer             findings.Producer         `json:"producer"`
	Assurance            string                    `json:"assurance"`
	Outcome              findings.CandidateOutcome `json:"outcome"`
	ProposedPriority     string                    `json:"proposed_priority"`
	VerifierPriority     *string                   `json:"verifier_priority"`
	VerifiedPriority     *string                   `json:"verified_priority"`
	VerificationEvidence panelVerificationEvidence `json:"verification_evidence"`
	VerifiedFinding      *panelFinding             `json:"verified_finding"`
}

type panelVerificationResult struct {
	SchemaVersion int              `json:"schema_version"`
	Criteria      []panelCriterion `json:"criteria"`
	Candidates    []panelCandidate `json:"candidates"`
}

type priorOccurrence struct {
	Context         findings.ManifestContext `json:"context"`
	Finding         findings.VerifiedFinding `json:"finding"`
	RepairCommitSHA string                   `json:"repair_commit_sha,omitempty"`
}

type priorOccurrenceSet struct {
	SchemaVersion int               `json:"schema_version"`
	Occurrences   []priorOccurrence `json:"occurrences"`
}

type findingCommandContext struct {
	Config    ServiceConfig
	Repo      RepoConfig
	Facts     Facts
	Store     *ledger.Store
	Token     int64
	Lease     ledger.Lease
	Workspace string
}

func FindingsCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: minos findings assemble|deliver|repair-plan|inspect [arguments]")
	}
	command, err := loadFindingCommand(ctx)
	if err != nil {
		return err
	}
	defer command.Store.Close()
	switch args[0] {
	case "assemble":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: minos findings assemble VERIFICATION_JSON [PRIOR_OCCURRENCES_JSON]")
		}
		manifest, err := assembleDispositionManifest(command, args[1], optionalArgument(args, 2))
		if err != nil {
			return err
		}
		return writeJSON(stdout, manifest)
	case "deliver":
		if len(args) != 3 {
			return fmt.Errorf("usage: minos findings deliver MANIFEST_JSON BAR_ATTESTATION_JSON")
		}
		manifest, err := deliverDispositionManifest(ctx, command, args[1], args[2])
		if err != nil {
			return err
		}
		return writeJSON(stdout, manifest)
	case "repair-plan":
		if len(args) < 3 || len(args) > 4 {
			return fmt.Errorf("usage: minos findings repair-plan DECISION_MANIFEST_JSON BAR_ATTESTATION_JSON [DELIVERY_MANIFEST_JSON]")
		}
		plan, err := buildRepairPlan(ctx, command, args[1], args[2], optionalArgument(args, 3))
		if err != nil {
			return err
		}
		return writeJSON(stdout, plan)
	case "inspect":
		if len(args) != 2 {
			return fmt.Errorf("usage: minos findings inspect MANIFEST_JSON")
		}
		manifest, err := readOwnedManifest(args[1], command)
		if err != nil {
			return err
		}
		digest, err := manifest.Digest()
		if err != nil {
			return err
		}
		index, err := manifest.Index()
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			SchemaVersion  int                       `json:"schema_version"`
			ManifestSHA256 string                    `json:"manifest_sha256"`
			Material       bool                      `json:"material_intervention_triggered"`
			Index          findings.DispositionIndex `json:"index"`
		}{1, digest, manifest.HasMaterialFindings(), index})
	default:
		return fmt.Errorf("unknown findings action %q", args[0])
	}
}

type repairPlan struct {
	SchemaVersion          int                        `json:"schema_version"`
	DecisionManifestSHA256 string                     `json:"decision_manifest_sha256"`
	DeliveryManifestSHA256 string                     `json:"delivery_manifest_sha256,omitempty"`
	Findings               []findings.VerifiedFinding `json:"findings"`
}

func buildRepairPlan(ctx context.Context, command findingCommandContext, decisionPath, attestationPath, deliveryPath string) (repairPlan, error) {
	decision, err := readOwnedManifest(decisionPath, command)
	if err != nil {
		return repairPlan{}, err
	}
	attestationData, err := os.ReadFile(attestationPath)
	if err != nil {
		return repairPlan{}, err
	}
	if _, err := findings.DecodeBarAttestation(attestationData, decision); err != nil {
		return repairPlan{}, err
	}
	if !decision.HasMaterialFindings() || decision.HasConfirmedQuietDelivery() {
		return repairPlan{}, fmt.Errorf("repair planning requires the original pre-delivery material decision manifest")
	}
	if err := requirePublishedManifest(ctx, command, decision); err != nil {
		return repairPlan{}, err
	}
	decisionDigest, err := decision.Digest()
	if err != nil {
		return repairPlan{}, err
	}
	plan := repairPlan{SchemaVersion: 1, DecisionManifestSHA256: decisionDigest, Findings: []findings.VerifiedFinding{}}
	needsDelivery := false
	for _, entry := range decision.Findings {
		if entry.Delivery == findings.DeliveryPending {
			needsDelivery = true
		}
		if entry.Repair == findings.RepairSelected {
			plan.Findings = append(plan.Findings, entry.Finding)
		}
	}
	if len(plan.Findings) == 0 {
		return repairPlan{}, fmt.Errorf("material intervention selected no repair-eligible findings")
	}
	if needsDelivery {
		if deliveryPath == "" {
			return repairPlan{}, fmt.Errorf("destination-mode material repair requires the confirmed delivery manifest")
		}
		delivery, err := readOwnedManifest(deliveryPath, command)
		if err != nil {
			return repairPlan{}, err
		}
		if err := validateDeliverySnapshot(decision, delivery); err != nil {
			return repairPlan{}, err
		}
		plan.DeliveryManifestSHA256, err = delivery.Digest()
		if err != nil {
			return repairPlan{}, err
		}
	} else if deliveryPath != "" {
		return repairPlan{}, fmt.Errorf("repair plan received an unnecessary delivery snapshot")
	}
	return plan, nil
}

func validateDeliverySnapshot(decision, delivery findings.DispositionManifest) error {
	if !reflect.DeepEqual(decision.Context, delivery.Context) || decision.Policy != delivery.Policy || !reflect.DeepEqual(decision.Candidates, delivery.Candidates) || !reflect.DeepEqual(decision.RepairEvidence, delivery.RepairEvidence) || len(decision.Findings) != len(delivery.Findings) {
		return fmt.Errorf("delivery snapshot does not preserve the exact material decision input")
	}
	for index := range decision.Findings {
		before, after := decision.Findings[index], delivery.Findings[index]
		if before.Delivery == findings.DeliveryPending {
			if after.Delivery != findings.DeliveryConfirmed || after.Receipt == nil {
				return fmt.Errorf("delivery snapshot lacks confirmation for occurrence %s", before.Finding.OccurrenceID)
			}
			after.Delivery = findings.DeliveryPending
			after.Receipt = nil
		}
		if !reflect.DeepEqual(before, after) {
			return fmt.Errorf("delivery snapshot changed decision fields for occurrence %s", before.Finding.OccurrenceID)
		}
	}
	return nil
}

func loadFindingCommand(ctx context.Context) (findingCommandContext, error) {
	cfg, err := LoadServiceConfig(os.Getenv("MINOS_CONFIG"))
	if err != nil {
		return findingCommandContext{}, err
	}
	facts := envFacts(os.Getenv("MINOS_FORGE"))
	repo, err := FindRepoConfig(cfg, facts)
	if err != nil {
		return findingCommandContext{}, err
	}
	store, err := ledger.Open(ledgerPath(cfg))
	if err != nil {
		return findingCommandContext{}, err
	}
	token, err := attemptToken()
	if err != nil {
		store.Close()
		return findingCommandContext{}, err
	}
	lease, err := ownedLease(ctx, store, coordinationKey(facts), token)
	if err != nil {
		store.Close()
		return findingCommandContext{}, err
	}
	workspace, err := filepath.Abs(os.Getenv("MINOS_WORKSPACE"))
	if err != nil || strings.TrimSpace(os.Getenv("MINOS_WORKSPACE")) == "" {
		store.Close()
		return findingCommandContext{}, fmt.Errorf("MINOS_WORKSPACE is required")
	}
	leaseWorkspace, err := filepath.Abs(lease.Workspace)
	if err != nil || workspace != leaseWorkspace {
		store.Close()
		return findingCommandContext{}, fmt.Errorf("MINOS_WORKSPACE does not match the currently owned lease")
	}
	return findingCommandContext{Config: cfg, Repo: repo, Facts: facts, Store: store, Token: token, Lease: lease, Workspace: workspace}, nil
}

func assembleDispositionManifest(command findingCommandContext, verificationPath, priorPath string) (findings.DispositionManifest, error) {
	var result panelVerificationResult
	if err := decodeStrictJSONFile(verificationPath, &result); err != nil {
		return findings.DispositionManifest{}, err
	}
	if result.SchemaVersion != 1 {
		return findings.DispositionManifest{}, fmt.Errorf("unsupported verification result schema %d", result.SchemaVersion)
	}
	if err := verifyWorkspaceRevisions(command); err != nil {
		return findings.DispositionManifest{}, err
	}
	changed, err := diffChangedEvidenceLines(os.Getenv("MINOS_DIFF"))
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	criteria, err := resolveCriteria(command, result.Criteria)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	prior, err := loadPriorOccurrences(priorPath, command)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	contextRecord, err := manifestContext(command)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	existingLineages := make(map[product.FindingID]struct{}, len(prior))
	for _, occurrence := range prior {
		id, parseErr := product.ParseFindingID(string(occurrence.Finding.LineageID))
		if parseErr != nil {
			return findings.DispositionManifest{}, parseErr
		}
		existingLineages[id] = struct{}{}
	}
	candidates := make([]findings.CandidateDisposition, 0, len(result.Candidates))
	verified := make([]findings.VerifiedFinding, 0, len(result.Candidates))
	repairEvidence := make([]findings.RepairEvidence, 0)
	excluded := make([]panelCandidate, 0)
	seenProducer := make(map[string]struct{}, len(result.Candidates))
	for _, entry := range result.Candidates {
		if err := validatePanelProducer(entry); err != nil {
			return findings.DispositionManifest{}, err
		}
		if entry.Candidate.Preexisting {
			excluded = append(excluded, entry)
			continue
		}
		key := entry.Producer.Family + "\x00" + entry.Producer.ID + "\x00" + strconv.Itoa(entry.Producer.Ordinal)
		if _, duplicate := seenProducer[key]; duplicate {
			return findings.DispositionManifest{}, fmt.Errorf("verification result repeats producer candidate %q", key)
		}
		seenProducer[key] = struct{}{}
		candidate, err := admitCandidate(command, contextRecord, criteria, changed, entry)
		if err != nil {
			_ = recordExcludedProposals(command, append(excluded, entry), err.Error())
			return findings.DispositionManifest{}, err
		}
		evidence, err := verificationEvidence(entry)
		if err != nil {
			return findings.DispositionManifest{}, err
		}
		disposition := findings.CandidateDisposition{Candidate: candidate, Outcome: entry.Outcome, VerificationEvidence: evidence}
		if entry.Outcome == findings.CandidateVerified {
			finding, err := makeVerifiedFinding(contextRecord, candidate, entry, prior, existingLineages)
			if err != nil {
				return findings.DispositionManifest{}, err
			}
			disposition.OccurrenceID = finding.OccurrenceID
			verified = append(verified, finding)
			evidence, ok, err := successorRepairEvidence(finding, prior)
			if err != nil {
				return findings.DispositionManifest{}, err
			}
			if ok {
				repairEvidence = append(repairEvidence, evidence)
			}
		}
		candidates = append(candidates, disposition)
	}
	if len(excluded) > 0 {
		if err := recordExcludedProposals(command, excluded, "pre-existing proposal retained as attempt evidence outside the current-head manifest"); err != nil {
			return findings.DispositionManifest{}, err
		}
	}
	manifest, err := findings.NewDispositionManifest(contextRecord, command.Repo.FindingPolicy(), candidates, verified)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	manifest.RepairEvidence = repairEvidence
	if err := manifest.Validate(); err != nil {
		return findings.DispositionManifest{}, err
	}
	return manifest, nil
}

func validatePanelProducer(entry panelCandidate) error {
	expectedFamily := "claude"
	if strings.HasSuffix(entry.Producer.ID, "@codex-cli") {
		expectedFamily = "codex"
	}
	if entry.Producer.Family != expectedFamily || entry.Candidate.Producer != entry.Producer.ID || entry.Candidate.ProducerIdentity != entry.Producer.ID || entry.Candidate.ProducerOrdinal != entry.Producer.Ordinal {
		return fmt.Errorf("verification candidate producer identity is inconsistent")
	}
	return nil
}

func admitCandidate(command findingCommandContext, contextRecord findings.ManifestContext, criteria map[string]findings.Criterion, changed map[evidenceLine]bool, entry panelCandidate) (findings.CandidateFinding, error) {
	raw := entry.Candidate
	if entry.Assurance != findings.AgentJudgement.String() || raw.Assurance != findings.AgentJudgement.String() || raw.ProducerIdentity != entry.Producer.ID || raw.ProducerOrdinal != entry.Producer.Ordinal || entry.ProposedPriority != raw.Priority {
		return findings.CandidateFinding{}, fmt.Errorf("candidate producer, assurance, or proposed priority changed across panel verification")
	}
	priority, err := findings.ParsePriority(entry.ProposedPriority)
	if err != nil {
		return findings.CandidateFinding{}, err
	}
	if raw.Line == nil || *raw.Line <= 0 {
		return findings.CandidateFinding{}, fmt.Errorf("candidate %q lacks a positive display line", raw.Title)
	}
	if err := validateReviewPath(raw.File); err != nil {
		return findings.CandidateFinding{}, err
	}
	side := findings.HeadSide
	lineSide := headEvidenceLine
	revision := command.Lease.ObservedHead
	if raw.Side == "LEFT" {
		side = findings.BaseSide
		lineSide = baseEvidenceLine
		revision = command.Lease.ObservedTarget
	} else if raw.Side != "" && raw.Side != "RIGHT" {
		return findings.CandidateFinding{}, fmt.Errorf("candidate %q has invalid evidence side %q", raw.Title, raw.Side)
	}
	if !changed[evidenceLine{path: raw.File, line: *raw.Line, side: lineSide}] {
		return findings.CandidateFinding{}, fmt.Errorf("candidate evidence %s:%d is not a changed %s line", raw.File, *raw.Line, side)
	}
	anchor, err := anchoredQuote(command.Workspace, revision, raw.File, side, *raw.Line, raw.CodeQuote)
	if err != nil {
		return findings.CandidateFinding{}, err
	}
	criterion, ok := criteria[raw.Brief]
	if !ok && raw.Brief == "codex-review" {
		category := findings.DefectGeneralCorrectness
		criterion = findings.Criterion{Defect: &category}
		ok = true
	}
	if !ok {
		return findings.CandidateFinding{}, fmt.Errorf("candidate names unknown criterion %q", raw.Brief)
	}
	candidateID, err := findings.NewCandidateID(contextRecord.AttemptToken, entry.Producer)
	if err != nil {
		return findings.CandidateFinding{}, err
	}
	candidate := findings.CandidateFinding{SchemaVersion: 1, CandidateID: candidateID, Anchor: anchor, Criterion: criterion, ProposedPriority: priority, Assurance: findings.AgentJudgement, Title: raw.Title, Message: raw.Message, Suggestion: raw.Suggestion, Producer: entry.Producer}
	if err := findings.ValidateCandidate(candidate); err != nil {
		return findings.CandidateFinding{}, err
	}
	return candidate, nil
}

func makeVerifiedFinding(contextRecord findings.ManifestContext, candidate findings.CandidateFinding, entry panelCandidate, prior []priorOccurrence, existing map[product.FindingID]struct{}) (findings.VerifiedFinding, error) {
	if entry.VerifiedFinding == nil || entry.VerifierPriority == nil || entry.VerifiedPriority == nil {
		return findings.VerifiedFinding{}, fmt.Errorf("verified candidate %s lacks the complete verified finding", candidate.CandidateID)
	}
	verifierPriority, err := findings.ParsePriority(*entry.VerifierPriority)
	if err != nil {
		return findings.VerifiedFinding{}, err
	}
	verifiedPriority, err := findings.ParsePriority(*entry.VerifiedPriority)
	if err != nil {
		return findings.VerifiedFinding{}, err
	}
	moreSevere, err := findings.MoreSevere(candidate.ProposedPriority, verifierPriority)
	if err != nil || verifiedPriority != moreSevere {
		return findings.VerifiedFinding{}, fmt.Errorf("verified candidate %s does not preserve the conservative priority result", candidate.CandidateID)
	}
	evidence, err := verificationEvidence(entry)
	if err != nil {
		return findings.VerifiedFinding{}, err
	}
	verifiedRaw := entry.VerifiedFinding
	if verifiedRaw.Brief != entry.Candidate.Brief || verifiedRaw.File != entry.Candidate.File || verifiedRaw.Line == nil || entry.Candidate.Line == nil || *verifiedRaw.Line != *entry.Candidate.Line || verifiedRaw.Side != entry.Candidate.Side || verifiedRaw.CodeQuote != entry.Candidate.CodeQuote || verifiedRaw.Title != candidate.Title || verifiedRaw.Message != candidate.Message || verifiedRaw.Suggestion != candidate.Suggestion || verifiedRaw.LineageID != entry.Candidate.LineageID || verifiedRaw.Producer != candidate.Producer.ID || verifiedRaw.ProducerIdentity != candidate.Producer.ID || verifiedRaw.ProducerOrdinal != candidate.Producer.Ordinal || verifiedRaw.Priority != verifiedPriority.String() {
		return findings.VerifiedFinding{}, fmt.Errorf("verified candidate %s changed immutable proposal fields", candidate.CandidateID)
	}
	agreement := findings.PriorityAgreed
	if candidate.ProposedPriority != verifierPriority {
		agreement = findings.PriorityDisputed
	}
	if verifiedRaw.CheckedBy != evidence.Verifier.Family || verifiedRaw.ProposedPriority != candidate.ProposedPriority.String() || verifiedRaw.VerifierPriority != verifierPriority.String() || verifiedRaw.VerificationState != "verified" || verifiedRaw.PriorityValidation.Agreement != string(agreement) || verifiedRaw.PriorityValidation.Rationale != evidence.Rationale {
		return findings.VerifiedFinding{}, fmt.Errorf("verified candidate %s has inconsistent verification evidence", candidate.CandidateID)
	}
	lineage, err := resolveLineage(candidate, entry.Candidate.LineageID, prior, existing)
	if err != nil {
		return findings.VerifiedFinding{}, err
	}
	finding := findings.VerifiedFinding{
		SchemaVersion: 1, CandidateID: candidate.CandidateID, LineageID: lineage,
		Anchor: candidate.Anchor, Criterion: candidate.Criterion, ProposedPriority: candidate.ProposedPriority,
		VerifierPriority: verifierPriority, VerifiedPriority: verifiedPriority,
		PriorityValidation: findings.PriorityValidation{VerifierFamily: evidence.Verifier.Family, VerifierID: evidence.Verifier.ID, Agreement: agreement, Rationale: evidence.Rationale},
		Assurance:          candidate.Assurance, Title: candidate.Title, Message: candidate.Message, Suggestion: candidate.Suggestion,
		Producer: candidate.Producer, Verifier: evidence.Verifier,
	}
	finding.OccurrenceID, err = findings.NewOccurrenceID(contextRecord, finding)
	if err != nil {
		return findings.VerifiedFinding{}, err
	}
	return finding, nil
}

func verificationEvidence(entry panelCandidate) (findings.VerificationEvidence, error) {
	rationale := ""
	if entry.VerificationEvidence.Rationale != nil {
		rationale = strings.TrimSpace(*entry.VerificationEvidence.Rationale)
	}
	if rationale == "" {
		return findings.VerificationEvidence{}, fmt.Errorf("candidate verification lacks rationale")
	}
	var verifier findings.Producer
	if entry.VerificationEvidence.CheckerFamily != nil && entry.VerificationEvidence.CheckerID != nil {
		verifier = findings.Producer{Family: *entry.VerificationEvidence.CheckerFamily, ID: *entry.VerificationEvidence.CheckerID, Ordinal: entry.Producer.Ordinal}
	}
	if entry.Outcome != findings.CandidateVerificationUnresolved && (strings.TrimSpace(verifier.Family) == "" || strings.TrimSpace(verifier.ID) == "") {
		return findings.VerificationEvidence{}, fmt.Errorf("candidate %s lacks independent verifier evidence", entry.Producer.ID)
	}
	if verifier.Family != "" {
		if verifier.Family != "codex" && verifier.Family != "claude" {
			return findings.VerificationEvidence{}, fmt.Errorf("candidate %s has unknown verifier family", entry.Producer.ID)
		}
		expectedID := fmt.Sprintf("check:%s:%d@%s", entry.Producer.ID, entry.Producer.Ordinal, verifier.Family)
		if verifier.ID != expectedID {
			return findings.VerificationEvidence{}, fmt.Errorf("candidate %s has inconsistent verifier identity", entry.Producer.ID)
		}
	}
	return findings.VerificationEvidence{Verifier: verifier, Rationale: rationale}, nil
}

func resolveCriteria(command findingCommandContext, input []panelCriterion) (map[string]findings.Criterion, error) {
	criteria := make(map[string]findings.Criterion, len(input))
	for _, item := range input {
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Path) == "" {
			return nil, fmt.Errorf("verification criterion name and path are required")
		}
		if _, duplicate := criteria[item.Name]; duplicate {
			return nil, fmt.Errorf("duplicate verification criterion %q", item.Name)
		}
		var briefPath, blobSHA string
		if filepath.IsAbs(item.Path) {
			aspectRoot := filepath.Clean(filepath.Join(filepath.Dir(command.Repo.Adaptation.Skill), "aspects"))
			clean := filepath.Clean(item.Path)
			relative, err := filepath.Rel(aspectRoot, clean)
			if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.Base(relative) != relative {
				return nil, fmt.Errorf("criterion %q is outside the configured review-panel aspects", item.Name)
			}
			data, err := os.ReadFile(clean)
			if err != nil {
				return nil, err
			}
			digest := sha256.Sum256(data)
			blobSHA = hex.EncodeToString(digest[:])
			briefPath = filepath.ToSlash(filepath.Join("review-panel", "aspects", relative))
		} else {
			if err := validateReviewPath(item.Path); err != nil {
				return nil, fmt.Errorf("criterion %q: %w", item.Name, err)
			}
			var err error
			blobSHA, err = gitOutput(command.Workspace, "rev-parse", command.Lease.ObservedTarget+":"+item.Path)
			if err != nil {
				return nil, fmt.Errorf("criterion %q is unavailable at trusted target %s: %w", item.Name, command.Lease.ObservedTarget, err)
			}
			briefPath = item.Path
		}
		criteria[item.Name] = findings.Criterion{Brief: &findings.BriefCriterion{TrustedTargetSHA: command.Lease.ObservedTarget, BriefPath: briefPath, BriefBlobSHA: blobSHA}}
	}
	return criteria, nil
}

func anchoredQuote(workspace, revision, path string, side findings.EvidenceSide, line int, quote string) (findings.EvidenceAnchor, error) {
	if quote == "" {
		return findings.EvidenceAnchor{}, fmt.Errorf("evidence quote is empty")
	}
	blob, err := gitBytes(workspace, "show", revision+":"+path)
	if err != nil {
		return findings.EvidenceAnchor{}, fmt.Errorf("read immutable evidence %s:%s: %w", revision, path, err)
	}
	if count := bytes.Count(blob, []byte(quote)); count != 1 {
		return findings.EvidenceAnchor{}, fmt.Errorf("evidence quote for %s occurs %d times in immutable blob", path, count)
	}
	blobSHA, err := gitOutput(workspace, "rev-parse", revision+":"+path)
	if err != nil {
		return findings.EvidenceAnchor{}, err
	}
	anchor := findings.NewEvidenceAnchor(path, side, line, blobSHA, quote)
	return anchor, anchor.Validate()
}

func resolveLineage(candidate findings.CandidateFinding, claim string, prior []priorOccurrence, existing map[product.FindingID]struct{}) (findings.LineageID, error) {
	matches := make([]findings.LineageID, 0, 1)
	for _, occurrence := range prior {
		anchor := occurrence.Finding.Anchor
		if anchor.Path == candidate.Anchor.Path && anchor.Side == candidate.Anchor.Side && anchor.QuoteSHA256 == candidate.Anchor.QuoteSHA256 && reflect.DeepEqual(occurrence.Finding.Criterion, candidate.Criterion) {
			matches = append(matches, occurrence.Finding.LineageID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if claim != "" {
		claimed, err := findings.ParseLineageID(claim)
		if err == nil {
			claimMatches := 0
			for _, occurrence := range prior {
				if occurrence.Finding.LineageID == claimed && strings.TrimSpace(occurrence.RepairCommitSHA) != "" {
					claimMatches++
				}
			}
			if claimMatches == 1 {
				return claimed, nil
			}
		}
	}
	minted, err := product.MintFindingID(existing)
	if err != nil {
		return "", err
	}
	existing[minted] = struct{}{}
	return findings.ParseLineageID(minted.String())
}

func successorRepairEvidence(finding findings.VerifiedFinding, prior []priorOccurrence) (findings.RepairEvidence, bool, error) {
	var matched *priorOccurrence
	for index := range prior {
		occurrence := &prior[index]
		if occurrence.Finding.LineageID != finding.LineageID || strings.TrimSpace(occurrence.RepairCommitSHA) == "" {
			continue
		}
		if matched != nil {
			return findings.RepairEvidence{}, false, fmt.Errorf("lineage %s has ambiguous repair evidence", finding.LineageID)
		}
		matched = occurrence
	}
	if matched == nil {
		return findings.RepairEvidence{}, false, nil
	}
	return findings.RepairEvidence{PriorOccurrenceID: matched.Finding.OccurrenceID, LineageID: finding.LineageID, RepairCommitSHA: matched.RepairCommitSHA}, true, nil
}

func loadPriorOccurrences(path string, command findingCommandContext) ([]priorOccurrence, error) {
	if path == "" {
		return []priorOccurrence{}, nil
	}
	var set priorOccurrenceSet
	if err := decodeStrictJSONFile(path, &set); err != nil {
		return nil, err
	}
	if set.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported prior occurrence schema %d", set.SchemaVersion)
	}
	seenOccurrences := make(map[findings.OccurrenceID]struct{}, len(set.Occurrences))
	for _, occurrence := range set.Occurrences {
		if occurrence.Context.Forge != command.Facts.Forge || occurrence.Context.Owner != command.Facts.Owner || occurrence.Context.Repo != command.Facts.Repo || strconv.FormatInt(occurrence.Context.PullRequest, 10) != command.Facts.PR {
			return nil, fmt.Errorf("prior occurrence is outside the current pull request lineage scope")
		}
		if err := findings.ValidateVerifiedFinding(occurrence.Finding); err != nil {
			return nil, err
		}
		expected, err := findings.NewOccurrenceID(occurrence.Context, occurrence.Finding)
		if err != nil || expected != occurrence.Finding.OccurrenceID {
			return nil, fmt.Errorf("prior occurrence identity does not match its context")
		}
		if _, duplicate := seenOccurrences[occurrence.Finding.OccurrenceID]; duplicate {
			return nil, fmt.Errorf("duplicate prior occurrence %s", occurrence.Finding.OccurrenceID)
		}
		seenOccurrences[occurrence.Finding.OccurrenceID] = struct{}{}
		if occurrence.RepairCommitSHA != "" {
			if _, err := gitOutput(command.Workspace, "rev-parse", "--verify", occurrence.RepairCommitSHA+"^{commit}"); err != nil {
				return nil, fmt.Errorf("prior occurrence repair commit is unavailable: %w", err)
			}
			ancestor := exec.Command("git", "-C", command.Workspace, "merge-base", "--is-ancestor", occurrence.RepairCommitSHA, command.Lease.ObservedHead)
			ancestor.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C"}
			if err := ancestor.Run(); err != nil {
				return nil, fmt.Errorf("prior occurrence repair commit is not contained in the current head")
			}
		}
	}
	return set.Occurrences, nil
}

func deliverDispositionManifest(ctx context.Context, command findingCommandContext, manifestPath, attestationPath string) (findings.DispositionManifest, error) {
	manifest, err := readOwnedManifest(manifestPath, command)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	attestationData, err := os.ReadFile(attestationPath)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	attestation, err := findings.DecodeBarAttestation(attestationData, manifest)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	if !manifest.HasMaterialFindings() && attestation.Verdict != findings.BarPass {
		return findings.DispositionManifest{}, fmt.Errorf("clean destination delivery requires a passing initial bar attestation")
	}
	if manifest.Policy.Mode != findings.DestinationMode {
		return findings.DispositionManifest{}, fmt.Errorf("publish-through-p3 manifests have no durable destination delivery")
	}
	if manifest.HasMaterialFindings() {
		if err := requirePublishedManifest(ctx, command, manifest); err != nil {
			return findings.DispositionManifest{}, err
		}
	}
	configured := command.Config.FindingDestinations[manifest.Policy.Destination]
	adapter, err := destination.NewAdapter(destination.Config{Name: manifest.Policy.Destination, Target: manifest.Policy.Target, Adaptation: configured.Adaptation, Endpoint: configured.Endpoint, CredentialFile: configured.CredentialFile, ExpectedPrincipal: configured.ExpectedPrincipal})
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	for _, disposition := range append([]findings.FindingDisposition(nil), manifest.Findings...) {
		if disposition.Delivery != findings.DeliveryPending {
			continue
		}
		record, err := destination.NewDurableRecord(manifest.Policy.Destination, manifest.Policy.Target, disposition)
		if err != nil {
			return findings.DispositionManifest{}, err
		}
		proof, err := adapter.Deliver(ctx, record, func(ctx context.Context) error {
			return reloadFindingAuthority(ctx, manifest)
		})
		if err != nil {
			return findings.DispositionManifest{}, err
		}
		if err := manifest.ConfirmDelivery(disposition.Finding.OccurrenceID, proof.Receipt); err != nil {
			return findings.DispositionManifest{}, err
		}
	}
	return manifest, nil
}

func reloadFindingAuthority(ctx context.Context, manifest findings.DispositionManifest) error {
	command, err := loadFindingCommand(ctx)
	if err != nil {
		return err
	}
	defer command.Store.Close()
	return validateOwnedManifest(manifest, command)
}

func requirePublishedManifest(ctx context.Context, command findingCommandContext, manifest findings.DispositionManifest) error {
	adapter, err := newBehaviouralForge(command.Config, command.Facts.Forge, denyForgeMutation{})
	if err != nil {
		return err
	}
	pullRequest, err := strconv.ParseInt(command.Facts.PR, 10, 64)
	if err != nil {
		return err
	}
	snapshot, err := adapter.Snapshot(ctx, forge.Repository{Owner: command.Facts.Owner, Name: command.Facts.Repo}, pullRequest)
	if err != nil {
		return err
	}
	wantIndex, err := manifest.Index()
	if err != nil {
		return err
	}
	for _, review := range snapshot.Reviews {
		if review.User != command.Config.Service.BotLogin || review.CommitID != manifest.Context.HeadSHA {
			continue
		}
		index, ok := dispositionIndexFromBody(review.Body)
		if ok && reflect.DeepEqual(index, wantIndex) {
			return nil
		}
	}
	return fmt.Errorf("material-head destination delivery requires a service-owned review with the exact complete manifest index")
}

func dispositionIndexFromBody(body string) (findings.DispositionIndex, bool) {
	var found *findings.DispositionIndex
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "<!-- Minos-Disposition: ") {
			continue
		}
		index, err := findings.ParseDispositionRecord(line)
		if err != nil || found != nil {
			return findings.DispositionIndex{}, false
		}
		found = &index
	}
	if found == nil {
		return findings.DispositionIndex{}, false
	}
	return *found, true
}

func readOwnedManifest(path string, command findingCommandContext) (findings.DispositionManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	manifest, err := findings.DecodeManifest(data)
	if err != nil {
		return findings.DispositionManifest{}, err
	}
	if err := validateOwnedManifest(manifest, command); err != nil {
		return findings.DispositionManifest{}, err
	}
	return manifest, nil
}

func validateOwnedManifest(manifest findings.DispositionManifest, command findingCommandContext) error {
	pullRequest, err := strconv.ParseInt(command.Facts.PR, 10, 64)
	if err != nil {
		return err
	}
	want := findings.ManifestContext{Forge: command.Facts.Forge, Owner: command.Facts.Owner, Repo: command.Facts.Repo, PullRequest: pullRequest, HeadSHA: command.Lease.ObservedHead, TargetSHA: command.Lease.ObservedTarget, AttemptToken: command.Token, GoverningIdentity: governingIdentity(command.Repo)}
	if !reflect.DeepEqual(manifest.Context, want) || manifest.Policy != command.Repo.FindingPolicy() {
		return fmt.Errorf("disposition manifest does not match the currently owned repository, revisions, policy, or governing identity")
	}
	return nil
}

func manifestContext(command findingCommandContext) (findings.ManifestContext, error) {
	pullRequest, err := strconv.ParseInt(command.Facts.PR, 10, 64)
	if err != nil || pullRequest <= 0 {
		return findings.ManifestContext{}, fmt.Errorf("invalid pull request %q", command.Facts.PR)
	}
	return findings.ManifestContext{Forge: command.Facts.Forge, Owner: command.Facts.Owner, Repo: command.Facts.Repo, PullRequest: pullRequest, HeadSHA: command.Lease.ObservedHead, TargetSHA: command.Lease.ObservedTarget, AttemptToken: command.Token, GoverningIdentity: governingIdentity(command.Repo)}, nil
}

func verifyWorkspaceRevisions(command findingCommandContext) error {
	head, err := gitOutput(command.Workspace, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != command.Lease.ObservedHead {
		return fmt.Errorf("workspace HEAD %s does not match owned head %s", head, command.Lease.ObservedHead)
	}
	_, err = gitOutput(command.Workspace, "rev-parse", "--verify", command.Lease.ObservedTarget+"^{commit}")
	return err
}

func gitOutput(workspace string, args ...string) (string, error) {
	data, err := gitBytes(workspace, args...)
	return strings.TrimSpace(string(data)), err
}

func gitBytes(workspace string, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", workspace}, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C"}
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return data, nil
}

func decodeStrictJSONFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON in %s", path)
	}
	return nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func recordExcludedProposals(command findingCommandContext, candidates []panelCandidate, reason string) error {
	runDir, err := filepath.Abs(os.Getenv("MINOS_RUN_DIR"))
	if err != nil || strings.TrimSpace(os.Getenv("MINOS_RUN_DIR")) == "" {
		return fmt.Errorf("MINOS_RUN_DIR is required to retain excluded finding proposals")
	}
	data, err := json.MarshalIndent(struct {
		SchemaVersion int              `json:"schema_version"`
		Reason        string           `json:"reason"`
		Candidates    []panelCandidate `json:"candidates"`
	}{1, reason, candidates}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "finding-proposals-excluded.json"), append(data, '\n'), 0o600)
}

func optionalArgument(args []string, index int) string {
	if len(args) > index {
		return args[index]
	}
	return ""
}
