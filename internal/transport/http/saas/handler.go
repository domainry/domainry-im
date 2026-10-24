package saas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/domainry/domainry-foundation/apperror"
	"github.com/domainry/domainry-foundation/httpresponse"
	"github.com/domainry/domainry-foundation/modulehttp"
	"github.com/domainry/domainry-foundation/telemetry"
	imapplication "github.com/domainry/domainry-im/internal/application/im"
	"github.com/domainry/domainry-im/internal/domain/conversation/model"
)

const maxRequestBytes = 1024 * 1024

type Application interface {
	CreateThread(context.Context, model.Actor, model.CreateThreadRequest) (model.Thread, error)
	ReplaceThreadMembers(context.Context, model.Actor, string, model.ReplaceThreadMembersRequest) (model.Thread, error)
	ReplaceContextThreadMembers(context.Context, model.Actor, model.ContextRef, model.ReplaceThreadMembersRequest) (model.Thread, error)
	ListThreads(context.Context, model.Actor, int) ([]model.Thread, error)
	SendMessage(context.Context, model.Actor, string, model.CreateMessageRequest) (model.Message, error)
	SendContextMessage(context.Context, model.Actor, model.ContextRef, model.CreateMessageRequest) (model.Message, error)
	ListMessages(context.Context, model.Actor, string, int64, int) (model.MessagePage, error)
	SearchMessages(context.Context, model.Actor, model.SearchMessagesRequest) ([]model.Message, error)
	ListReadCursors(context.Context, model.Actor, string) ([]model.ReadCursor, error)
	MarkRead(context.Context, model.Actor, string, model.ReadRequest) (model.ReadCursor, error)
	UploadAttachment(context.Context, model.Actor, string, string, io.Reader) (model.CreateAttachmentRequest, error)
	OpenAttachment(context.Context, model.Actor, string) (model.Attachment, io.ReadCloser, error)
	ContextHistory(context.Context, model.Actor, model.ContextRef, int64, int) (model.ContextHistory, error)
	SearchContextHistory(context.Context, model.Actor, model.ContextRef, model.SearchMessagesRequest) ([]model.Message, error)
	OpenContextAttachment(context.Context, model.Actor, model.ContextRef, string) (model.Attachment, io.ReadCloser, error)
	ListQuickReplies(context.Context, model.Actor, string) ([]model.QuickReply, error)
	PutQuickReply(context.Context, model.Actor, string, model.PutQuickReplyRequest) (model.QuickReply, error)
	SyncState(context.Context, model.Actor) (model.SyncState, error)
}

type ActorResolver interface {
	Resolve(context.Context, string) (model.Actor, error)
}

type Handler struct {
	application Application
	actors      ActorResolver
	mux         *http.ServeMux
	routes      []modulehttp.Route
	poll        time.Duration
	heartbeat   time.Duration
}

func New(application Application, actors ActorResolver, poll, heartbeat time.Duration) (*Handler, error) {
	if application == nil || actors == nil || poll < 250*time.Millisecond || poll > time.Minute || heartbeat < 5*time.Second || heartbeat > time.Minute {
		return nil, errors.New("valid IM application, identity resolver, and SSE timing are required")
	}
	definitions, err := imapplication.AuthorizationActions()
	if err != nil {
		return nil, err
	}
	routes := make([]modulehttp.Route, 0, len(definitions))
	for _, definition := range definitions {
		route, err := modulehttp.RouteFromAction(definition)
		if err != nil {
			return nil, fmt.Errorf("project IM Action %q: %w", definition.Key, err)
		}
		routes = append(routes, route)
	}
	h := &Handler{application: application, actors: actors, mux: http.NewServeMux(), routes: routes, poll: poll, heartbeat: heartbeat}
	handlers := h.actionHandlers()
	for _, route := range routes {
		key := strings.TrimSpace(route.Action.Key)
		handler, found := handlers[key]
		if !found {
			return nil, fmt.Errorf("IM Action %q has no HTTP handler", key)
		}
		exactKey := key
		h.mux.HandleFunc(route.Pattern(), func(w http.ResponseWriter, r *http.Request) {
			actor, err := h.actors.Resolve(r.Context(), exactKey)
			if err != nil {
				writeError(w, r, err)
				return
			}
			handler(w, r, actor)
		})
		delete(handlers, key)
	}
	if len(handlers) != 0 {
		keys := make([]string, 0, len(handlers))
		for key := range handlers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("IM handlers have no Action entries: %v", keys)
	}
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }
func (h *Handler) Routes() []modulehttp.Route                       { return append([]modulehttp.Route(nil), h.routes...) }

