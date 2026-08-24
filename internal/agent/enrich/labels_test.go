package enrich

import "testing"

func TestSchemaVersion(t *testing.T) {
	if SchemaVersion != 8 {
		t.Fatalf("SchemaVersion = %d, want 8", SchemaVersion)
	}
}

// The published sensitivity-span vocabulary. Every value here appears in
// sidecar/app/pii.py's _ENTITY_MAP, and vice versa — a name in one and not the
// other is either a detection that publishes an unmapped label (and so silently
// rolls up to "none") or a rollup row nothing can ever reach.
func TestSensitivityTriggersCoverTheDetectedVocabulary(t *testing.T) {
	want := map[string]string{
		// credentials — the Go-side gitleaks layer, no sidecar involved
		"api_key": "secrets", "secret": "secrets",
		// the original presidio four
		"ssn": "phi", "credit_card": "pci", "email": "pii", "phone": "pii",
		// listed with no detector: see SensitivityFromEntity's doc comment
		"person": "pii", "address": "pii",
		// universal tier
		"iban": "pci", "crypto_wallet": "pci",
		// "us" (the default region)
		"aba_routing": "pci", "us_npi": "pii", "medical_license": "phi",
		// opt-in regions
		"uk_nhs": "phi", "au_medicare": "phi",
		"es_nif": "pii", "es_nie": "pii",
		"it_fiscal_code": "pii", "it_vat_code": "pii",
		"pl_pesel": "pii", "fi_personal_identity_code": "pii",
		"kr_rrn": "pii", "kr_driver_license": "pii", "kr_brn": "pii", "kr_frn": "pii",
		"in_aadhaar": "pii", "in_gstin": "pii",
		"au_tfn": "pii", "au_abn": "pii", "au_acn": "pii",
		"ng_nin": "pii", "th_tnin": "pii", "sg_uen": "pii",
	}
	got := map[string]string{}
	for _, rule := range SensitivityFromEntity {
		for _, trig := range rule.Triggers {
			if prev, dup := got[trig]; dup {
				t.Errorf("trigger %q listed twice (%s and %s)", trig, prev, rule.Sensitivity)
			}
			got[trig] = rule.Sensitivity
		}
	}
	for label, class := range want {
		if got[label] != class {
			t.Errorf("%q rolls up to %q, want %q", label, got[label], class)
		}
	}
	for label := range got {
		if _, ok := want[label]; !ok {
			t.Errorf("%q is a trigger nothing declares; add it here or remove the rule", label)
		}
	}
}

// phi is the most severe class, so what does NOT reach it is as much of a
// decision as what does. Pinned because the tempting-but-wrong assignments were
// argued explicitly: an NPI is a PUBLIC CMS directory number for a provider, not
// patient data, and a tax or company id is not health data however sensitive.
func TestOnlyPatientAndPrescriberIdentifiersReachPHI(t *testing.T) {
	var phi []string
	for _, rule := range SensitivityFromEntity {
		if rule.Sensitivity == "phi" {
			phi = rule.Triggers
		}
	}
	allowed := map[string]bool{"ssn": true, "uk_nhs": true, "au_medicare": true, "medical_license": true}
	for _, trig := range phi {
		if !allowed[trig] {
			t.Errorf("%q reaches phi; a false phi is the worst output this facet has", trig)
		}
	}
	for _, no := range []string{"us_npi", "it_vat_code", "au_tfn", "in_gstin", "sg_uen"} {
		for _, trig := range phi {
			if trig == no {
				t.Errorf("%q must not roll up to phi", no)
			}
		}
	}
}

func TestVocabNonEmpty(t *testing.T) {
	if len(TaskTypes) == 0 || len(Domains) == 0 || len(Sensitivity) == 0 {
		t.Fatal("vocab lists must be non-empty")
	}
	if len(DomainEntityLabels) == 0 {
		t.Fatal("entity label maps must be non-empty")
	}
	if len(SensitivityFromEntity) == 0 {
		t.Fatal("SensitivityFromEntity must be non-empty")
	}
}

func TestSensitivityRuleOrderPHIBeforePII(t *testing.T) {
	// Order matters: ssn -> phi must be evaluated before email -> pii.
	phiIdx, piiIdx := -1, -1
	for i, r := range SensitivityFromEntity {
		if r.Sensitivity == "phi" {
			phiIdx = i
		}
		if r.Sensitivity == "pii" {
			piiIdx = i
		}
	}
	if phiIdx == -1 || piiIdx == -1 || phiIdx > piiIdx {
		t.Fatalf("expected phi rule before pii rule, got phi=%d pii=%d", phiIdx, piiIdx)
	}
}

func TestSubcatsCoverFunctions(t *testing.T) {
	for _, f := range Functions {
		if len(Subcats[f.ID]) == 0 {
			t.Errorf("function %q has no subcategories", f.ID)
		}
	}
	if len(Functions) != 12 {
		t.Fatalf("want 12 functions, got %d", len(Functions))
	}
}
