package dsp

import (
	"encoding/json"
	"testing"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

func testConfig(ids ...string) config.Config {
	cfg := config.Config{
		PublicURL:     "https://connector.example.org",
		ParticipantID: "urn:participant:example",
	}
	for _, id := range ids {
		cfg.Datasets = append(cfg.Datasets, config.Dataset{ID: id})
	}
	return cfg
}

// decode marshals a document and decodes it back into a generic map, so the
// assertions run against the JSON that actually goes on the wire rather than
// against the Go struct. Keys dropped by an omitempty tag are then genuinely
// absent.
func decode(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestCatalogWithNoDatasetsOmitsTheDatasetKey(t *testing.T) {
	// The schema allows the key to be absent but requires at least one entry
	// when it is present, so an empty array would be invalid.
	m := decode(t, buildCatalog(testConfig()))
	if _, present := m["dataset"]; present {
		t.Error("dataset key is present with no datasets configured; it must be omitted entirely")
	}
}

func TestCatalogRootCarriesTheRequiredFields(t *testing.T) {
	m := decode(t, buildCatalog(testConfig("urn:dataset:a")))

	ctx, ok := m["@context"].([]any)
	if !ok || len(ctx) != 1 || ctx[0] != ContextURL {
		t.Errorf("@context = %v, want the array [%s]", m["@context"], ContextURL)
	}
	if got, want := m["@id"], "https://connector.example.org/2025-1/catalog"; got != want {
		t.Errorf("@id = %v, want %q", got, want)
	}
	if got, want := m["@type"], "Catalog"; got != want {
		t.Errorf("@type = %v, want %q", got, want)
	}
	if got, want := m["participantId"], "urn:participant:example"; got != want {
		t.Errorf("participantId = %v, want %q", got, want)
	}
}

func TestCatalogListsEveryConfiguredDataset(t *testing.T) {
	m := decode(t, buildCatalog(testConfig("urn:dataset:a", "urn:dataset:b")))
	list, ok := m["dataset"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("dataset = %v, want two entries", m["dataset"])
	}
	for i, want := range []string{"urn:dataset:a", "urn:dataset:b"} {
		node := list[i].(map[string]any)
		if node["@id"] != want {
			t.Errorf("dataset[%d].@id = %v, want %q", i, node["@id"], want)
		}
	}
}

func TestDatasetNodeCarriesTypeOnEveryNode(t *testing.T) {
	// Type-scoped contexts are why this matters: hasPolicy and distribution
	// exist only inside Dataset, format and accessService only inside
	// Distribution, endpointURL only inside DataService. A missing @type drops
	// those keys silently during expansion.
	m := decode(t, buildDataset("https://connector.example.org", "urn:dataset:a"))

	if m["@type"] != "Dataset" {
		t.Errorf("dataset @type = %v, want Dataset", m["@type"])
	}
	offer := m["hasPolicy"].([]any)[0].(map[string]any)
	if offer["@type"] != "Offer" {
		t.Errorf("offer @type = %v, want Offer", offer["@type"])
	}
	dist := m["distribution"].([]any)[0].(map[string]any)
	if dist["@type"] != "Distribution" {
		t.Errorf("distribution @type = %v, want Distribution", dist["@type"])
	}
	svc := dist["accessService"].(map[string]any)
	if svc["@type"] != "DataService" {
		t.Errorf("data service @type = %v, want DataService", svc["@type"])
	}
}

func TestDatasetDerivedIdentifiers(t *testing.T) {
	m := decode(t, buildDataset("https://connector.example.org", "urn:dataset:a"))

	offer := m["hasPolicy"].([]any)[0].(map[string]any)
	if got, want := offer["@id"], "urn:dataset:a#offer"; got != want {
		t.Errorf("offer @id = %v, want %q", got, want)
	}
	if got, want := offer["permission"].([]any)[0].(map[string]any)["action"], "use"; got != want {
		t.Errorf("permission action = %v, want %q", got, want)
	}
	if _, present := offer["target"]; present {
		t.Error("offer carries target; the schema forbids it")
	}

	dist := m["distribution"].([]any)[0].(map[string]any)
	if got, want := dist["format"], unspecifiedFormat; got != want {
		t.Errorf("format = %v, want %q", got, want)
	}
	svc := dist["accessService"].(map[string]any)
	const endpoint = "https://connector.example.org/2025-1"
	if svc["@id"] != endpoint || svc["endpointURL"] != endpoint {
		t.Errorf("data service = %v, want @id and endpointURL both %q", svc, endpoint)
	}
}

func TestDatasetInsideACatalogHasNoContext(t *testing.T) {
	// A context belongs to a document, not to a node nested in one.
	m := decode(t, buildCatalog(testConfig("urn:dataset:a")))
	node := m["dataset"].([]any)[0].(map[string]any)
	if _, present := node["@context"]; present {
		t.Error("a dataset nested in a catalog carries its own @context")
	}
}

func TestFindDatasetReturnsASelfContainedDocument(t *testing.T) {
	ds, ok := findDataset(testConfig("urn:dataset:a", "urn:dataset:b"), "urn:dataset:b")
	if !ok {
		t.Fatal("findDataset: configured dataset not found")
	}
	m := decode(t, ds)
	if m["@id"] != "urn:dataset:b" {
		t.Errorf("@id = %v, want urn:dataset:b", m["@id"])
	}
	ctx, ok := m["@context"].([]any)
	if !ok || len(ctx) != 1 || ctx[0] != ContextURL {
		t.Errorf("@context = %v, want the array [%s]", m["@context"], ContextURL)
	}
	if _, present := m["distribution"]; !present {
		t.Error("the dataset document must carry its distribution; it is served standalone")
	}
}

func TestFindDatasetReportsAnUnknownIdentifier(t *testing.T) {
	if _, ok := findDataset(testConfig("urn:dataset:a"), "urn:dataset:missing"); ok {
		t.Error("findDataset accepted an identifier that is not configured")
	}
}
