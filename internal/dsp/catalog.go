package dsp

import (
	"encoding/json"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// DSP node type names and the derived-value rules for catalog documents.
//
// Every node this project emits carries @type, including where the JSON Schema
// does not require it. The DSP context defines most terms inside type-scoped
// contexts — participantId and dataset only inside Catalog, hasPolicy and
// distribution only inside Dataset, format and accessService only inside
// Distribution, endpointURL only inside DataService. A node without @type
// therefore loses those keys silently during expansion: the document still
// parses, and the information is simply gone.
const (
	CatalogType      = "Catalog"
	DatasetType      = "Dataset"
	OfferType        = "Offer"
	DistributionType = "Distribution"
	DataServiceType  = "DataService"

	// catalogPath is the catalog's own identifier, relative to the public URL.
	catalogPath = VersionPath + "/catalog"

	// offerIDSuffix derives an offer identifier from its dataset's. Deriving it
	// keeps the identifier stable across restarts without storage; when
	// negotiation has to pin an offer to an agreement, that is the point at
	// which storage decides the identifier instead.
	offerIDSuffix = "#offer"

	// unspecifiedFormat is a placeholder. DSP does not define the distribution
	// format vocabulary, and advertising a real transfer format such as
	// HttpData-PULL would claim a transfer capability this connector does not
	// have. The value changes when the transfer milestone makes a real one true.
	unspecifiedFormat = "dsbox:unspecified"

	// useAction expands to http://www.w3.org/ns/odrl/2/use, the exact value the
	// TCK's own reference dataset uses.
	useAction = "use"
)

// Catalog is a catalog document.
type Catalog struct {
	Context       []string `json:"@context"`
	ID            string   `json:"@id"`
	Type          string   `json:"@type"`
	ParticipantID string   `json:"participantId"`
	// Dataset is omitted entirely when empty: the schema allows the key to be
	// absent but requires at least one entry when it is present.
	Dataset []Dataset `json:"dataset,omitempty"`
}

// Dataset is one advertised dataset. Context is set only when the dataset is
// served as its own document; nested inside a catalog it stays empty, because a
// context belongs to a document rather than to a node.
type Dataset struct {
	Context      []string       `json:"@context,omitempty"`
	ID           string         `json:"@id"`
	Type         string         `json:"@type"`
	HasPolicy    []Offer        `json:"hasPolicy"`
	Distribution []Distribution `json:"distribution"`
}

// Offer is the policy advertised with a dataset. target is deliberately absent:
// the schema forbids it on an offer inside a dataset.
type Offer struct {
	ID         string       `json:"@id"`
	Type       string       `json:"@type"`
	Permission []Permission `json:"permission"`
}

// Permission is one ODRL rule. This connector advertises unrestricted use and
// nothing else — it never sets Constraint, which is why that field is
// omitempty and every permission this project emits is byte-identical to
// before it existed.
//
// Constraint exists for the inbound direction only. DECISIONS.md §14 evaluates
// exactly two policy shapes, unrestricted use and a validity period, and
// requires any other constraint to parse and then have the negotiation
// rejected. The consumer role is where a counterparty's constraint can first
// reach this connector, so that is where the rule is enforced — see
// decideOfferReaction and decideAgreementReaction.
//
// The elements are deliberately opaque. v1 evaluates no constraint at all, so
// the only question worth asking is whether one is present; json.RawMessage
// still requires each to be well-formed JSON, which is precisely §14's "parses
// successfully but causes the negotiation to be rejected" — parsed, never
// interpreted. Giving the fields names this connector cannot act on would
// suggest an evaluator that does not exist.
type Permission struct {
	Action     string            `json:"action"`
	Constraint []json.RawMessage `json:"constraint,omitempty"`
}

// Distribution describes how a dataset can be obtained.
type Distribution struct {
	Type   string `json:"@type"`
	Format string `json:"format"`
	// AccessService holds the full DataService object rather than a string
	// reference. The schema permits either, but the context does not declare
	// accessService as @type: @id, so a bare string expands to a literal rather
	// than to a link.
	AccessService DataService `json:"accessService"`
}

// DataService is the endpoint a distribution is served from.
type DataService struct {
	ID          string `json:"@id"`
	Type        string `json:"@type"`
	EndpointURL string `json:"endpointURL"`
}

// buildCatalog returns the catalog document this participant serves. The
// catalog is built from configuration on every request: it is an operator
// declaration, not runtime state, and it is small enough that caching it would
// be an optimization with nothing to show for it.
func buildCatalog(cfg config.Config) Catalog {
	cat := Catalog{
		Context:       []string{ContextURL},
		ID:            cfg.PublicURL + catalogPath,
		Type:          CatalogType,
		ParticipantID: cfg.ParticipantID,
	}
	for _, d := range cfg.Datasets {
		cat.Dataset = append(cat.Dataset, buildDataset(cfg.PublicURL, d.ID))
	}
	return cat
}

// buildDataset returns one dataset node, without a context. The offer, the
// distribution, and the data service are all derived from the identifier and
// the public URL, so the document is identical across restarts.
func buildDataset(publicURL, id string) Dataset {
	endpoint := publicURL + VersionPath
	return Dataset{
		ID:   id,
		Type: DatasetType,
		HasPolicy: []Offer{{
			ID:         id + offerIDSuffix,
			Type:       OfferType,
			Permission: []Permission{{Action: useAction}},
		}},
		Distribution: []Distribution{{
			Type:   DistributionType,
			Format: unspecifiedFormat,
			AccessService: DataService{
				ID:          endpoint,
				Type:        DataServiceType,
				EndpointURL: endpoint,
			},
		}},
	}
}

// findDataset returns the advertised dataset with the given identifier as a
// standalone document, context included. A linear scan is right here: the list
// is an operator's hand-written configuration, not a data store.
func findDataset(cfg config.Config, id string) (Dataset, bool) {
	for _, d := range cfg.Datasets {
		if d.ID == id {
			ds := buildDataset(cfg.PublicURL, d.ID)
			ds.Context = []string{ContextURL}
			return ds, true
		}
	}
	return Dataset{}, false
}
