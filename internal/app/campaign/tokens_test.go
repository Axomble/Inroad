package campaign_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/worker/personalize"
)

func TestUnknownTokensAcceptsBuiltinsAndDefinedCustomKeys(t *testing.T) {
	body := "Hi {{first_name}} {{last_name}} at {{company}} ({{email}}) — {{custom.industry}} is booming."
	if got := campaign.UnknownTokens([]string{"industry"}, body); len(got) != 0 {
		t.Fatalf("UnknownTokens = %v, want none", got)
	}
}

func TestUnknownTokensFlagsMisspelledBuiltin(t *testing.T) {
	got := campaign.UnknownTokens(nil, "Hi {{firstname}},")
	if !slices.Equal(got, []string{"{{firstname}}"}) {
		t.Fatalf("UnknownTokens = %v, want [{{firstname}}]", got)
	}
}

// A spaced token is the subtle case: it LOOKS like a valid built-in, but
// personalize does a literal string replace, so "{{ first_name }}" survives
// into the sent email exactly as typed.
func TestUnknownTokensFlagsSpacedTokenBecauseSubstitutionIsLiteral(t *testing.T) {
	const spaced = "{{ first_name }}"
	if got := personalize.Text("Hi "+spaced, personalize.Vars{FirstName: "Ada"}); !strings.Contains(got, spaced) {
		t.Fatalf("personalize substituted %q (%q) — this test's premise is stale", spaced, got)
	}
	if got := campaign.UnknownTokens(nil, "Hi "+spaced); !slices.Equal(got, []string{spaced}) {
		t.Fatalf("UnknownTokens = %v, want [%s]", got, spaced)
	}
}

// An undefined custom key is flagged for the opposite reason: personalize DOES
// match it and replaces it with the empty string, so the email silently ships
// with a hole in it.
func TestUnknownTokensFlagsUndefinedCustomKey(t *testing.T) {
	const tok = "{{custom.industry}}"
	if got := personalize.Text("in "+tok, personalize.Vars{}); strings.Contains(got, tok) {
		t.Fatalf("personalize left %q in place (%q) — this test's premise is stale", tok, got)
	}
	if got := campaign.UnknownTokens([]string{"tier"}, "in "+tok); !slices.Equal(got, []string{tok}) {
		t.Fatalf("UnknownTokens = %v, want [%s]", got, tok)
	}
}

// Custom keys are matched case-sensitively because personalize's lookup is a
// plain map read, and definitions are constrained to lower case — so a
// capitalised token is a typo, not a second spelling.
func TestUnknownTokensIsCaseSensitiveOnCustomKeys(t *testing.T) {
	got := campaign.UnknownTokens([]string{"industry"}, "{{custom.Industry}}")
	if !slices.Equal(got, []string{"{{custom.Industry}}"}) {
		t.Fatalf("UnknownTokens = %v, want the capitalised token flagged", got)
	}
}

func TestUnknownTokensDeduplicatesAcrossTemplatesAndSorts(t *testing.T) {
	got := campaign.UnknownTokens(nil, "{{zeta}} {{alpha}}", "{{zeta}}")
	if !slices.Equal(got, []string{"{{alpha}}", "{{zeta}}"}) {
		t.Fatalf("UnknownTokens = %v, want sorted and deduplicated", got)
	}
}

// The built-in list is duplicated between this package (which validates) and
// personalize (which substitutes). This is the test that keeps the copy honest:
// if personalize ever stops substituting one of them, or the spelling drifts,
// preflight would start passing a token that arrives unrendered.
func TestBuiltinTokensAllActuallySubstitute(t *testing.T) {
	vars := personalize.Vars{FirstName: "Ada", LastName: "Lovelace", Email: "ada@x.test", Company: "Analytical"}
	for _, name := range []string{"first_name", "last_name", "email", "company"} {
		token := "{{" + name + "}}"
		if got := personalize.Text(token, vars); got == token {
			t.Errorf("personalize left %s unsubstituted — campaign.builtinTokens has drifted", token)
		}
		if unknown := campaign.UnknownTokens(nil, token); len(unknown) != 0 {
			t.Errorf("UnknownTokens flagged %s, which personalize does substitute", token)
		}
	}
}

