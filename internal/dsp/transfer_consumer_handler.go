package dsp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/kimjoin2/dataspace-in-a-box/internal/store"
)

// transferInitiateBody is what the TCK POSTs to
// dataspacetck.dsp.connector.transfer.initiate.url. Plain JSON, not JSON-LD:
// no @context and no @type. Confirmed from the pinned image —
// HttpConsumerTransferProcessClient.initiateTransferRequest builds a
// four-entry Map.of and calls postJson(url, body, false, true), whose false
// is the JSON-LD flag. The negotiation hook has the same shape and the same
// flags.
type transferInitiateBody struct {
	ProviderID       string `json:"providerId"`
	AgreementID      string `json:"agreementId"`
	Format           string `json:"format"`
	ConnectorAddress string `json:"connectorAddress"`
}

// handleTransferInitiate serves POST /transfers/initiate: the hook that asks
// this connector to start a transfer as consumer. It responds 200 as soon as
// the transfer is recorded and dispatches the outbound request in a
// goroutine, because the caller proceeds by waiting for that request to
// arrive on its own endpoint rather than by reading this response — and
// blocking the response on a network call would make the hook's latency
// depend on the counterparty.
//
// Unauthenticated, on the public listener, exactly like
// /negotiations/initiate and for the reason that endpoint's doc comment
// gives: this is the TCK-shaped hook, not a management feature. Until a real
// management trigger exists, it is also this connector's only way to start a
// transfer as consumer.
func (h transferHandler) handleTransferInitiate(w http.ResponseWriter, r *http.Request) {
	var body transferInitiateBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNegotiationRequestBodyBytes))
	if err := dec.Decode(&body); err != nil {
		writeError(w, TransferErrorType, http.StatusBadRequest, "the request body is not a JSON object")
		return
	}
	if body.ProviderID == "" || body.AgreementID == "" || body.Format == "" || body.ConnectorAddress == "" {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"providerId, agreementId, format, and connectorAddress are all required")
		return
	}
	// The rejection reason is logged, not echoed — validateOutgoingCallback
	// reports which address a hostname resolved to, and this endpoint is open
	// to anonymous callers, so returning that text would make it a
	// name-resolution oracle for the network this connector sits on.
	if err := validateOutgoingCallback(body.ConnectorAddress); err != nil {
		slog.Warn("reject transfer initiate", "connector_address", body.ConnectorAddress, "error", err)
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"connectorAddress is not an address this connector will send to")
		return
	}
	// One rule for both roles. The provider role already refuses a transfer
	// citing an agreement it has no record of; starting one as consumer under
	// a contract this connector never held would be the same defect from the
	// other side, and CLAUDE.md's "never accept a constraint that is not
	// enforced" rules it out either way.
	if _, ok, err := h.store.GetAgreement(body.AgreementID); err != nil {
		slog.Error("get agreement", "agreement_id", body.AgreementID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if !ok {
		writeError(w, TransferErrorType, http.StatusBadRequest,
			"no agreement with id "+body.AgreementID)
		return
	}

	consumerPID, err := store.NewUUID()
	if err != nil {
		slog.Error("generate consumer pid", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	t := store.ConsumerTransfer{
		ConsumerPID:     consumerPID,
		ProviderBaseURL: body.ConnectorAddress,
		AgreementID:     body.AgreementID,
		Format:          body.Format,
		State:           TransferRequested,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := h.store.CreateConsumerTransfer(t); err != nil {
		slog.Error("create consumer transfer", "consumer_pid", consumerPID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)

	go h.requestTransfer(t)
}

// requestTransfer sends the opening TransferRequestMessage and records what
// the acknowledgment reveals. There is nothing to record and nothing this
// connector can legally send without a provider pid, so a failure here ends
// the transfer where it stands rather than retrying.
func (h transferHandler) requestTransfer(t store.ConsumerTransfer) {
	callbackAddress := h.cfg.PublicURL + VersionPath
	providerPID, err := sendTransferRequest(t.ProviderBaseURL, buildTransferRequestMessage(t, callbackAddress))
	if err != nil {
		slog.Error("send transfer request", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	h.onTransferRequestAcknowledged(t, providerPID)
}

// onTransferRequestAcknowledged records the provider pid the ACK supplied.
// It is the only place that pid enters the row, and every message this
// connector sends as consumer is addressed to a URL containing it — which is
// why a policy triggered by REQUESTED fires from here rather than when the
// row was written.
func (h transferHandler) onTransferRequestAcknowledged(t store.ConsumerTransfer, providerPID string) {
	if err := h.store.SetConsumerTransferProviderPID(t.ConsumerPID, providerPID, time.Now()); err != nil {
		slog.Error("record provider pid", "consumer_pid", t.ConsumerPID, "error", err)
		return
	}
	t.ProviderPID = providerPID
}
