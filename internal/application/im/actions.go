package im

import (
	"fmt"
	"strings"

	actioncontract "github.com/domainry/domainry-foundation/action"
)

const AuthorizationOwner = "module:im"

const (
	ActionThreadsCreate          = "im.threads.create"
	ActionThreadsList            = "im.threads.list"
	ActionThreadMembersUpdate    = "im.threads.members.update"
	ActionMessagesSend           = "im.messages.send"
	ActionMessagesList           = "im.messages.list"
	ActionMessagesSearch         = "im.messages.search"
	ActionReceiptsList           = "im.receipts.list"
	ActionReceiptsMark           = "im.receipts.mark"
	ActionAttachmentsUpload      = "im.attachments.upload"
	ActionAttachmentsRead        = "im.attachments.read"
	ActionContextHistoryList     = "im.contexts.history.list"
	ActionContextHistorySearch   = "im.contexts.history.search"
	ActionContextAttachmentsRead = "im.contexts.attachments.read"
	ActionQuickRepliesList       = "im.quick_replies.list"
	ActionQuickRepliesUpdate     = "im.quick_replies.update"
	ActionEventsStream           = "im.events.stream"
)

type httpAction struct {
	key, pattern, label string
	effect              actioncontract.EffectClass
	risk                actioncontract.RiskLevel
	idempotency         string
}

func AuthorizationActions() ([]actioncontract.ActionDefinition, error) {
	read, write := actioncontract.EffectRead, actioncontract.EffectWrite
	low, medium := actioncontract.RiskLow, actioncontract.RiskMedium
	specs := []httpAction{
		{ActionThreadsCreate, "POST /im/threads", "Create IM thread", write, medium, "natural_key"},
		{ActionThreadsList, "GET /im/threads", "List IM threads", read, low, "not_applicable"},
		{ActionThreadMembersUpdate, "PUT /im/threads/{thread_id}/members", "Replace IM group members", write, medium, "natural_key"},
		{ActionMessagesSend, "POST /im/threads/{thread_id}/messages", "Send IM message", write, medium, "caller_key_required"},
		{ActionMessagesList, "GET /im/threads/{thread_id}/messages", "List IM messages", read, low, "not_applicable"},
		{ActionMessagesSearch, "GET /im/messages/search", "Search IM messages", read, low, "not_applicable"},
		{ActionReceiptsList, "GET /im/threads/{thread_id}/receipts", "List IM read receipts", read, low, "not_applicable"},
		{ActionReceiptsMark, "PUT /im/threads/{thread_id}/read", "Mark IM thread read", write, low, "natural_key"},
		{ActionAttachmentsUpload, "POST /im/uploads", "Upload IM attachment", write, medium, "non_idempotent"},
		{ActionAttachmentsRead, "GET /im/attachments/{attachment_id}/content", "Read IM attachment", read, low, "not_applicable"},
		{ActionContextHistoryList, "GET /im/contexts/{namespace}/{resource_type}/{resource_id}/history", "Read IM context history", read, medium, "not_applicable"},
		{ActionContextHistorySearch, "GET /im/contexts/{namespace}/{resource_type}/{resource_id}/history/search", "Search IM context history", read, medium, "not_applicable"},
		{ActionContextAttachmentsRead, "GET /im/context-attachments/{attachment_id}/content", "Read IM context attachment", read, medium, "not_applicable"},
		{ActionQuickRepliesList, "GET /im/quick-replies", "List IM quick replies", read, low, "not_applicable"},
		{ActionQuickRepliesUpdate, "PUT /im/quick-replies/{key}", "Update IM quick reply", write, medium, "natural_key"},
		{ActionEventsStream, "GET /im/events", "Open IM synchronization stream", read, low, "not_applicable"},
	}
	definitions := make([]actioncontract.ActionDefinition, 0, len(specs))
	for _, spec := range specs {
		method, route, found := strings.Cut(spec.pattern, " ")
		separator := strings.LastIndex(spec.key, ".")
		if !found || separator <= 0 || separator == len(spec.key)-1 {
			return nil, fmt.Errorf("IM Action %q is invalid", spec.key)
		}
		definition := actioncontract.ActionDefinition{
			Key: spec.key, Owner: AuthorizationOwner, SourceKind: "module_http",
			CapabilityKey: "im", CapabilityLabel: "Instant messaging",
			OperationKey: spec.key[separator+1:], OperationLabel: spec.label, Label: spec.label,
			Exposures:   []actioncontract.Exposure{actioncontract.ExposurePublic},
			HTTP:        &actioncontract.HTTPBinding{Method: method, RouteTemplate: route},
			EffectClass: spec.effect, RiskLevel: spec.risk, IdempotencyDecision: spec.idempotency,
			AuditClass: "im_http", LifecycleStatus: actioncontract.LifecycleActive,
			Authorization: actioncontract.Authorization{Strategy: actioncontract.AuthorizationAuthenticated},
			Permission:    &actioncontract.PermissionDefinition{Key: spec.key, Owner: AuthorizationOwner, ResourceKey: spec.key[:separator], OperationKey: spec.key[separator+1:], Label: spec.label, Category: "Instant messaging", LifecycleStatus: actioncontract.LifecycleActive},
		}
		normalized, err := actioncontract.NormalizeDefinition(definition)
		if err != nil {
			return nil, fmt.Errorf("IM Action %q: %w", spec.key, err)
		}
		definitions = append(definitions, normalized)
	}
	return definitions, nil
}
