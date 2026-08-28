package dsp

import (
	"encoding/json"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/config"
)

// DSP node type names and the derived-value rules for catalog documents.
//
// The five node types named here — Catalog, Dataset, Offer, Distribution,
// DataService — each carry @type, including where the JSON Schema does not
// require it. The DSP context defines most terms inside type-scoped contexts:
// participantId and dataset only inside Catalog, hasPolicy and distribution
// only inside Dataset, format and accessService only inside Distribution,
// endpointURL only inside DataService. A node without @type therefore loses
// those keys silently during expansion: the document still parses, and the
// information is simply gone.
//
// The ODRL rule nodes are outside that rule and are why it is stated about
// these five rather than about everything this project emits: Permission
// below carries no @type, and neither does negotiation.go's
// TerminationReason. Both are shapes the TCK accepts as they stand, and
// adding a @type no schema asks for would change a passing wire shape on
// reasoning alone.
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

// Permission is one ODRL rule. DECISIONS.md §14 evaluates exactly two policy
// shapes — unrestricted use, and a validity-period constraint — and requires
// any other constraint to parse and then have the negotiation rejected.
// Constraint is omitempty because unrestricted use, the common case, still
// emits no constraint key at all: buildPermission(nil) is byte-identical to
// every permission this project emitted before this type existed.
//
// The elements stay opaque on the receiving end: a counterparty's constraint
// is decoded as json.RawMessage and inspected structurally
// (hasUnenforceableConstraint), never interpreted beyond recognizing the one
// shape this connector emits itself. json.RawMessage still requires each
// element to be well-formed JSON, which is §14's "parses successfully" half;
// the consumer role decides "and then rejected" — see decideOfferReaction and
// decideAgreementReaction.
type Permission struct {
	Action     string            `json:"action"`
	Constraint []json.RawMessage `json:"constraint,omitempty"`
}

// Constraint is the ODRL rule this connector both emits and recognizes: a
// validity-period bound, expressed with bare (unprefixed) ODRL terms — the
// same convention useAction already follows, relying on the DSP @context's
// imported ODRL vocabulary rather than an "odrl:" prefix.
type Constraint struct {
	LeftOperand  string `json:"leftOperand"`
	Operator     string `json:"operator"`
	RightOperand string `json:"rightOperand"`
}

const (
	// leftOperandDateTime and operatorLTEq name the one constraint shape §14
	// permits building: access is granted through rightOperand, inclusive,
	// and not after.
	leftOperandDateTime = "dateTime"
	operatorLTEq        = "lteq"
)

// buildPermission returns the ODRL permission this connector grants for a
// dataset. validityUntil is nil for unrestricted use, the common case;
// non-nil attaches the one constraint shape §14 permits. Every builder that
// emits a permission for one of this connector's own datasets goes through
// this function, so there is exactly one place the shape is produced.
func buildPermission(validityUntil *time.Time) []Permission {
	if validityUntil == nil {
		return []Permission{{Action: useAction}}
	}
	// Constraint's fields are all plain strings, so this cannot fail — the
	// same reasoning newMessageID's doc comment gives for ignoring its own
	// unfailable error, and the same choice: no fallback path for a failure
	// mode that does not exist.
	c, _ := json.Marshal(Constraint{
		LeftOperand:  leftOperandDateTime,
		Operator:     operatorLTEq,
		RightOperand: validityUntil.UTC().Format(time.RFC3339),
	})
	return []Permission{{Action: useAction, Constraint: []json.RawMessage{c}}}
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
		cat.Dataset = append(cat.Dataset, buildDataset(cfg.PublicURL, d))
	}
	return cat
}

// buildDataset returns one dataset node, without a context. The offer, the
// distribution, and the data service are all derived from the dataset's
// configuration and the public URL, so the document is identical across
// restarts. d.ValidityUntil becomes the offer's permission constraint — see
// buildPermission.
func buildDataset(publicURL string, d config.Dataset) Dataset {
	endpoint := publicURL + VersionPath
	return Dataset{
		ID:   d.ID,
		Type: DatasetType,
		HasPolicy: []Offer{{
			ID:         d.ID + offerIDSuffix,
			Type:       OfferType,
			Permission: buildPermission(d.ValidityUntil),
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
			ds := buildDataset(cfg.PublicURL, d)
			ds.Context = []string{ContextURL}
			return ds, true
		}
	}
	return Dataset{}, false
}

// remoteCatalog is a counterparty's catalog document as this connector reads
// it. Deliberately not Catalog above: the emitting side owes a complete
// document and the reading side owes a handful of identifiers and a refusal,
// which is DECISIONS.md section 24.7's rule for splitting a type by direction.
// OfferRef is the existing precedent for a lean decode-only sibling.
//
// @context, @type and distribution are not decoded, and not reading them is
// what makes this type work against a real counterparty: they carry the
// JSON-LD shape variation, and discovery needs none of them. distribution
// carries format, which POST /transfers/initiate requires -- that is a
// deferral rather than a design argument, and the design's section 2.2 records
// why it is safe: this connector advertises a placeholder format that is not
// the value the transfer hook takes.
//
// Strict, like every inbound decode in this package. DECISIONS.md section 20
// accepts that arbitrary JSON-LD input is not handled, and the TCK's own
// schemas declare dataset and hasPolicy to be arrays.
type remoteCatalog struct {
	ParticipantID string          `json:"participantId"`
	Dataset       []remoteDataset `json:"dataset"`
	// Catalog is decoded but not walked: a catalog of sub-catalogs is how a
	// federated broker advertises, and reporting one as empty would be a lie.
	// Kept opaque so its presence can be logged without this connector
	// claiming to understand it.
	Catalog []json.RawMessage `json:"catalog"`
}

type remoteDataset struct {
	ID        string        `json:"@id"`
	HasPolicy []remoteOffer `json:"hasPolicy"`
}

type remoteOffer struct {
	ID string `json:"@id"`
}

// datasetOffer is one negotiable pair. An initiate call takes exactly one, so
// a dataset advertising several offers produces several of these rather than
// one row with a list.
type datasetOffer struct {
	DatasetID string `json:"id"`
	OfferID   string `json:"offerId"`
}

// catalogLookupResponse is what the management route answers with. It carries
// enough to build an initiate call, and connectorAddress is a report of the
// address this connector resolved and dialed rather than an echo of anything
// the caller sent.
type catalogLookupResponse struct {
	ParticipantID    string         `json:"participantId"`
	ConnectorAddress string         `json:"connectorAddress"`
	Datasets         []datasetOffer `json:"datasets"`
}

// pairs flattens the catalog into the pairs an initiate call can name, and
// reports how many datasets were dropped for advertising no offer. The count
// is returned rather than logged here so the decision to log stays with the
// handler, which is where the participant this is about is known.
func (c remoteCatalog) pairs() ([]datasetOffer, int) {
	out := make([]datasetOffer, 0, len(c.Dataset))
	skipped := 0
	for _, d := range c.Dataset {
		if len(d.HasPolicy) == 0 {
			skipped++
			continue
		}
		for _, o := range d.HasPolicy {
			out = append(out, datasetOffer{DatasetID: d.ID, OfferID: o.ID})
		}
	}
	return out, skipped
}