type actorHandler func(http.ResponseWriter, *http.Request, model.Actor)

func (h *Handler) actionHandlers() map[string]actorHandler {
	return map[string]actorHandler{
		imapplication.ActionThreadsCreate:          h.createThread,
		imapplication.ActionThreadsList:            h.listThreads,
		imapplication.ActionThreadMembersUpdate:    h.replaceThreadMembers,
		imapplication.ActionMessagesSend:           h.sendMessage,
		imapplication.ActionMessagesList:           h.listMessages,
		imapplication.ActionMessagesSearch:         h.searchMessages,
		imapplication.ActionReceiptsList:           h.listReceipts,
		imapplication.ActionReceiptsMark:           h.markRead,
		imapplication.ActionAttachmentsUpload:      h.upload,
		imapplication.ActionAttachmentsRead:        h.attachment,
		imapplication.ActionContextHistoryList:     h.contextHistory,
		imapplication.ActionContextHistorySearch:   h.searchContextHistory,
		imapplication.ActionContextMessagesSend:    h.sendContextMessage,
		imapplication.ActionContextMembersUpdate:   h.replaceContextThreadMembers,
		imapplication.ActionContextAttachmentsRead: h.contextAttachment,
		imapplication.ActionQuickRepliesList:       h.listQuickReplies,
		imapplication.ActionQuickRepliesUpdate:     h.putQuickReply,
		imapplication.ActionEventsStream:           h.stream,
	}
}

func (h *Handler) replaceThreadMembers(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	var input model.ReplaceThreadMembersRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	value, err := h.application.ReplaceThreadMembers(r.Context(), actor, r.PathValue("thread_id"), input)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) replaceContextThreadMembers(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	var input model.ReplaceThreadMembersRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	value, err := h.application.ReplaceContextThreadMembers(r.Context(), actor, contextFromPath(r), input)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) createThread(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	var input model.CreateThreadRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	value, err := h.application.CreateThread(r.Context(), actor, input)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) listThreads(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	limit, err := integerQuery(r, "limit", 0)
	if err != nil {
		writeError(w, r, model.Invalid("im.query_invalid"))
		return
	}
	values, err := h.application.ListThreads(r.Context(), actor, limit)
	writeResult(w, r, http.StatusOK, map[string]any{"threads": values}, err)
}

func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	var input model.CreateMessageRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	value, err := h.application.SendMessage(r.Context(), actor, r.PathValue("thread_id"), input)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) sendContextMessage(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	var input model.CreateMessageRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	value, err := h.application.SendContextMessage(r.Context(), actor, contextFromPath(r), input)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	after, err := integer64Query(r, "after_sequence", 0)
	if err != nil {
		writeError(w, r, model.Invalid("im.query_invalid"))
		return
	}
	limit, err := integerQuery(r, "limit", 0)
	if err != nil {
		writeError(w, r, model.Invalid("im.query_invalid"))
		return
	}
	value, err := h.application.ListMessages(r.Context(), actor, r.PathValue("thread_id"), after, limit)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) searchMessages(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	limit, err := integerQuery(r, "limit", 0)
	if err != nil {
		writeError(w, r, model.Invalid("im.query_invalid"))
		return
	}
	values, err := h.application.SearchMessages(r.Context(), actor, model.SearchMessagesRequest{ThreadID: r.URL.Query().Get("thread_id"), Query: r.URL.Query().Get("q"), Tag: r.URL.Query().Get("tag"), Limit: limit})
	writeResult(w, r, http.StatusOK, map[string]any{"messages": values}, err)
}

func (h *Handler) listReceipts(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	values, err := h.application.ListReadCursors(r.Context(), actor, r.PathValue("thread_id"))
	writeResult(w, r, http.StatusOK, map[string]any{"receipts": values}, err)
}

func (h *Handler) markRead(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	var input model.ReadRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	value, err := h.application.MarkRead(r.Context(), actor, r.PathValue("thread_id"), input)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	r.Body = http.MaxBytesReader(w, r.Body, 26*1024*1024)
	if err := r.ParseMultipartForm(1024 * 1024); err != nil {
		writeError(w, r, &model.Error{Class: "bad_request", Code: "im.attachment_upload_invalid", Cause: err})
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, &model.Error{Class: "bad_request", Code: "im.attachment_upload_invalid", Cause: err})
		return
	}
	defer file.Close()
	value, err := h.application.UploadAttachment(r.Context(), actor, header.Filename, header.Header.Get("Content-Type"), file)
	writeResult(w, r, http.StatusCreated, value, err)
}

