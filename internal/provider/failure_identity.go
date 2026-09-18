package provider

import (
	"fmt"
	"strings"
)

// This file ports the self-contained identity/diagnostic types from
// DeepSeek-Reasonix internal/provider/failure_diagnostic.go. The upstream file
// also defines DiagnoseFailure/IsOpaqueBadRequest, which depend on the v2 error
// taxonomy (QuotaError fields, AsRecoveryWaitExhausted, APIError.TraceID, ...)
// that hiq has not adopted yet; those are intentionally omitted here and
// will land with that subsystem. The types below are additive and unblock the
// durable tool-recovery and Responses-adapter contracts.

// RequestIdentity keeps the stable connection key separate from the
// user-editable label and the selected wire protocol.
type RequestIdentity struct {
	Provider    string
	DisplayName string
	Protocol    string
}

// RequestFailure preserves connection identity for failures that happen before
// an HTTP status exists, while retaining the original error for classification.
type RequestFailure struct {
	Identity  RequestIdentity
	Operation string
	Err       error
}

func (e *RequestFailure) Error() string {
	return fmt.Sprintf("%s: %s: %v", ProviderDisplayLabel(e.Identity.Provider, e.Identity.DisplayName, e.Identity.Protocol), e.Operation, e.Err)
}

func (e *RequestFailure) Unwrap() error { return e.Err }

// ProtocolDisplayName maps a wire protocol id to its human label.
func ProtocolDisplayName(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "openai":
		return "Chat Completions"
	case "anthropic":
		return "Anthropic Messages"
	case "responses":
		return "Responses"
	case "dashscope-responses":
		return "DashScope Responses"
	default:
		return strings.TrimSpace(kind)
	}
}

// ProviderDisplayLabel renders "<connection> · <protocol>" for failure notices.
func ProviderDisplayLabel(providerID, displayName, protocol string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = strings.TrimSpace(providerID)
	}
	protocolName := ProtocolDisplayName(protocol)
	if name == "" {
		return protocolName
	}
	if protocolName == "" {
		return name
	}
	return name + " · " + protocolName
}

// FailureDiagnostic contains safe classification only, never response bodies.
type FailureDiagnostic struct {
	Kind                string `json:"kind"`
	Status              int    `json:"status,omitempty"`
	TraceID             string `json:"traceId,omitempty"`
	ProviderID          string `json:"providerId,omitempty"`
	ProviderDisplayName string `json:"providerDisplayName,omitempty"`
	Protocol            string `json:"protocol,omitempty"`
	RequestPath         string `json:"requestPath,omitempty"`
}

// FailureDiagnosticDetail renders the safe operator fields shared by live and
// persisted failure notices. It intentionally excludes display identity and
// any request query or credentials.
func FailureDiagnosticDetail(d *FailureDiagnostic) string {
	if d == nil {
		return ""
	}
	detail := ""
	if d.ProviderID != "" {
		detail = "Connection ID: " + d.ProviderID
	}
	if d.RequestPath != "" {
		if detail != "" {
			detail += "\n"
		}
		detail += "Request path: " + d.RequestPath
	}
	return detail
}