func TestComputePreflightUnknownTokenFails(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{Subject: "quick q", BodyText: "Hi {{firstname}}"}}
	report := campaign.ComputePreflight(in)
	if report.Ready {
		t.Error("ready = true, want false: an unknown token must block launch")
	}
	c := findCheck(t, report, campaign.CheckTokens)
	if c.Severity != campaign.SeverityFail {
		t.Fatalf("personalization_tokens severity = %q, want fail", c.Severity)
	}
	if !strings.Contains(c.Detail, "{{firstname}}") {
		t.Errorf("detail = %q, want it to name the offending token", c.Detail)
	}
}

// Subjects are scanned too — a token in the subject line is the most visible
// place for one to go wrong.
func TestComputePreflightScansSubjectsForTokens(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{Subject: "{{custom.trigger}} at {{company}}", BodyText: "hi"}}
	report := campaign.ComputePreflight(in)
	if c := findCheck(t, report, campaign.CheckTokens); c.Severity != campaign.SeverityFail {
		t.Errorf("severity = %q, want fail for an undefined custom key in the subject", c.Severity)
	}

	in.CustomFieldKeys = []string{"trigger"}
	if c := findCheck(t, campaign.ComputePreflight(in), campaign.CheckTokens); c.Severity != campaign.SeverityPass {
		t.Errorf("severity = %q, want pass once the field is defined", c.Severity)
	}
}

// --- variants ------------------------------------------------------------
//
// A variant is a real email that real prospects receive. Every check that reads
// copy has to read variants too, or a step that looks fine ships broken
// alternatives to whatever share of the audience the split sends them.

func TestComputePreflightScansVariantBodiesForTokens(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{
		Subject: "hi", BodyText: "Hello {{first_name}}", BaseWeight: 1,
		Variants: []campaign.PreflightVariant{
			{Label: "B", Weight: 1, BodyText: "Hello {{firstname}}"},
		},
	}}
	c := findCheck(t, campaign.ComputePreflight(in), campaign.CheckTokens)
	if c.Severity != campaign.SeverityFail {
		t.Fatalf("severity = %q, want fail for a bad token in a variant", c.Severity)
	}
	if !strings.Contains(c.Detail, "{{firstname}}") {
		t.Errorf("detail = %q, want it to name the variant's token", c.Detail)
	}
}

func TestComputePreflightWarnsAboutAnEmptyVariantBody(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{
		BodyText: "hi", BaseWeight: 1,
		Variants: []campaign.PreflightVariant{{Label: "B", Weight: 1}},
	}}
	if c := findCheck(t, campaign.ComputePreflight(in), campaign.CheckEmptyBodies); c.Severity != campaign.SeverityWarn {
		t.Errorf("severity = %q, want warn for an empty variant body", c.Severity)
	}
}

// A retired arm cannot be selected, so warning about its body would be noise
// about an email nobody receives.
func TestComputePreflightIgnoresTheBodyOfAZeroWeightVariant(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{
		BodyText: "hi", BaseWeight: 1,
		Variants: []campaign.PreflightVariant{{Label: "B", Weight: 0}},
	}}
	if c := findCheck(t, campaign.ComputePreflight(in), campaign.CheckEmptyBodies); c.Severity != campaign.SeverityPass {
		t.Errorf("severity = %q, want pass — a retired arm's body is irrelevant", c.Severity)
	}
}

func TestComputePreflightFailsAStepWithEveryWeightAtZero(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{
		BodyText: "hi", BaseWeight: 0,
		Variants: []campaign.PreflightVariant{{Label: "B", Weight: 0, BodyText: "hey"}},
	}}
	report := campaign.ComputePreflight(in)
	if report.Ready {
		t.Error("ready = true, want false: this step cannot send anything")
	}
	if c := findCheck(t, report, campaign.CheckVariantWeights); c.Severity != campaign.SeverityFail {
		t.Errorf("severity = %q, want fail", c.Severity)
	}
}

// A step with no variants at all must never be judged by its base weight: the
// send path returns the base copy immediately when there are no variant rows,
// so a stray 0 there is not "retired".
func TestComputePreflightTreatsAVariantlessStepAsSendable(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{BodyText: "hi", BaseWeight: 0}}
	report := campaign.ComputePreflight(in)
	if c := findCheck(t, report, campaign.CheckVariantWeights); c.Severity != campaign.SeverityPass {
		t.Errorf("variant_weights severity = %q, want pass", c.Severity)
	}
	if c := findCheck(t, report, campaign.CheckEmptyBodies); c.Severity != campaign.SeverityPass {
		t.Errorf("empty_bodies severity = %q, want pass", c.Severity)
	}
}