func (h *Handler) attachment(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	attachment, content, err := h.application.OpenAttachment(r.Context(), actor, r.PathValue("attachment_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer content.Close()
	writeAttachment(w, attachment, content)
}

func writeAttachment(w http.ResponseWriter, attachment model.Attachment, content io.Reader) {
	w.Header().Set("Content-Type", attachment.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(attachment.SizeBytes, 10))
	if attachment.Filename != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename}))
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, content); err != nil {
		telemetry.RecordHTTPError(w, "internal", "im.attachment_stream_failed", err)
	}
}

func (h *Handler) contextHistory(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	after, err := integer64Query(r, "after_sequence", 0)
	if err != nil {
		writeError(w, r, model.Invalid("im.query_invalid"))
		return
	}
	limit, err := integerQuery(r, "limit", 0)
	if err != nil {
		writeError(w, r, model.Invalid("im.query_invalid"))
		return
	}
	value, err := h.application.ContextHistory(r.Context(), actor, contextFromPath(r), after, limit)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) searchContextHistory(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	limit, err := integerQuery(r, "limit", 0)
	if err != nil {
		writeError(w, r, model.Invalid("im.query_invalid"))
		return
	}
	values, err := h.application.SearchContextHistory(r.Context(), actor, contextFromPath(r), model.SearchMessagesRequest{Query: r.URL.Query().Get("q"), Tag: r.URL.Query().Get("tag"), Limit: limit})
	writeResult(w, r, http.StatusOK, map[string]any{"messages": values}, err)
}

func (h *Handler) contextAttachment(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	attachment, content, err := h.application.OpenContextAttachment(r.Context(), actor, contextFromPath(r), r.PathValue("attachment_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	defer content.Close()
	writeAttachment(w, attachment, content)
}

func (h *Handler) listQuickReplies(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	values, err := h.application.ListQuickReplies(r.Context(), actor, r.URL.Query().Get("tag"))
	writeResult(w, r, http.StatusOK, map[string]any{"quick_replies": values}, err)
}

func contextFromPath(r *http.Request) model.ContextRef {
	return model.ContextRef{Namespace: r.PathValue("namespace"), ResourceType: r.PathValue("resource_type"), ResourceID: r.PathValue("resource_id")}
}

func (h *Handler) putQuickReply(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	var input model.PutQuickReplyRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	value, err := h.application.PutQuickReply(r.Context(), actor, r.PathValue("key"), input)
	writeResult(w, r, http.StatusOK, value, err)
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request, actor model.Actor) {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	state, err := h.application.SyncState(r.Context(), actor)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	last := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if cursor := strings.TrimSpace(r.URL.Query().Get("cursor")); cursor != "" {
		last = cursor
	}
	event := "im.sync"
	if last == state.Cursor {
		event = "im.ready"
	}
	if !writeSSE(w, controller, event, state) {
		return
	}
	last = state.Cursor
	poll := time.NewTicker(h.poll)
	heartbeat := time.NewTicker(h.heartbeat)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil || controller.Flush() != nil {
				return
			}
		case <-poll.C:
			next, err := h.application.SyncState(r.Context(), actor)
			if err != nil {
				return
			}
			if next.Cursor != last {
				if !writeSSE(w, controller, "im.sync", next) {
					return
				}
				last = next.Cursor
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, controller *http.ResponseController, event string, state model.SyncState) bool {
	payload, err := json.Marshal(state)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", state.Cursor, event, payload); err != nil {
		return false
	}
	return controller.Flush() == nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &model.Error{Class: "bad_request", Code: "im.request_json_invalid", Cause: err}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &model.Error{Class: "bad_request", Code: "im.request_json_invalid", Cause: err}
	}
	return nil
}

func integerQuery(r *http.Request, name string, fallback int) (int, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func integer64Query(r *http.Request, name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(r.URL.Query().Get(name))
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func writeResult(w http.ResponseWriter, r *http.Request, status int, value any, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpresponse.WriteJSON(w, status, value)
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := imHTTPStatus(apperror.KindOf(err)), "im.internal_error"
	var applicationError *model.Error
	if errors.As(err, &applicationError) {
		code = applicationError.Code
	}
	httpresponse.WriteError(w, r, httpresponse.Failure{Status: status, Code: code, Cause: err})
}

func imHTTPStatus(kind apperror.ErrorKind) int {
	switch kind {
	case apperror.KindBadRequest:
		return http.StatusBadRequest
	case apperror.KindForbidden:
		return http.StatusForbidden
	case apperror.KindNotFound:
		return http.StatusNotFound
	case apperror.KindConflict:
		return http.StatusConflict
	case apperror.KindRateLimited:
		return http.StatusTooManyRequests
	case apperror.KindUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
