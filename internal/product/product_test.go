package product_test

import (
	"reflect"
	"strings"
	"testing"

	"bfj/minos/internal/product"
)

func TestStatesAreTheCompleteCommissionedVocabulary(t *testing.T) {
	want := []struct {
		state       product.State
		name        string
		forgeState  string
		description string
	}{
		{product.Queued(), "queued", "pending", "Waiting for review"},
		{product.Working(), "working", "pending", "Reviewing changes"},
		{product.Waiting(), "waiting", "pending", "Waiting for checks"},
		{product.Blocked(), "blocked", "failure", "Changes need attention"},
		{product.Partial(), "partial", "failure", "Review needs attention"},
		{product.Stopped(), "stopped", "failure", "Review stopped; findings remain"},
		{product.Clean(), "clean", "success", "Changes approved"},
		{product.CleanLimited(), "clean, limited", "success", "Changes approved; verification limited"},
		{product.Merged(), "merged", "success", "Merged"},
	}

	states := product.States()
	if len(states) != len(want) {
		t.Fatalf("States() returned %d states, want %d", len(states), len(want))
	}
	for i, expected := range want {
		got := states[i]
		if got != expected.state || got.Name() != expected.name || got.ForgeState() != expected.forgeState || got.Description() != expected.description {
			t.Errorf("state %d = (%q, %q, %q), want (%q, %q, %q)", i, got.Name(), got.ForgeState(), got.Description(), expected.name, expected.forgeState, expected.description)
		}
		if got.Meaning() == "" {
			t.Errorf("state %q has no current-head meaning", got.Name())
		}
	}

	// The zero value is deliberately not a tenth state callers can publish.
	if (product.State{}).Valid() {
		t.Fatal("zero State is valid")
	}
	states[0] = product.State{}
	if product.States()[0] != product.Queued() {
		t.Fatal("caller mutated the package's state vocabulary")
	}
}

func TestMachineRecordIsHiddenCanonicalAndStrict(t *testing.T) {
	line, err := product.FormatRecord(map[string]string{
		"finding": "F-7KQ3",
		"head":    "abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if line != "<!-- Minos: finding=F-7KQ3 head=abc123 -->" {
		t.Fatalf("FormatRecord() = %q", line)
	}
	parsed, err := product.ParseRecord(line)
	if err != nil || parsed["finding"] != "F-7KQ3" || parsed["head"] != "abc123" {
		t.Fatalf("ParseRecord() = %#v, %v", parsed, err)
	}
	if _, err := product.ParseRecord("Minos: finding=F-7KQ3"); err == nil {
		t.Fatal("visible legacy marker was accepted")
	}
	if _, err := product.ParseRecord("<!-- Minos: run=review run=fix -->"); err == nil {
		t.Fatal("duplicate record key was accepted")
	}
	body := strings.Join([]string{"Substantive review text.", "", line, ""}, "\n")
	trailing, ok := product.TrailingRecord(body)
	if !ok || !reflect.DeepEqual(parsed, trailing) {
		t.Fatalf("TrailingRecord() = %#v, %t", trailing, ok)
	}
}

func TestReviewVerdictMappingIsFixed(t *testing.T) {
	tests := []struct {
		result  product.ReviewResult
		verdict product.Verdict
		name    string
	}{
		{product.ReviewConverged(), product.Approve(), "approve"},
		{product.ReviewHasMaterialFindings(), product.RequestChanges(), "request-changes"},
		{product.ReviewIncomplete(), product.Comment(), "comment"},
	}
	for _, test := range tests {
		got, err := product.VerdictFor(test.result)
		if err != nil {
			t.Fatalf("VerdictFor(%v): %v", test.result, err)
		}
		if got != test.verdict || got.Name() != test.name || !got.Valid() {
			t.Errorf("VerdictFor(%v) = %q, want %q", test.result, got.Name(), test.name)
		}
	}
	if _, err := product.VerdictFor(product.ReviewResult{}); err == nil {
		t.Fatal("VerdictFor accepted an unrecognised review result")
	}
	if (product.Verdict{}).Valid() {
		t.Fatal("zero Verdict is valid")
	}
}

func TestLeaseTransitionsKeepEyesBoundToLiveOwnership(t *testing.T) {
	lease, err := product.TransitionLease(product.Lease{}, product.LeaseAcquired())
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if !lease.Live() || !lease.Eyes() {
		t.Fatalf("acquired lease = live %t, eyes %t; want both true", lease.Live(), lease.Eyes())
	}

	for _, event := range []product.LeaseEvent{
		product.LeaseExited(),
		product.LeaseWaiting(),
		product.LeaseFailed(),
		product.LeaseReaped(),
	} {
		got, err := product.TransitionLease(lease, event)
		if err != nil {
			t.Errorf("transition %q: %v", event.Name(), err)
			continue
		}
		if got.Live() || got.Eyes() {
			t.Errorf("transition %q left live=%t eyes=%t", event.Name(), got.Live(), got.Eyes())
		}
	}

	if _, err := product.TransitionLease(lease, product.LeaseAcquired()); err == nil {
		t.Error("second acquisition of a live lease succeeded")
	}
	if _, err := product.TransitionLease(product.Lease{}, product.LeaseEvent{}); err == nil {
		t.Error("unrecognised lease event succeeded")
	}
}

func TestFindingIdentityDedupeCarriesNoAuthority(t *testing.T) {
	existing := []product.FindingID{
		product.MustParseFindingID("F-7KQ3"),
		product.MustParseFindingID("F-1234"),
	}
	candidates := []product.FindingID{
		product.MustParseFindingID("F-7KQ3"),
		{}, // A finding without lineage is new.
	}

	got, err := product.DeduplicateFindings(existing, candidates)
	if err != nil {
		t.Fatalf("DeduplicateFindings: %v", err)
	}
	if !reflect.DeepEqual(got.Recurring, []product.FindingID{existing[0]}) || got.New != 1 {
		t.Fatalf("dedupe = %#v, want one recurring and one new finding", got)
	}
	if _, err := product.DeduplicateFindings(existing, []product.FindingID{product.MustParseFindingID("F-ABCD")}); err == nil {
		t.Fatal("unknown claimed lineage was accepted")
	}
	if _, err := product.DeduplicateFindings(existing, []product.FindingID{existing[0], existing[0]}); err == nil {
		t.Fatal("duplicate candidate lineage was accepted")
	}

	type authorityCarrier interface{ AuthorisesMerge() bool }
	if _, carriesAuthority := any(existing[0]).(authorityCarrier); carriesAuthority {
		t.Fatal("finding identity carries merge authority")
	}
}

func TestFindingIdentityGrammar(t *testing.T) {
	for _, invalid := range []string{"", "F-", "F-IO01", "F-abcd", "F-ABCDE", "X-7KQ3"} {
		if _, err := product.ParseFindingID(invalid); err == nil {
			t.Errorf("ParseFindingID(%q) succeeded", invalid)
		}
	}

	existing := map[product.FindingID]struct{}{}
	for range 64 {
		id, err := product.MintFindingID(existing)
		if err != nil {
			t.Fatalf("MintFindingID: %v", err)
		}
		if !id.Valid() {
			t.Fatalf("minted invalid identity %q", id.String())
		}
		if _, duplicate := existing[id]; duplicate {
			t.Fatalf("minted duplicate identity %q", id.String())
		}
		existing[id] = struct{}{}
	}
}
