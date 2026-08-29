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

	// servedFormat is what a distribution advertises, and it is the value
	// POST /transfers/initiate takes. The same token on both halves is the
	// whole point: a consumer reads it out of the catalog instead of being
	// told it out of band.
	//
	// It was `dsbox:unspecified` while this connector had no data plane,
	// because advertising a transfer capability it did not have would have
	// been a claim rather than a placeholder — and that comment said the
	// value changes when a real one becomes true. data_handler.go is where
	// it became true.
	//
	// DSP does not define the vocabulary, and the TCK's own
	// `catalog/dataset-schema.json` requires a distribution's format while
	// constraining it to `{"type": "string"}` — it checks that there is one,
	// never which. So this token is this implementation's, a counterparty is
	// not obliged to know it, and the provider still accepts any non-empty
	// format on the wire for the reason transfer_handler.go states.
	servedFormat = "HTTP-PULL"

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
			Format: servedFormat,
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
// @context and @type are not decoded, and not reading them is what makes this
// type work against a real counterparty: they carry JSON-LD shape variation
// and discovery needs neither. distribution is read now, for the format POST
// /transfers/initiate requires — the deferral recorded here previously rested
// on this connector advertising a placeholder, which it no longer does — but
// read at arm's length; see remoteDataset.
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
	// Distribution is kept whole and opaque, not declared an array, and that
	// is a deliberate departure from how dataset and hasPolicy are read.
	//
	// It was an array first, and measurement refused documents that had
	// decoded before: the DSP context scopes distribution's @container: @set
	// to a node typed Dataset, so a dataset written without @type collapses a
	// lone distribution to a bare object — and @type is precisely what this
	// type declines to decode. One such dataset voided every other dataset in
	// the same catalog.
	//
	// §38.5's argument for strictness does not reach here. There, tolerating
	// a single object could manufacture an offer with a phantom @id, a value
	// an operator pastes into an initiate call. The worst a tolerated
	// distribution yields is a format string — and a missing one is already
	// survivable, which is what Format's omitempty says.
	Distribution json.RawMessage `json:"distribution"`
}

// format returns a format this dataset advertises that can be read, or the
// empty string. Absence is not an error: it is the situation every caller was
// in before this was decoded at all, and the response says so by omitting the
// field rather than by carrying a blank one.
//
// A format this connector can actually carry out wins over one it cannot.
// Reporting the first advertised value would hand an operator a token the
// transfer then fails on while a usable one sat beside it in the same
// document. What is advertised is still reported when none of it is usable —
// discovery's job is to say what is on offer, not to hide it.
func (d remoteDataset) format() string {
	advertised := ""
	for _, raw := range d.distributions() {
		f := distributionFormat(raw)
		switch {
		case f == "":
		case f == servedFormat:
			return f
		case advertised == "":
			advertised = f
		}
	}
	return advertised
}

// distributionFormat reads one distribution node's format, or reports none.
//
// The node is read as a map keyed by its exact terms rather than decoded into
// a struct, and both halves of that are load-bearing. Go's decoder matches
// field names case-insensitively, so a struct reads `Format` and `FORMAT` as
// the DSP term they are not — JSON-LD terms are case-sensitive. And on a node
// carrying `format` twice, which a producer emits when it has both a string
// and an `@id` form, a struct is populated by the first and then errors: an
// ignored error reports a value the document itself overrode. A map takes the
// last, which is what the duplicate means, and an unreadable last value is
// reported as no format rather than as the earlier one.
func distributionFormat(raw json.RawMessage) string {
	var node map[string]json.RawMessage
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	var format string
	if json.Unmarshal(node["format"], &format) != nil {
		return ""
	}
	return format
}

// distributions returns the distribution nodes to read, whether the
// counterparty wrote an array or the single object the context collapses one
// to. Anything else yields nothing to read rather than refusing the catalog.
func (d remoteDataset) distributions() []json.RawMessage {
	var many []json.RawMessage
	if err := json.Unmarshal(d.Distribution, &many); err == nil {
		return many
	}
	return []json.RawMessage{d.Distribution}
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
	// Format is what POST /transfers/initiate takes. Omitted rather than
	// blank when the counterparty advertised none this connector could read,
	// so an operator sees a value missing instead of a value that is empty.
	Format string `json:"format,omitempty"`
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
		format := d.format()
		for _, o := range d.HasPolicy {
			out = append(out, datasetOffer{DatasetID: d.ID, OfferID: o.ID, Format: format})
		}
	}
	return out, skipped
}
